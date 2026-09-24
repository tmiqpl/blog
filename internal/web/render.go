// Package web 提供博客的 HTTP 路由、处理器与模板渲染。
package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/models"
	"github.com/tmiqpl/blog/internal/site"
)

// SiteInfo 是站点元信息类型，后台修改后会即时生效。
type SiteInfo = site.Info

// baseData 是所有页面共享的数据。
type baseData struct {
	Site        SiteInfo
	Nav         string
	Tags        []models.Tag
	Keyword     string
	Year        int
	IsDev       bool
	RequestPath string
}

// indexData 用于文章列表页（首页 / 标签页 / 搜索页）。
type indexData struct {
	baseData
	Heading    string
	Subheading string
	Posts      []*models.Post
	Page       pagination
	ActiveTag  *models.Tag
}

// postData 用于文章详情页。
type postData struct {
	baseData
	Post    *models.Post
	Content template.HTML
	Related []*models.Post
	TOC     []tocItem
}

// tocItem 是文章目录中的一项。
type tocItem struct {
	ID    string
	Text  string
	Level int // 2 = 二级标题，3 = 三级标题
}

// tagsData 用于标签总览页。
type tagsData struct {
	baseData
	Heading string
}

// aboutData 用于关于页。
type aboutData struct {
	baseData
	Heading string
	HTML    template.HTML
}

// initData 用于站点初始化页面。该页面不套用 base 布局，
// 因为此时站点信息还没配置，页头页脚没有内容可渲染。
type initData struct {
	CSRF       string
	InitAction string
	Error      string
	Form       initForm
	Year       int
	Dev        bool
	// Seedable 表示文章表为空、勾选「写入示例文章」才有意义。
	Seedable bool
}

// initForm 是初始化表单的字段值，既用于进入页面时的预填，也用于校验失败后的回显。
// 两个密码字段不在此列——出于安全考虑，密码永远不回显。
type initForm struct {
	Title       string
	Description string
	Author      string
	Bio         string
	GitHub      string
	Email       string
	ICP         string
	Username    string
	SeedSamples bool
}

// fromSite 用站点默认值预填表单。
func (f initForm) fromSite(info site.Info, username string) initForm {
	f.Title = info.Title
	f.Description = info.Description
	f.Author = info.Author
	f.Bio = info.Bio
	f.GitHub = info.GitHub
	f.Email = info.Email
	f.ICP = info.ICP
	f.Username = username
	f.SeedSamples = true
	return f
}

// pagination 描述分页状态。
type pagination struct {
	Page       int
	PerPage    int
	Total      int
	TotalPages int
	HasPrev    bool
	HasNext    bool
	PrevURL    string
	NextURL    string
	Pages      []pageLink
}

type pageLink struct {
	Number  int
	URL     string
	Current bool
}

// buildPagination 根据总数与当前页构造分页信息，baseURL 为不含页码的地址。
func buildPagination(page, perPage, total int, baseURL string) pagination {
	if page < 1 {
		page = 1
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	p := pagination{
		Page:       page,
		PerPage:    perPage,
		Total:      total,
		TotalPages: totalPages,
		HasPrev:    page > 1,
		HasNext:    page < totalPages,
	}
	p.PrevURL = pageURL(baseURL, page-1)
	p.NextURL = pageURL(baseURL, page+1)

	// 生成页码窗口：当前页前后各 2 页。
	start := page - 2
	if start < 1 {
		start = 1
	}
	end := start + 4
	if end > totalPages {
		end = totalPages
		start = end - 4
		if start < 1 {
			start = 1
		}
	}
	for i := start; i <= end; i++ {
		p.Pages = append(p.Pages, pageLink{
			Number:  i,
			URL:     pageURL(baseURL, i),
			Current: i == page,
		})
	}
	return p
}

// pageURL 拼装带 page 查询参数的地址，第一页直接返回 baseURL。
func pageURL(baseURL string, page int) string {
	if page <= 1 {
		return baseURL
	}
	sep := "?"
	if strings.Contains(baseURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%spage=%d", baseURL, sep, page)
}

// templateFuncs 是暴露给模板的辅助函数集合。
var templateFuncs = template.FuncMap{
	"formatDate": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006年01月02日")
	},
	"formatDateShort": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02")
	},
	"year":     func(t time.Time) int { return t.Year() },
	"urlquery": func(s string) string { return url.QueryEscape(s) },
	"joinTags": func(tags []models.Tag) string {
		names := make([]string, 0, len(tags))
		for _, t := range tags {
			names = append(names, t.Name)
		}
		return strings.Join(names, "、")
	},
	"add":   func(a, b int) int { return a + b },
	"sub":   func(a, b int) int { return a - b },
	"lower": strings.ToLower,
	"truncate": func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	},
}

// pageNames 列出所有需要渲染的页面模板。
var pageNames = []string{"index", "post", "tags", "tag", "about", "search", "error"}

// standalonePages 是不套用 base.html 的独立页面，自带完整 HTML 结构。
var standalonePages = []string{"init"}

// loadTemplates 为每个页面构建独立的模板集合（base + partials + page）。
func loadTemplates(fsys fs.FS) (map[string]*template.Template, error) {
	shared := []string{"templates/base.html", "templates/partials.html"}

	base := template.New("base").Funcs(templateFuncs)
	if _, err := base.ParseFS(fsys, shared...); err != nil {
		return nil, fmt.Errorf("解析基础模板失败: %w", err)
	}

	out := make(map[string]*template.Template, len(pageNames)+len(standalonePages))
	for _, name := range pageNames {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("克隆模板集合失败: %w", err)
		}
		if _, err := clone.ParseFS(fsys, "templates/"+name+".html"); err != nil {
			return nil, fmt.Errorf("解析模板 %s.html 失败: %w", name, err)
		}
		out[name] = clone
	}

	for _, name := range standalonePages {
		tpl := template.New(name).Funcs(templateFuncs)
		if _, err := tpl.ParseFS(fsys, "templates/"+name+".html"); err != nil {
			return nil, fmt.Errorf("解析模板 %s.html 失败: %w", name, err)
		}
		out[name] = tpl
	}
	return out, nil
}
