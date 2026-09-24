package admin

import (
	"fmt"
	"html/template"
	"io/fs"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/models"
	"github.com/tmiqpl/blog/internal/site"
	"github.com/tmiqpl/blog/internal/store"
)

// 后台导航项标识。
const (
	navDashboard = "dashboard"
	navPosts     = "posts"
	navTags      = "tags"
	navSettings  = "settings"
	navAccount   = "account"
)

// flashMessage 是一次性的提示消息。
type flashMessage struct {
	Kind    string // success | error | info
	Message string
}

// baseData 是后台所有页面共享的数据。
type baseData struct {
	Title     string
	Nav       string
	Username  string
	CSRF      string
	Flash     *flashMessage
	SiteTitle string
	Year      int
	Dev       bool
}

// loginData 用于登录页。
type loginData struct {
	baseData
	Error       string
	Next        string
	CaptchaMode string
	CaptchaURL  string        // image 模式下的验证码图片地址
	Slider      *sliderConfig // slider 模式下的尺寸配置
}

// sliderConfig 供模板预设元素尺寸，避免图片加载完成时布局跳动。
type sliderConfig struct {
	Width     int
	Height    int
	PieceSize int
}

// dashboardData 用于仪表盘。
type dashboardData struct {
	baseData
	Stats        store.AdminStats
	RecentPosts  []*models.Post
	DraftPosts   []*models.Post
	TagCount     int
	PublishedPct int
}

// postsData 用于文章列表页。
type postsData struct {
	baseData
	Posts   []*models.Post
	Page    pagination
	Filter  store.PostFilter
	Total   int
	HasPrev bool
	HasNext bool
	PrevURL string
	NextURL string
}

// editorData 用于新建/编辑文章页。
type editorData struct {
	baseData
	Form      postForm
	IsNew     bool
	Errors    []string
	AllTags   []models.Tag
	PostID    int64
	UpdatedAt string
}

// postForm 是文章编辑表单的数据载体。
type postForm struct {
	Title     string
	Slug      string
	Summary   string
	Tags      string
	Content   string
	Published bool
	CreatedAt string
}

// tagsData 用于标签管理页。
type tagsData struct {
	baseData
	Tags []store.TagUsage
}

// settingsData 用于站点设置页。
type settingsData struct {
	baseData
	Form   site.Info
	Errors []string
}

// accountData 用于账号设置页。
type accountData struct {
	baseData
	Error    string
	Success  string
	Username string
}

// errorPageData 用于后台错误页。
type errorPageData struct {
	baseData
	Code    int
	Heading string
	Message string
}

// pagination 是后台列表的简化分页信息。
type pagination struct {
	Page       int
	TotalPages int
	Pages      []int
}

// newPagination 构造页码窗口。
func newPagination(page, perPage, total int) pagination {
	totalPages := 0
	if total > 0 {
		totalPages = (total + perPage - 1) / perPage
	}
	if page < 1 {
		page = 1
	}
	if totalPages > 0 && page > totalPages {
		page = totalPages
	}

	p := pagination{Page: page, TotalPages: totalPages}

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
		p.Pages = append(p.Pages, i)
	}
	return p
}

// templateFuncs 是后台模板可用的辅助函数。
var templateFuncs = template.FuncMap{
	"formatDate": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("2006-01-02")
	},
	"formatDateTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("2006-01-02 15:04")
	},
	"formatDateTimeLocal": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02T15:04")
	},
	"relativeTime": relativeTime,
	"joinTags": func(tags []models.Tag) string {
		names := make([]string, 0, len(tags))
		for _, t := range tags {
			names = append(names, t.Name)
		}
		return strings.Join(names, ", ")
	},
	"truncate": func(s string, n int) string {
		r := []rune(s)
		if len(r) <= n {
			return s
		}
		return string(r[:n]) + "…"
	},
	"wordCount": func(s string) int { return len([]rune(s)) },
	"add":       func(a, b int) int { return a + b },
	"sub":       func(a, b int) int { return a - b },
	"postListURL": func(status, keyword string, page int) string {
		return postListURL(store.PostFilter{Status: status, Keyword: keyword}, page)
	},
	"seq": func(from, to int) []int {
		if to < from {
			return nil
		}
		out := make([]int, 0, to-from+1)
		for i := from; i <= to; i++ {
			out = append(out, i)
		}
		return out
	},
}

// relativeTime 把时间转成「3 分钟前」这类相对描述。
func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}

	d := time.Since(t)
	switch {
	case d < 0:
		return t.Format("2006-01-02 15:04")
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// pageNames 列出所有后台页面模板。
var pageNames = []string{"login", "dashboard", "posts", "editor", "tags", "settings", "account", "error"}

// loadTemplates 为每个后台页面构建独立的模板集合。
func loadTemplates(fsys fs.FS) (map[string]*template.Template, error) {
	shared := []string{"templates/admin/base.html", "templates/admin/partials.html"}

	base := template.New("base").Funcs(templateFuncs)
	if _, err := base.ParseFS(fsys, shared...); err != nil {
		return nil, fmt.Errorf("解析后台基础模板失败: %w", err)
	}

	out := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("克隆后台模板集合失败: %w", err)
		}
		if _, err := clone.ParseFS(fsys, "templates/admin/"+name+".html"); err != nil {
			return nil, fmt.Errorf("解析后台模板 %s.html 失败: %w", name, err)
		}
		out[name] = clone
	}
	return out, nil
}
