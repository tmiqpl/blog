package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/tmiqpl/blog/internal/models"
)

// ListTags 返回所有标签及其文章数量，按文章数倒序。
func (s *Store) ListTags() ([]models.Tag, error) {
	rows, err := s.db.Query(
		`SELECT t.id, t.name, t.slug, COUNT(p.id) AS cnt
		 FROM tags t
		 JOIN post_tags pt ON pt.tag_id = t.id
		 JOIN posts p      ON p.id = pt.post_id AND p.published = 1
		 GROUP BY t.id, t.name, t.slug
		 ORDER BY cnt DESC, t.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询标签列表失败: %w", err)
	}
	defer rows.Close()

	tags := make([]models.Tag, 0, 8)
	for rows.Next() {
		var t models.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.Count); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// GetTagBySlug 按 slug 查询标签。
func (s *Store) GetTagBySlug(slug string) (*models.Tag, error) {
	var t models.Tag
	err := s.db.QueryRow(
		`SELECT id, name, slug FROM tags WHERE slug = ?`, slug).
		Scan(&t.ID, &t.Name, &t.Slug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("查询标签失败: %w", err)
	}
	if t.Count, err = s.CountPostsByTag(slug); err != nil {
		return nil, err
	}
	return &t, nil
}

// TotalTags 统计标签总数。
func (s *Store) TotalTags() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM tags`).Scan(&n)
	return n, err
}
