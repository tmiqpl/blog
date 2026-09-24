package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/setup"
	"github.com/tmiqpl/blog/internal/site"
	"github.com/tmiqpl/blog/internal/store"
)

// PerPage 是列表页每页显示的文章数量。
const PerPage = 6

// Server 承载路由、模板与依赖。
type Server struct {
	store            *store.Store
	templates        map[string]*template.Template
	static           http.Handler
	siteDefaults     site.Info
	defaultAdminUser string
	aboutPath        string
	state            *setup.State
	logger           *slog.Logger
	dev              bool
	mux              *http.ServeMux
}

// Options 是构造 Server 所需的依赖与配置。
type Options struct {
	Store        *store.Store
	Templates    fs.FS // 需包含 templates/*.html
	Static       fs.FS // 需包含 static/ 目录
	SiteDefaults site.Info
	// DefaultAdminUser 用于预填初始化表单里的管理员用户名。
	DefaultAdminUser string
	AboutPath        string
	Dev              bool
	// State 是站点初始化状态；为 nil 时视为已初始化（不启用引导）。
	State  *setup.State
	Logger *slog.Logger
}

// New 构造一个可用的 Server。
func New(opts Options) (*Server, error) {
	tpls, err := loadTemplates(opts.Templates)
	if err != nil {
		return nil, err
	}

	staticSub, err := fs.Sub(opts.Static, "static")
	if err != nil {
		return nil, fmt.Errorf("定位静态资源目录失败: %w", err)
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		store:            opts.Store,
		templates:        tpls,
		static:           http.FileServer(http.FS(staticSub)),
		siteDefaults:     opts.SiteDefaults,
		defaultAdminUser: opts.DefaultAdminUser,
		aboutPath:        opts.AboutPath,
		state:            opts.State,
		logger:           logger,
		dev:              opts.Dev,
	}
	s.routes()
	return s, nil
}

// siteInfo 读取当前站点配置（后台修改后立即生效）。
func (s *Server) siteInfo() site.Info {
	return site.Load(s.store, s.siteDefaults)
}

// Handler 返回站点的根 http.Handler。
// 外面套一层门控：站点尚未初始化时，除初始化页面本身外一律引导到 /init。
func (s *Server) Handler() http.Handler { return s.gate(s.mux) }

// gate 是站点初始化门控。
//
//   - 未初始化：除 /init、静态资源、/healthz 外，所有请求 302 到 /init
//   - 已初始化：/init 不再对外开放，直接 302 回首页
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path == setup.InitPath || path == setup.InitHTMLPath {
			if s.state.Done() {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// 静态资源与健康检查不受初始化状态影响，否则初始化页面自己都加载不出样式
		if path == "/healthz" || path == "/favicon.ico" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}

		if !s.state.Done() {
			http.Redirect(w, r, setup.InitPath, http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	mux := http.NewServeMux()

	// 静态资源
	mux.Handle("GET /static/", http.StripPrefix("/static/", s.cacheControl(s.static)))

	// 浏览器默认会请求 /favicon.ico，统一指向 SVG 图标
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})

	// 站点初始化（两个地址指向同一套处理器）
	mux.HandleFunc("GET "+setup.InitPath, s.handleInitPage)
	mux.HandleFunc("GET "+setup.InitHTMLPath, s.handleInitPage)
	mux.HandleFunc("POST "+setup.InitPath, s.handleInitSubmit)
	mux.HandleFunc("POST "+setup.InitHTMLPath, s.handleInitSubmit)

	// 页面路由
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /post/{slug}", s.handlePost)
	mux.HandleFunc("GET /tags", s.handleTags)
	mux.HandleFunc("GET /tag/{slug}", s.handleTag)
	mux.HandleFunc("GET /about", s.handleAbout)
	mux.HandleFunc("GET /search", s.handleSearch)

	// 运维与兜底
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("/", s.handleNotFound)

	s.mux = mux
}

// base 构造所有页面共享的数据。nav 用于高亮当前导航项。
func (s *Server) base(r *http.Request, nav string) baseData {
	tags, err := s.store.ListTags()
	if err != nil {
		s.logger.Error("加载标签失败", "error", err)
	}
	return baseData{
		Site:        s.siteInfo(),
		Nav:         nav,
		Tags:        tags,
		Year:        time.Now().Year(),
		IsDev:       s.dev,
		RequestPath: r.URL.Path,
	}
}

// render 先渲染到内存缓冲，成功后再写入响应，避免出错时输出半截页面。
func (s *Server) render(w http.ResponseWriter, status int, page string, data any) {
	tpl, ok := s.templates[page]
	if !ok {
		s.logger.Error("模板不存在", "page", page)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "base", data); err != nil {
		s.logger.Error("模板渲染失败", "page", page, "error", err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderStandalone 渲染不套用 base 布局的独立页面（页面模板自带完整 HTML 结构）。
func (s *Server) renderStandalone(w http.ResponseWriter, status int, page string, data any) {
	tpl, ok := s.templates[page]
	if !ok {
		s.logger.Error("模板不存在", "page", page)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, page, data); err != nil {
		s.logger.Error("模板渲染失败", "page", page, "error", err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// serverError 统一处理 500。
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("请求处理失败", "path", r.URL.Path, "error", err)
	data := errorData{
		baseData: s.base(r, ""),
		Code:     http.StatusInternalServerError,
		Heading:  "服务器出了点问题",
		Message:  "请稍后重试，或返回首页继续浏览。",
	}
	s.render(w, http.StatusInternalServerError, "error", data)
}

// notFound 统一处理 404。
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	data := errorData{
		baseData: s.base(r, ""),
		Code:     http.StatusNotFound,
		Heading:  "页面走丢了",
		Message:  "你访问的页面不存在，也可能已经被移除。",
	}
	s.render(w, http.StatusNotFound, "error", data)
}

// pageParam 解析 ?page= 参数，非法值一律回退到第 1 页。
func pageParam(r *http.Request) int {
	raw := r.URL.Query().Get("page")
	if raw == "" {
		return 1
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 1
	}
	if n > 10000 {
		return 10000
	}
	return n
}

// cacheControl 为静态资源加上基础缓存头；开发模式下禁用缓存，便于实时预览改动。
func (s *Server) cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.dev {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		next.ServeHTTP(w, r)
	})
}
