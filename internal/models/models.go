// Package models 定义博客的核心数据结构。
package models

import "time"

// Post 表示一篇博客文章。
type Post struct {
	ID        int64
	Title     string
	Slug      string
	Summary   string
	Content   string // 原始 Markdown 文本
	Published bool
	CreatedAt time.Time
	UpdatedAt time.Time
	Tags      []Tag
}

// Tag 表示一个标签，Count 为该标签下的文章数量。
type Tag struct {
	ID    int64
	Name  string
	Slug  string
	Count int
}

// ReadingMinutes 根据正文字数粗略估算阅读时长（按每分钟 400 字计）。
func (p *Post) ReadingMinutes() int {
	n := len([]rune(p.Content))
	if n == 0 {
		return 1
	}
	m := n / 400
	if n%400 != 0 {
		m++
	}
	if m < 1 {
		m = 1
	}
	return m
}
