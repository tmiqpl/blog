package web

import (
	"errors"
	"html"
	"html/template"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/tmiqpl/blog/internal/markdown"
	"github.com/tmiqpl/blog/internal/store"
)

// errorData 用于错误页。
type errorData struct {
	baseData
	Code    int
	Heading string
	Message string
}

// handleIndex 渲染首页文章列表。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	page := pageParam(r)

	total, err := s.store.CountPosts()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	posts, err := s.store.ListPosts(PerPage, (page-1)*PerPage)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := indexData{
		baseData:   s.base(r, "home"),
		Heading:    "最新文章",
		Subheading: "记录关于技术、工程与思考的一切",
		Posts:      posts,
		Page:       buildPagination(page, PerPage, total, "/"),
	}
	s.render(w, http.StatusOK, "index", data)
}

// handlePost 渲染文章详情页。
func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.PathValue("slug"))
	if slug == "" {
		s.notFound(w, r)
		return
	}

	post, err := s.store.GetPostBySlug(slug)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	related, err := s.store.RelatedPosts(post.ID, 3)
	if err != nil {
		s.logger.Warn("加载相关文章失败", "error", err)
	}

	rendered := markdown.Render(stripDuplicateTitle(post.Content, post.Title))

	data := postData{
		baseData: s.base(r, "home"),
		Post:     post,
		Content:  template.HTML(rendered), //nolint:gosec // 内容由站主本人撰写
		Related:  related,
		TOC:      extractTOC(rendered),
	}
	s.render(w, http.StatusOK, "post", data)
}

var (
	headingPattern = regexp.MustCompile(`(?is)<h([23])\b[^>]*\bid="([^"]+)"[^>]*>(.*?)</h[23]>`)
	tagPattern     = regexp.MustCompile(`<[^>]*>`)
)

// extractTOC 从渲染后的 HTML 中提取二、三级标题，用于生成文章目录。
// 标题过少时返回 nil —— 只有一两个小节的目录没有导航价值，反而占地方。
func extractTOC(rendered string) []tocItem {
	matches := headingPattern.FindAllStringSubmatch(rendered, -1)
	if len(matches) < 3 {
		return nil
	}

	items := make([]tocItem, 0, len(matches))
	for _, m := range matches {
		// 标题里可能嵌着 <code> 之类的内联标签，先剥掉再反转义
		text := html.UnescapeString(tagPattern.ReplaceAllString(m[3], ""))
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		items = append(items, tocItem{
			ID:    m[2],
			Text:  text,
			Level: int(m[1][0] - '0'),
		})
	}
	if len(items) < 3 {
		return nil
	}
	return items
}

// stripDuplicateTitle 去掉正文开头与文章标题完全相同的 H1。
// 详情页已经在正文上方显示标题，正文里再出现一次会显得重复；
// 但若 H1 与标题不同（作者有意为之），则原样保留。
func stripDuplicateTitle(content, title string) string {
	title = strings.TrimSpace(title)

	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if heading, ok := strings.CutPrefix(trimmed, "# "); ok && strings.TrimSpace(heading) == title {
			return strings.Join(append(lines[:i:i], lines[i+1:]...), "\n")
		}
		return content // 首行不是标题，不做处理
	}
	return content
}

// handleTags 渲染全部标签页。
func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	data := tagsData{
		baseData: s.base(r, "tags"),
		Heading:  "全部标签",
	}
	s.render(w, http.StatusOK, "tags", data)
}

// handleTag 渲染某个标签下的文章列表。
func (s *Server) handleTag(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.PathValue("slug"))
	if slug == "" {
		s.notFound(w, r)
		return
	}

	tag, err := s.store.GetTagBySlug(slug)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	page := pageParam(r)
	baseURL := "/tag/" + tag.Slug

	posts, err := s.store.ListPostsByTag(tag.Slug, PerPage, (page-1)*PerPage)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := indexData{
		baseData:   s.base(r, "tags"),
		Heading:    "标签：" + tag.Name,
		Subheading: "共 " + strconv.Itoa(tag.Count) + " 篇文章",
		Posts:      posts,
		Page:       buildPagination(page, PerPage, tag.Count, baseURL),
		ActiveTag:  tag,
	}
	s.render(w, http.StatusOK, "tag", data)
}

// handleSearch 处理关键词搜索。
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	keyword := strings.TrimSpace(r.URL.Query().Get("q"))
	if keyword == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if len([]rune(keyword)) > 64 {
		keyword = string([]rune(keyword)[:64])
	}

	posts, err := s.store.SearchPosts(keyword, 50)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	bd := s.base(r, "")
	bd.Keyword = keyword

	data := indexData{
		baseData:   bd,
		Heading:    "搜索结果",
		Subheading: "关键词「" + keyword + "」，共 " + strconv.Itoa(len(posts)) + " 篇",
		Posts:      posts,
		Page:       buildPagination(1, PerPage, len(posts), "/search?q="+keyword),
	}
	s.render(w, http.StatusOK, "search", data)
}

// handleAbout 渲染关于页面，内容来自 Markdown 文件。
func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	raw := s.readAbout()
	data := aboutData{
		baseData: s.base(r, "about"),
		Heading:  "关于",
		HTML:     template.HTML(markdown.Render(raw)), //nolint:gosec // 内容由站主本人维护
	}
	s.render(w, http.StatusOK, "about", data)
}

// readAbout 读取关于页的 Markdown 源文件，读取失败时回退到内置内容。
func (s *Server) readAbout() string {
	if s.aboutPath != "" {
		if b, err := os.ReadFile(s.aboutPath); err == nil {
			return string(b)
		} else if !os.IsNotExist(err) {
			s.logger.Warn("读取关于页内容失败", "path", s.aboutPath, "error", err)
		}
	}
	return defaultAbout
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.notFound(w, r)
}

const defaultAbout = `# 你好，欢迎来到我的博客

这里是我的个人空间，用来记录技术笔记、工程实践和一些随想。

## 关于我

一名热爱编程的开发者，平时主要写 **Go**，也折腾一点前端和基础设施。

## 关于这个站点

- 后端：Go 标准库 ` + "`net/http`" + `
- 数据库：SQLite（纯 Go 驱动，无需 CGO）
- 渲染：` + "`goldmark`" + ` 服务端渲染 Markdown
- 样式：手写 CSS，深色优先

## 联系我

如果你对文章内容有任何想法，欢迎交流。

> 慢慢来，比较快。
`
