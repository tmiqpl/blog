package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/models"
)

// PostFilter 描述后台文章列表的筛选条件。
type PostFilter struct {
	Status  string // "" 全部 | "published" 已发布 | "draft" 草稿
	Keyword string
}

func (f PostFilter) where() (string, []any) {
	var (
		conds []string
		args  []any
	)

	switch f.Status {
	case "published":
		conds = append(conds, "p.published = 1")
	case "draft":
		conds = append(conds, "p.published = 0")
	}

	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		conds = append(conds, "(p.title LIKE ? OR p.slug LIKE ? OR p.content LIKE ?)")
		args = append(args, like, like, like)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// GetPostByID 按 ID 获取文章，包含草稿。tags 为标签名列表。
func (s *Store) GetPostByID(id int64) (*models.Post, error) {
	row := s.db.QueryRow(`SELECT `+postColumns+` FROM posts p WHERE p.id = ?`, id)
	p, err := scanPost(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询文章失败: %w", err)
	}
	if err := s.attachTags([]*models.Post{p}); err != nil {
		return nil, err
	}
	return p, nil
}

// ListPostsAdmin 分页返回全部文章（含草稿），供后台列表使用。
func (s *Store) ListPostsAdmin(f PostFilter, limit, offset int) ([]*models.Post, int, error) {
	where, args := f.where()

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM posts p`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计文章数量失败: %w", err)
	}

	query := `SELECT ` + postColumns + ` FROM posts p` + where +
		` ORDER BY p.updated_at DESC, p.id DESC LIMIT ? OFFSET ?`
	rows, err := s.db.Query(query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询后台文章列表失败: %w", err)
	}
	defer rows.Close()

	posts, err := collectPosts(rows)
	if err != nil {
		return nil, 0, err
	}
	if err := s.attachTags(posts); err != nil {
		return nil, 0, err
	}
	return posts, total, nil
}

// SetPublished 切换文章的发布状态。
func (s *Store) SetPublished(id int64, published bool) error {
	flag := 0
	if published {
		flag = 1
	}
	res, err := s.db.Exec(
		`UPDATE posts SET published = ?, updated_at = ? WHERE id = ?`,
		flag, time.Now().UTC().Format(timeLayout), id)
	if err != nil {
		return fmt.Errorf("更新发布状态失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePostByID 按 ID 删除文章。
func (s *Store) DeletePostByID(id int64) error {
	res, err := s.db.Exec(`DELETE FROM posts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除文章失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SlugTaken 判断 slug 是否已被其他文章占用。
func (s *Store) SlugTaken(slug string, excludeID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM posts WHERE slug = ? AND id <> ?`, slug, excludeID).Scan(&n)
	return n > 0, err
}

// AdminStats 汇总后台仪表盘需要的统计数据。
type AdminStats struct {
	TotalPosts     int
	PublishedPosts int
	DraftPosts     int
	TotalTags      int
	TotalWords     int
	LatestPost     *models.Post
}

// Stats 计算后台仪表盘统计数据。
func (s *Store) Stats() (AdminStats, error) {
	var st AdminStats

	err := s.db.QueryRow(`
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN published = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN published = 0 THEN 1 ELSE 0 END), 0)
		FROM posts`).Scan(&st.TotalPosts, &st.PublishedPosts, &st.DraftPosts)
	if err != nil {
		return st, fmt.Errorf("统计文章失败: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM tags`).Scan(&st.TotalTags); err != nil {
		return st, fmt.Errorf("统计标签失败: %w", err)
	}

	if err := s.db.QueryRow(`SELECT COALESCE(SUM(LENGTH(content)), 0) FROM posts`).Scan(&st.TotalWords); err != nil {
		return st, fmt.Errorf("统计字数失败: %w", err)
	}

	row := s.db.QueryRow(
		`SELECT ` + postColumns + ` FROM posts p ORDER BY p.updated_at DESC LIMIT 1`)
	if latest, err := scanPost(row); err == nil {
		st.LatestPost = latest
	} else if !errors.Is(err, sql.ErrNoRows) {
		return st, err
	}

	return st, nil
}

// TagUsage 表示标签及其使用情况（含草稿）。
type TagUsage struct {
	models.Tag
	PublishedCount int
	DraftCount     int
}

// ListTagsAdmin 返回全部标签及其已发布/草稿文章数量。
func (s *Store) ListTagsAdmin() ([]TagUsage, error) {
	rows, err := s.db.Query(`
		SELECT t.id, t.name, t.slug,
		       COALESCE(SUM(CASE WHEN p.published = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN p.published = 0 THEN 1 ELSE 0 END), 0)
		FROM tags t
		LEFT JOIN post_tags pt ON pt.tag_id = t.id
		LEFT JOIN posts p      ON p.id = pt.post_id
		GROUP BY t.id, t.name, t.slug
		ORDER BY COUNT(p.id) DESC, t.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询标签失败: %w", err)
	}
	defer rows.Close()

	var out []TagUsage
	for rows.Next() {
		var u TagUsage
		if err := rows.Scan(&u.ID, &u.Name, &u.Slug, &u.PublishedCount, &u.DraftCount); err != nil {
			return nil, err
		}
		u.Count = u.PublishedCount + u.DraftCount
		out = append(out, u)
	}
	return out, rows.Err()
}

// RenameTag 修改标签名称与 slug，名称重复时返回错误。
func (s *Store) RenameTag(id int64, name, slug string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("标签名不能为空")
	}
	_, err := s.db.Exec(`UPDATE tags SET name = ?, slug = ? WHERE id = ?`, name, slug, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("标签名或 slug 已存在")
		}
		return fmt.Errorf("重命名标签失败: %w", err)
	}
	return nil
}

// DeleteTag 删除标签；当 removePosts 为 true 时同时删除关联文章。
// 返回被删除的文章数量。
func (s *Store) DeleteTag(id int64, removePosts bool) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	removed := 0
	if removePosts {
		res, err := tx.Exec(
			`DELETE FROM posts WHERE id IN (SELECT post_id FROM post_tags WHERE tag_id = ?)`, id)
		if err != nil {
			return 0, fmt.Errorf("删除标签下文章失败: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			removed = int(n)
		}
	}

	if _, err := tx.Exec(`DELETE FROM post_tags WHERE tag_id = ?`, id); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM tags WHERE id = ?`, id)
	if err != nil {
		return 0, fmt.Errorf("删除标签失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return removed, tx.Commit()
}

// CleanupOrphanTags 清理没有任何文章引用的标签，返回清理数量。
func (s *Store) CleanupOrphanTags() (int, error) {
	res, err := s.db.Exec(
		`DELETE FROM tags WHERE id NOT IN (SELECT DISTINCT tag_id FROM post_tags)`)
	if err != nil {
		return 0, fmt.Errorf("清理空标签失败: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}
