package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/markdown"
	"github.com/tmiqpl/blog/internal/models"
)

// PostInput 是写入文章所需的字段集合。
type PostInput struct {
	Title     string
	Slug      string
	Summary   string
	Content   string
	Published bool
	CreatedAt time.Time
	Tags      []string
}

// UpsertPost 按 slug 新增或更新一篇文章，并同步其标签关系。
// 返回文章 ID 以及本次是否为新建。
func (s *Store) UpsertPost(in PostInput) (int64, bool, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return 0, false, errors.New("文章标题不能为空")
	}
	if in.Slug == "" {
		in.Slug = Slugify(in.Title)
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now().UTC()
	}
	if in.Summary == "" {
		in.Summary = markdown.Excerpt(in.Content, 120)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRow(`SELECT id FROM posts WHERE slug = ?`, in.Slug).Scan(&existingID)
	isNew := errors.Is(err, sql.ErrNoRows)
	if err != nil && !isNew {
		return 0, false, fmt.Errorf("查询已有文章失败: %w", err)
	}

	published := 0
	if in.Published {
		published = 1
	}
	ts := in.CreatedAt.UTC().Format(timeLayout)

	var postID int64
	if isNew {
		res, err := tx.Exec(
			`INSERT INTO posts(title, slug, summary, content, published, created_at, updated_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			in.Title, in.Slug, in.Summary, in.Content, published, ts, ts)
		if err != nil {
			return 0, false, fmt.Errorf("写入文章失败: %w", err)
		}
		if postID, err = res.LastInsertId(); err != nil {
			return 0, false, err
		}
	} else {
		postID = existingID
		if _, err := tx.Exec(
			`UPDATE posts
			 SET title = ?, summary = ?, content = ?, published = ?, created_at = ?, updated_at = ?
			 WHERE id = ?`,
			in.Title, in.Summary, in.Content, published, ts, time.Now().UTC().Format(timeLayout), postID); err != nil {
			return 0, false, fmt.Errorf("更新文章失败: %w", err)
		}
	}

	// 重建标签关系
	if _, err := tx.Exec(`DELETE FROM post_tags WHERE post_id = ?`, postID); err != nil {
		return 0, false, err
	}
	for _, name := range in.Tags {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		tagID, err := ensureTag(tx, name)
		if err != nil {
			return 0, false, err
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO post_tags(post_id, tag_id) VALUES(?, ?)`, postID, tagID); err != nil {
			return 0, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return postID, isNew, nil
}

// DeletePost 按 slug 删除文章，返回是否确实删除了记录。
func (s *Store) DeletePost(slug string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM posts WHERE slug = ?`, slug)
	if err != nil {
		return false, fmt.Errorf("删除文章失败: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AllPosts 返回全部文章（含未发布），供导出使用。
func (s *Store) AllPosts() ([]*models.Post, error) {
	rows, err := s.db.Query(
		`SELECT ` + postColumns + ` FROM posts p ORDER BY p.created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	return posts, s.attachTags(posts)
}

// ensureTag 按名称取得标签 ID，不存在则创建。
func ensureTag(tx *sql.Tx, name string) (int64, error) {
	slug := Slugify(name)

	var id int64
	err := tx.QueryRow(`SELECT id FROM tags WHERE slug = ? OR name = ?`, slug, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	res, err := tx.Exec(`INSERT INTO tags(name, slug) VALUES(?, ?)`, name, slug)
	if err != nil {
		return 0, fmt.Errorf("创建标签 %q 失败: %w", name, err)
	}
	return res.LastInsertId()
}
