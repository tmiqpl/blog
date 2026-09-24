package web

import (
	"testing"

	"github.com/tmiqpl/blog/internal/markdown"
)

func TestExtractTOCFromRenderedMarkdown(t *testing.T) {
	rendered := markdown.Render(`# 文章标题

## 第一节

内容

### 小节 A

内容

## 第二节

内容

#### 四级标题不进目录
`)

	items := extractTOC(rendered)
	if len(items) != 3 {
		t.Fatalf("应提取 3 个标题，实际 %d：%+v", len(items), items)
	}

	want := []struct {
		id    string
		text  string
		level int
	}{
		{"第一节", "第一节", 2},
		{"小节-a", "小节 A", 3},
		{"第二节", "第二节", 2},
	}

	for i, w := range want {
		got := items[i]
		if got.ID != w.id || got.Text != w.text || got.Level != w.level {
			t.Errorf("第 %d 项 = %+v，期望 {ID:%q Text:%q Level:%d}", i, got, w.id, w.text, w.level)
		}
	}
}

func TestExtractTOCSkipsWhenTooFewHeadings(t *testing.T) {
	cases := map[string]string{
		"没有标题":   "只有一段正文。",
		"只有一个标题": "## 唯一一节\n\n内容",
		"只有两个标题": "## 一\n\n## 二",
	}

	for name, src := range cases {
		if items := extractTOC(markdown.Render(src)); items != nil {
			t.Errorf("%s：标题不足 3 个时应返回 nil，实际 %+v", name, items)
		}
	}
}

func TestExtractTOCStripsInlineTags(t *testing.T) {
	rendered := markdown.Render("## 使用 `goldmark` 渲染\n\n## 第二节\n\n## 带 **加粗** 的标题")

	items := extractTOC(rendered)
	if len(items) != 3 {
		t.Fatalf("应提取 3 个标题，实际 %d", len(items))
	}

	// 标题里的 <code> / <strong> 应被剥掉，只留文字
	for _, item := range items {
		for _, unwanted := range []string{"<", ">", "`"} {
			if containsRune(item.Text, unwanted) {
				t.Errorf("标题文字 %q 中残留了标记 %q", item.Text, unwanted)
			}
		}
	}

	if items[0].Text != "使用 goldmark 渲染" {
		t.Errorf("行内代码应保留文字并去掉反引号，实际 %q", items[0].Text)
	}
	if items[2].Text != "带 加粗 的标题" {
		t.Errorf("加粗应只去掉标记，实际 %q", items[2].Text)
	}
}

func TestExtractTOCDecodesEntities(t *testing.T) {
	// 标题里出现 & < > 时 goldmark 会转义，提取后应还原
	rendered := `<h2 id="a">A &amp; B</h2><h2 id="b">1 &lt; 2</h2><h2 id="c">引号 &quot;x&quot;</h2>`

	items := extractTOC(rendered)
	if len(items) != 3 {
		t.Fatalf("应提取 3 个标题，实际 %d", len(items))
	}

	want := []string{`A & B`, `1 < 2`, `引号 "x"`}
	for i, w := range want {
		if items[i].Text != w {
			t.Errorf("第 %d 项 = %q，期望 %q", i, items[i].Text, w)
		}
	}
}

func TestExtractTOCIgsnoresH1AndH4(t *testing.T) {
	rendered := `<h1 id="t">标题</h1><h2 id="a">A</h2><h2 id="b">B</h2><h2 id="c">C</h2><h4 id="d">D</h4>`

	items := extractTOC(rendered)
	if len(items) != 3 {
		t.Fatalf("只应提取 h2/h3，实际 %d：%+v", len(items), items)
	}
	for _, item := range items {
		if item.Level != 2 {
			t.Errorf("层级应为 2，实际 %d", item.Level)
		}
	}
}

func TestExtractTOCHandlesEmptyInput(t *testing.T) {
	if items := extractTOC(""); items != nil {
		t.Errorf("空输入应返回 nil，实际 %+v", items)
	}
}

func TestExtractTOCSkipsHeadingsWithoutText(t *testing.T) {
	rendered := `<h2 id="a">有内容</h2><h2 id="b">   </h2><h2 id="c">也有内容</h2><h2 id="d">还有内容</h2>`

	items := extractTOC(rendered)
	if len(items) != 3 {
		t.Fatalf("空标题应被跳过，实际 %d：%+v", len(items), items)
	}
	for _, item := range items {
		if item.ID == "b" {
			t.Error("空标题不应出现在目录里")
		}
	}
}

func TestStripDuplicateTitle(t *testing.T) {
	cases := []struct {
		name    string
		content string
		title   string
		want    string
	}{
		{
			name:    "首行 H1 与标题相同",
			content: "# 我的标题\n\n正文内容",
			title:   "我的标题",
			// 只删掉标题那一行，原来分隔用的空行会保留下来
			want: "\n正文内容",
		},
		{
			name:    "首行 H1 与标题不同",
			content: "# 另一个标题\n\n正文",
			title:   "我的标题",
			want:    "# 另一个标题\n\n正文",
		},
		{
			name:    "首行不是标题",
			content: "直接开始写正文\n\n# 后面的标题",
			title:   "我的标题",
			want:    "直接开始写正文\n\n# 后面的标题",
		},
		{
			name:    "前面有空行",
			content: "\n\n# 我的标题\n\n正文",
			title:   "我的标题",
			want:    "\n\n\n正文",
		},
		{
			name:    "只有标题没有正文",
			content: "# 我的标题",
			title:   "我的标题",
			want:    "",
		},
		{
			name:    "标题前后有空格",
			content: "#   我的标题  \n\n正文",
			title:   "  我的标题 ",
			want:    "\n正文",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripDuplicateTitle(c.content, c.title); got != c.want {
				t.Errorf("stripDuplicateTitle() = %q，期望 %q", got, c.want)
			}
		})
	}
}

func containsRune(s string, r string) bool {
	for _, c := range s {
		if string(c) == r {
			return true
		}
	}
	return false
}
