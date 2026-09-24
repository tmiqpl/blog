package content

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFullFrontMatter(t *testing.T) {
	raw := `---
title: 我的第一篇文章
slug: my-first-post
date: 2026-09-01 10:30
tags: Go, 后端
summary: 这是摘要
draft: true
---

# 正文标题

正文内容。`

	fm, body, err := Parse(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if fm.Title != "我的第一篇文章" {
		t.Errorf("title = %q", fm.Title)
	}
	if fm.Slug != "my-first-post" {
		t.Errorf("slug = %q", fm.Slug)
	}
	if fm.Summary != "这是摘要" {
		t.Errorf("summary = %q", fm.Summary)
	}
	if !fm.Draft {
		t.Error("draft 应为 true")
	}
	if want := []string{"Go", "后端"}; !reflect.DeepEqual(fm.Tags, want) {
		t.Errorf("tags = %v，期望 %v", fm.Tags, want)
	}
	if want := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC); !fm.Date.Equal(want) {
		t.Errorf("date = %v，期望 %v", fm.Date, want)
	}
	if !strings.HasPrefix(body, "# 正文标题") {
		t.Errorf("正文解析错误: %q", body)
	}
}

func TestParseWithoutFrontMatter(t *testing.T) {
	raw := "# 只有正文\n\n内容"

	fm, body, err := Parse(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if fm.Title != "" || len(fm.Tags) != 0 || !fm.Date.IsZero() {
		t.Errorf("无 front matter 时应返回零值，实际 %+v", fm)
	}
	if body != raw {
		t.Errorf("正文应与原文一致，实际 %q", body)
	}
}

func TestParseMissingClosingDelimiter(t *testing.T) {
	raw := "---\ntitle: 缺少结束符\n\n# 正文"

	if _, _, err := Parse(raw); err == nil {
		t.Error("缺少结束分隔符时应返回错误")
	}
}

func TestParseTagFormats(t *testing.T) {
	cases := map[string][]string{
		"tags: Go, 后端":    {"Go", "后端"},
		"tags: [Go, 后端]":  {"Go", "后端"},
		"tags: Go，后端":     {"Go", "后端"},
		"tags: Go; 后端；前端": {"Go", "后端", "前端"},
		"tags:":           nil,
	}

	for line, want := range cases {
		raw := "---\ntitle: t\n" + line + "\n---\n正文"
		fm, _, err := Parse(raw)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", line, err)
		}
		if !reflect.DeepEqual(fm.Tags, want) {
			t.Errorf("%q → tags = %v，期望 %v", line, fm.Tags, want)
		}
	}
}

func TestParseDateFormats(t *testing.T) {
	cases := []string{
		"2026-09-01",
		"2026-09-01 10:30",
		"2026-09-01T10:30",
		"2026-09-01T10:30:00Z",
		"2026/09/01",
	}

	for _, value := range cases {
		raw := "---\ntitle: t\ndate: " + value + "\n---\n正文"
		fm, _, err := Parse(raw)
		if err != nil {
			t.Errorf("日期 %q 解析失败: %v", value, err)
			continue
		}
		if fm.Date.Year() != 2026 || fm.Date.Month() != time.September || fm.Date.Day() != 1 {
			t.Errorf("日期 %q 解析结果错误: %v", value, fm.Date)
		}
	}
}

func TestParseInvalidDate(t *testing.T) {
	raw := "---\ntitle: t\ndate: 昨天\n---\n正文"
	if _, _, err := Parse(raw); err == nil {
		t.Error("非法日期应返回错误")
	}
}

func TestParsePublishedInvertsDraft(t *testing.T) {
	raw := "---\ntitle: t\npublished: false\n---\n正文"
	fm, _, err := Parse(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !fm.Draft {
		t.Error("published: false 应等价于 draft: true")
	}
}

func TestParseIgnoresUnknownFields(t *testing.T) {
	raw := "---\ntitle: t\nunknown_field: whatever\n---\n正文"
	if _, _, err := Parse(raw); err != nil {
		t.Errorf("未知字段应被忽略，实际报错: %v", err)
	}
}

func TestParseCRLF(t *testing.T) {
	raw := "---\r\ntitle: 回车换行\r\ntags: Go, 后端\r\n---\r\n\r\n正文"
	fm, body, err := Parse(raw)
	if err != nil {
		t.Fatalf("CRLF 文件解析失败: %v", err)
	}
	if fm.Title != "回车换行" {
		t.Errorf("title = %q", fm.Title)
	}
	if len(fm.Tags) != 2 {
		t.Errorf("tags = %v", fm.Tags)
	}
	if !strings.Contains(body, "正文") {
		t.Errorf("正文解析错误: %q", body)
	}
}
