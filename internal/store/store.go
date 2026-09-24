// Package store 负责 SQLite 的连接、建表、种子数据与数据访问。
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 纯 Go 实现的 SQLite 驱动，注册名为 "sqlite"
)

// ErrNotFound 表示查询目标不存在。
var ErrNotFound = errors.New("资源不存在")

// Store 封装数据库连接与查询方法。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）指定路径的 SQLite 数据库，并完成建表。
//
// 这里不会写入任何示例数据：站点首次启动时处于「未初始化」状态，由 /init 页面
// 引导站主完成设置；示例文章则由初始化页面按站主的选择调用 SeedSamplePosts 写入。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}

	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 建议限制为单写连接，避免写锁竞争。
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// SeedSamplePosts 写入内置示例文章及其标签关联，仅在文章表为空时生效。
func (s *Store) SeedSamplePosts() error { return s.seed() }

// Close 关闭数据库连接。
func (s *Store) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS posts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT    NOT NULL,
    slug       TEXT    NOT NULL UNIQUE,
    summary    TEXT    NOT NULL DEFAULT '',
    content    TEXT    NOT NULL DEFAULT '',
    published  INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS tags (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    slug TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS post_tags (
    post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY (post_id, tag_id)
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_posts_created  ON posts(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_posts_publish  ON posts(published, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_post_tags_tag  ON post_tags(tag_id);
`

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("初始化表结构失败: %w", err)
	}
	return nil
}

const timeLayout = time.RFC3339

func parseTime(v string) time.Time {
	t, err := time.Parse(timeLayout, v)
	if err != nil {
		return time.Time{}
	}
	return t
}
