// Package markdown 封装 Markdown -> HTML 的渲染逻辑。
package markdown

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

var (
	once     sync.Once
	renderer goldmark.Markdown
)

func initRenderer() {
	renderer = goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,            // 表格、删除线、任务列表、自动链接
			extension.Footnote,       // 脚注
			extension.Typographer,    // 智能标点
			extension.DefinitionList, // 定义列表
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(), // 为标题生成 id，便于锚点跳转
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(), // 单个换行即换行，符合中文书写习惯
			html.WithUnsafe(),    // 允许内嵌 HTML
		),
	)
}

// Render 将 Markdown 源文本渲染为 HTML 字符串。
// 每次渲染都会创建独立的解析上下文，保证锚点 id 不会跨文档串号，可安全并发调用。
func Render(source string) string {
	once.Do(initRenderer)

	ctx := parser.NewContext(parser.WithIDs(newHeadingIDs()))
	var buf bytes.Buffer
	if err := renderer.Convert([]byte(source), &buf, parser.WithContext(ctx)); err != nil {
		return "<p>（内容渲染失败）</p>"
	}
	return buf.String()
}

// headingIDs 生成对中文友好的标题锚点 id。
// goldmark 内置实现会直接丢弃非 ASCII 字符，导致中文标题全部退化成 heading / heading-1。
type headingIDs struct {
	used map[string]bool
}

func newHeadingIDs() *headingIDs {
	return &headingIDs{used: make(map[string]bool)}
}

func (h *headingIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	base := slugifyHeading(string(value))
	if base == "" {
		if kind == ast.KindHeading {
			base = "section"
		} else {
			base = "id"
		}
	}

	id := base
	for i := 1; h.used[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	h.used[id] = true
	return []byte(id)
}

func (h *headingIDs) Put(value []byte) {
	h.used[string(value)] = true
}

// slugifyHeading 保留字母、数字与中日韩文字，其余字符折叠为连字符。
func slugifyHeading(s string) string {
	var b strings.Builder
	prevDash := false

	for _, r := range strings.TrimSpace(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			prevDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

var (
	codeFence   = regexp.MustCompile("(?s)```.*?```")
	inlineCode  = regexp.MustCompile("`([^`]*)`")
	imageSyntax = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	linkSyntax  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	heading     = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	blockquote  = regexp.MustCompile(`(?m)^>\s*`)
	listMark    = regexp.MustCompile(`(?m)^\s*([-*+]|\d+\.)\s+`)
	htmlTag     = regexp.MustCompile(`<[^>]+>`)
	emphasis    = regexp.MustCompile(`(\*\*|__|\*|_|~~)`)
	multiBlank  = regexp.MustCompile(`\n{2,}`)
)

// PlainText 将 Markdown 粗略转换为纯文本，用于生成摘要与搜索。
// 围栏代码块整体丢弃，行内代码仅去掉反引号并保留内容——摘要里「使用 goldmark 渲染」比「使用 渲染」通顺得多。
func PlainText(source string) string {
	s := source
	s = codeFence.ReplaceAllString(s, " ")
	s = imageSyntax.ReplaceAllString(s, " ")
	s = linkSyntax.ReplaceAllString(s, "$1")
	s = inlineCode.ReplaceAllString(s, "$1")
	s = heading.ReplaceAllString(s, "")
	s = blockquote.ReplaceAllString(s, "")
	s = listMark.ReplaceAllString(s, "")
	s = htmlTag.ReplaceAllString(s, " ")
	s = emphasis.ReplaceAllString(s, "")
	s = multiBlank.ReplaceAllString(s, "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// Excerpt 生成不超过 maxRunes 个字符的摘要。
func Excerpt(source string, maxRunes int) string {
	text := PlainText(source)
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "……"
}
