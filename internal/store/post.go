package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/tmiqpl/blog/internal/models"
)

const postColumns = `p.id, p.title, p.slug, p.summary, p.content, p.published, p.created_at, p.updated_at`

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的公共能力。
type rowScanner interface {
	Scan(dest ...any) error
}

func scanPost(sc rowScanner) (*models.Post, error) {
	var (
		p         models.Post
		published int
		created   string
		updated   string
	)
	if err := sc.Scan(&p.ID, &p.Title, &p.Slug, &p.Summary, &p.Content, &published, &created, &updated); err != nil {
		return nil, err
	}
	p.Published = published != 0
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return &p, nil
}

// ListPosts 返回已发布文章，按发布时间倒序分页。
func (s *Store) ListPosts(limit, offset int) ([]*models.Post, error) {
	rows, err := s.db.Query(
		`SELECT `+postColumns+` FROM posts p
		 WHERE p.published = 1
		 ORDER BY p.created_at DESC, p.id DESC
		 LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("查询文章列表失败: %w", err)
	}
	defer rows.Close()

	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	return posts, s.attachTags(posts)
}

// ListPostsByTag 返回某个标签下的已发布文章。
func (s *Store) ListPostsByTag(tagSlug string, limit, offset int) ([]*models.Post, error) {
	rows, err := s.db.Query(
		`SELECT `+postColumns+` FROM posts p
		 JOIN post_tags pt ON pt.post_id = p.id
		 JOIN tags t       ON t.id = pt.tag_id
		 WHERE p.published = 1 AND t.slug = ?
		 ORDER BY p.created_at DESC, p.id DESC
		 LIMIT ? OFFSET ?`, tagSlug, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("按标签查询文章失败: %w", err)
	}
	defer rows.Close()

	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	return posts, s.attachTags(posts)
}

// SearchPosts 在标题与正文中做模糊匹配。
func (s *Store) SearchPosts(keyword string, limit int) ([]*models.Post, error) {
	like := "%" + keyword + "%"
	rows, err := s.db.Query(
		`SELECT `+postColumns+` FROM posts p
		 WHERE p.published = 1 AND (p.title LIKE ? OR p.content LIKE ? OR p.summary LIKE ?)
		 ORDER BY p.created_at DESC
		 LIMIT ?`, like, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("搜索文章失败: %w", err)
	}
	defer rows.Close()

	posts, err := collectPosts(rows)
	if err != nil {
		return nil, err
	}
	return posts, s.attachTags(posts)
}

// GetPostBySlug 按 slug 获取单篇文章（含标签）。
func (s *Store) GetPostBySlug(slug string) (*models.Post, error) {
	row := s.db.QueryRow(
		`SELECT `+postColumns+` FROM posts p WHERE p.slug = ? AND p.published = 1`, slug)
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

// RelatedPosts 返回与给定文章共享标签的其他文章。
func (s *Store) RelatedPosts(postID int64, limit int) ([]*models.Post, error) {
	rows, err := s.db.Query(
		`SELECT `+postColumns+`, COUNT(*) AS shared
		 FROM posts p
		 JOIN post_tags pt  ON pt.post_id = p.id
		 WHERE p.published = 1 AND p.id <> ?
		   AND pt.tag_id IN (SELECT tag_id FROM post_tags WHERE post_id = ?)
		 GROUP BY p.id
		 ORDER BY shared DESC, p.created_at DESC
		 LIMIT ?`, postID, postID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询相关文章失败: %w", err)
	}
	defer rows.Close()

	var posts []*models.Post
	for rows.Next() {
		var (
			p         models.Post
			published int
			created   string
			updated   string
			shared    int
		)
		if err := rows.Scan(&p.ID, &p.Title, &p.Slug, &p.Summary, &p.Content, &published, &created, &updated, &shared); err != nil {
			return nil, err
		}
		p.Published = published != 0
		p.CreatedAt = parseTime(created)
		p.UpdatedAt = parseTime(updated)
		posts = append(posts, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return posts, nil
}

// CountPosts 统计已发布文章总数。
func (s *Store) CountPosts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM posts WHERE published = 1`).Scan(&n)
	return n, err
}

// CountPostsByTag 统计某标签下已发布文章数量。
func (s *Store) CountPostsByTag(tagSlug string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM posts p
		 JOIN post_tags pt ON pt.post_id = p.id
		 JOIN tags t       ON t.id = pt.tag_id
		 WHERE p.published = 1 AND t.slug = ?`, tagSlug).Scan(&n)
	return n, err
}

func collectPosts(rows *sql.Rows) ([]*models.Post, error) {
	posts := make([]*models.Post, 0, 8)
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历文章结果失败: %w", err)
	}
	return posts, nil
}

// attachTags 一次性为一批文章填充标签，避免 N+1 查询。
func (s *Store) attachTags(posts []*models.Post) error {
	if len(posts) == 0 {
		return nil
	}

	index := make(map[int64]*models.Post, len(posts))
	placeholders := make([]string, 0, len(posts))
	args := make([]any, 0, len(posts))
	for _, p := range posts {
		index[p.ID] = p
		placeholders = append(placeholders, "?")
		args = append(args, p.ID)
	}

	query := `SELECT pt.post_id, t.id, t.name, t.slug
	          FROM post_tags pt
	          JOIN tags t ON t.id = pt.tag_id
	          WHERE pt.post_id IN (` + strings.Join(placeholders, ",") + `)
	          ORDER BY t.name`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return fmt.Errorf("查询文章标签失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			postID int64
			tag    models.Tag
		)
		if err := rows.Scan(&postID, &tag.ID, &tag.Name, &tag.Slug); err != nil {
			return err
		}
		if p, ok := index[postID]; ok {
			p.Tags = append(p.Tags, tag)
		}
	}
	return rows.Err()
}
