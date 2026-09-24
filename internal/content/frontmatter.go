// Package content 负责解析带 front matter 的 Markdown 文章文件。
//
// 文件格式示例：
//
//	---
//	title: 我的第一篇文章
//	slug: my-first-post
//	date: 2026-09-01
//	tags: Go, 后端
//	summary: 可选，不填则自动截取正文
//	draft: false
//	---
//
//	# 正文标题
//	...
package content

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FrontMatter 描述文章文件的元信息。
type FrontMatter struct {
	Title   string
	Slug    string
	Summary string
	Tags    []string
	Date    time.Time
	Draft   bool
}

var dateLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04",
	"2006-01-02",
	"2006/01/02",
}

// Parse 拆分 front matter 与正文。没有 front matter 时返回零值与完整内容。
func Parse(raw string) (FrontMatter, string, error) {
	var fm FrontMatter

	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(raw, "---\n") {
		return fm, raw, nil
	}

	rest := raw[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm, "", fmt.Errorf("front matter 缺少结束分隔符 ---")
	}

	header := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\n")
	body = strings.TrimPrefix(body, "\n")

	if err := parseHeader(header, &fm); err != nil {
		return fm, "", err
	}
	return fm, body, nil
}

func parseHeader(header string, fm *FrontMatter) error {
	sc := bufio.NewScanner(strings.NewReader(header))
	lineNo := 0

	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("第 %d 行格式错误，应为 key: value", lineNo)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)

		switch key {
		case "title":
			fm.Title = value
		case "slug":
			fm.Slug = value
		case "summary", "description":
			fm.Summary = value
		case "date", "created":
			t, err := parseDate(value)
			if err != nil {
				return fmt.Errorf("第 %d 行日期格式无法识别: %q", lineNo, value)
			}
			fm.Date = t
		case "tags", "categories":
			fm.Tags = parseList(value)
		case "draft":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("第 %d 行 draft 应为布尔值: %q", lineNo, value)
			}
			fm.Draft = b
		case "published":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("第 %d 行 published 应为布尔值: %q", lineNo, value)
			}
			fm.Draft = !b
		default:
			// 未知字段直接忽略，保证向前兼容
		}
	}
	return sc.Err()
}

// parseList 支持 "a, b, c" 与 "[a, b, c]" 两种写法。
func parseList(value string) []string {
	value = strings.Trim(value, "[]")
	if value == "" {
		return nil
	}

	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	})

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseDate(value string) (time.Time, error) {
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析日期 %q", value)
}
