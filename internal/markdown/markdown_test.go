package markdown

import (
	"strings"
	"testing"
)

func TestRenderBasicMarkdown(t *testing.T) {
	html := Render("# 标题\n\n这是**加粗**和`代码`。")

	for _, want := range []string{"<h1", "标题", "<strong>加粗</strong>", "<code>代码</code>"} {
		if !strings.Contains(html, want) {
			t.Errorf("渲染结果缺少 %q，实际输出：\n%s", want, html)
		}
	}
}

func TestRenderChineseHeadingIDs(t *testing.T) {
	html := Render("## 关键配置\n\n### 开启 WAL 模式\n\n## 什么时候不适合")

	cases := []string{`id="关键配置"`, `id="开启-wal-模式"`, `id="什么时候不适合"`}
	for _, want := range cases {
		if !strings.Contains(html, want) {
			t.Errorf("中文标题锚点缺失 %q，实际输出：\n%s", want, html)
		}
	}
}

func TestRenderDuplicateHeadingIDs(t *testing.T) {
	html := Render("## 小结\n\n正文\n\n## 小结\n\n正文")

	if !strings.Contains(html, `id="小结"`) {
		t.Errorf("第一个同名标题应使用原始 id，实际输出：\n%s", html)
	}
	if !strings.Contains(html, `id="小结-1"`) {
		t.Errorf("第二个同名标题应追加序号，实际输出：\n%s", html)
	}
}

func TestRenderHeadingIDsAreIndependentPerDocument(t *testing.T) {
	first := Render("## 小结")
	second := Render("## 小结")

	if first != second {
		t.Errorf("相同输入应产生相同锚点，避免跨文档串号\nfirst:  %s\nsecond: %s", first, second)
	}
	if strings.Contains(second, "小结-1") {
		t.Errorf("锚点计数器不应在多次渲染之间累积，实际输出：\n%s", second)
	}
}

func TestRenderGFMExtensions(t *testing.T) {
	src := "| 列 A | 列 B |\n| --- | --- |\n| 1 | 2 |\n\n- [x] 已完成\n- [ ] 未完成\n\n~~删除线~~\n"
	html := Render(src)

	for _, want := range []string{"<table>", "<th>列 A</th>", `type="checkbox"`, "<del>删除线</del>"} {
		if !strings.Contains(html, want) {
			t.Errorf("GFM 扩展渲染缺失 %q，实际输出：\n%s", want, html)
		}
	}
}

func TestRenderHardWraps(t *testing.T) {
	// 中文书写习惯是一段一行，单个换行应保留为 <br>
	html := Render("第一行\n第二行")
	if !strings.Contains(html, "<br") {
		t.Errorf("单个换行应产生 <br>，实际输出：\n%s", html)
	}
}

func TestRenderEmptyInput(t *testing.T) {
	if got := Render(""); strings.TrimSpace(got) != "" {
		t.Errorf("空输入应渲染为空，实际得到 %q", got)
	}
}

func TestPlainText(t *testing.T) {
	src := "# 标题\n\n这是**正文**，含 `代码` 与 [链接](https://example.com)。\n\n```go\nfmt.Println()\n```\n\n> 引用\n"

	got := PlainText(src)

	for _, unwanted := range []string{"#", "**", "`", "```", "fmt.Println", "https://", ">", "["} {
		if strings.Contains(got, unwanted) {
			t.Errorf("纯文本中不应残留 Markdown 标记 %q，实际：%s", unwanted, got)
		}
	}
	for _, want := range []string{"标题", "正文", "代码", "链接", "引用"} {
		if !strings.Contains(got, want) {
			t.Errorf("纯文本中缺少 %q，实际：%s", want, got)
		}
	}
}

func TestExcerptTruncation(t *testing.T) {
	src := strings.Repeat("字", 200)

	got := Excerpt(src, 50)
	runes := []rune(got)

	// 50 个字符 + "……"
	if len(runes) != 52 {
		t.Errorf("摘要长度应为 52 个字符，实际 %d", len(runes))
	}
	if !strings.HasSuffix(got, "……") {
		t.Errorf("截断后应追加省略号，实际：%s", got)
	}
}

func TestExcerptShortInputUnchanged(t *testing.T) {
	src := "很短的正文。"
	if got := Excerpt(src, 120); got != src {
		t.Errorf("未超长时不应截断，期望 %q，实际 %q", src, got)
	}
}

func TestSlugifyHeading(t *testing.T) {
	cases := map[string]string{
		"Hello World": "hello-world",
		"关键配置":        "关键配置",
		"开启 WAL 模式":   "开启-wal-模式",
		"  A  B  ":    "a-b",
		"Go 1.22+ 路由": "go-122-路由",
		"---":         "",
		"符号!!!与???标点": "符号与标点",
	}

	for input, want := range cases {
		if got := slugifyHeading(input); got != want {
			t.Errorf("slugifyHeading(%q) = %q，期望 %q", input, got, want)
		}
	}
}
