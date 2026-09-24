// Package admin 提供博客管理后台：登录鉴权、文章管理、标签管理与站点设置。
package admin

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/captcha"
	"github.com/tmiqpl/blog/internal/setup"
	"github.com/tmiqpl/blog/internal/site"
	"github.com/tmiqpl/blog/internal/store"
)

const (
	// BasePath 是后台的挂载路径。
	BasePath = "/admin"

	loginCSRFCookie = "blog_login_csrf"
	perPage         = 15
)

// 后台登录使用的人机校验方式。
const (
	CaptchaModeOff    = ""
	CaptchaModeImage  = "image"  // 图形字符验证码
	CaptchaModeSlider = "slider" // 滑块拼图
)

// Server 是管理后台的 HTTP 服务。
type Server struct {
	store         *store.Store
	auth          *auth.Manager
	limiter       *auth.Limiter
	sliderLimiter *auth.Limiter // 滑块校验的独立限流，避免与登录失败次数互相影响
	captchaMode   string
	captcha       *captcha.Manager       // image 模式
	slider        *captcha.SliderManager // slider 模式
	templates     map[string]*template.Template
	static        http.Handler
	siteDefaults  site.Info
	state         *setup.State
	logger        *slog.Logger
	dev           bool
	mux           *http.ServeMux
}

// Options 是构造后台服务所需的依赖。
// Captcha 与 Slider 二选一：同时传入时以 Slider 为准，都为 nil 表示关闭人机校验。
type Options struct {
	Store        *store.Store
	Auth         *auth.Manager
	Captcha      *captcha.Manager       // 图形字符验证码
	Slider       *captcha.SliderManager // 滑块拼图
	Templates    fs.FS                  // 需包含 templates/admin/*.html
	Static       fs.FS                  // 需包含 static/ 目录
	SiteDefaults site.Info
	// State 是站点初始化状态；为 nil 时视为已初始化（不启用引导）。
	State  *setup.State
	Logger *slog.Logger
	Dev    bool
}

// New 构造管理后台服务。
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

	authMgr := opts.Auth
	if authMgr == nil {
		authMgr = auth.NewManager(7 * 24 * time.Hour)
	}

	// 确定人机校验方式：滑块优先
	mode := CaptchaModeOff
	if opts.Slider != nil {
		mode = CaptchaModeSlider
	} else if opts.Captcha != nil {
		mode = CaptchaModeImage
	}

	s := &Server{
		store:         opts.Store,
		auth:          authMgr,
		limiter:       auth.NewLimiter(8, 10*time.Minute),
		sliderLimiter: auth.NewLimiter(40, 10*time.Minute),
		captchaMode:   mode,
		captcha:       opts.Captcha,
		slider:        opts.Slider,
		templates:     tpls,
		static:        http.FileServer(http.FS(staticSub)),
		siteDefaults:  opts.SiteDefaults,
		state:         opts.State,
		logger:        logger,
		dev:           opts.Dev,
	}
	s.routes()
	return s, nil
}

// Handler 返回后台的根 http.Handler。
// 外面套一层门控：站点尚未初始化时，后台也引导到初始化页面，
// 否则访客会看到一个必然登录失败的后台。
func (s *Server) Handler() http.Handler { return s.gate(s.mux) }

// gate 在站点未初始化时把后台请求引导到 /init。
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.state.Done() {
			next.ServeHTTP(w, r)
			return
		}
		// 静态资源放行，避免初始化页面样式加载失败
		if strings.HasPrefix(r.URL.Path, "/admin/static/") {
			next.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, setup.InitPath, http.StatusFound)
	})
}

// GC 清理过期会话、验证码与滑块票据，供后台定时任务调用。
func (s *Server) GC() {
	s.auth.GC()
	if s.captcha != nil {
		s.captcha.GC()
	}
	if s.slider != nil {
		s.slider.GC()
	}
}

func (s *Server) routes() {
	mux := http.NewServeMux()

	// 静态资源（后台样式与脚本）
	mux.Handle("GET /admin/static/", http.StripPrefix("/admin/static/", s.static))

	// 登录 / 登出
	mux.HandleFunc("GET /admin/login", s.redirectIfAuthed(s.handleLoginPage))
	mux.HandleFunc("POST /admin/login", s.handleLoginSubmit)
	mux.HandleFunc("POST /admin/logout", s.handleLogout)

	// 登录人机校验（未登录也可访问，故不经过 requireAuth）
	mux.HandleFunc("GET /admin/captcha", s.handleCaptchaImage)
	mux.HandleFunc("GET /admin/captcha/slider", s.handleSliderChallenge)
	mux.HandleFunc("POST /admin/captcha/verify", s.handleSliderVerify)

	// 仪表盘
	mux.HandleFunc("GET /admin", s.requireAuth(s.handleDashboard))
	mux.HandleFunc("GET /admin/{$}", s.requireAuth(s.handleDashboard))

	// 文章管理
	mux.HandleFunc("GET /admin/posts", s.requireAuth(s.handlePostList))
	mux.HandleFunc("GET /admin/posts/new", s.requireAuth(s.handlePostNew))
	mux.HandleFunc("POST /admin/posts", s.requireAuth(s.handlePostCreate))
	mux.HandleFunc("GET /admin/posts/{id}/edit", s.requireAuth(s.handlePostEdit))
	mux.HandleFunc("POST /admin/posts/{id}", s.requireAuth(s.handlePostUpdate))
	mux.HandleFunc("POST /admin/posts/{id}/delete", s.requireAuth(s.handlePostDelete))
	mux.HandleFunc("POST /admin/posts/{id}/toggle", s.requireAuth(s.handlePostToggle))

	// Markdown 实时预览
	mux.HandleFunc("POST /admin/preview", s.requireAuth(s.handlePreview))

	// 标签管理
	mux.HandleFunc("GET /admin/tags", s.requireAuth(s.handleTags))
	mux.HandleFunc("POST /admin/tags/cleanup", s.requireAuth(s.handleTagCleanup))
	mux.HandleFunc("POST /admin/tags/{id}", s.requireAuth(s.handleTagUpdate))
	mux.HandleFunc("POST /admin/tags/{id}/delete", s.requireAuth(s.handleTagDelete))

	// 站点设置
	mux.HandleFunc("GET /admin/settings", s.requireAuth(s.handleSettings))
	mux.HandleFunc("POST /admin/settings", s.requireAuth(s.handleSettingsSave))

	// 账号设置
	mux.HandleFunc("GET /admin/account", s.requireAuth(s.handleAccount))
	mux.HandleFunc("POST /admin/account", s.requireAuth(s.handleAccountSave))

	// 兜底
	mux.HandleFunc("/admin/", s.handleNotFound)

	s.mux = mux
}

/* ------------------------------ 渲染 ------------------------------ */

// baseAuthed 构造已登录页面的公共数据。
func (s *Server) baseAuthed(w http.ResponseWriter, r *http.Request, sess *auth.Session, nav, title string) baseData {
	return baseData{
		Title:     title,
		Nav:       nav,
		Username:  sess.Username,
		CSRF:      sess.CSRF,
		Flash:     popFlash(w, r),
		SiteTitle: s.siteInfo().Title,
		Year:      time.Now().Year(),
		Dev:       s.dev,
	}
}

// siteInfo 读取当前站点配置。
func (s *Server) siteInfo() site.Info {
	return site.Load(s.store, s.siteDefaults)
}

// render 先渲染到缓冲，成功后再写出，避免出错时输出半截页面。
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data any) {
	tpl, ok := s.templates[page]
	if !ok {
		s.logger.Error("后台模板不存在", "page", page)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "base", data); err != nil {
		s.logger.Error("后台模板渲染失败", "page", page, "error", err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// renderError 渲染后台错误页。
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	sess, _ := s.sessionFrom(r)

	var bd baseData
	if sess != nil {
		// 已登录时保留侧边栏，便于直接跳转到其他页面
		bd = s.baseAuthed(w, r, sess, "error", heading)
	} else {
		bd = baseData{
			Title:     heading,
			SiteTitle: s.siteInfo().Title,
			Year:      time.Now().Year(),
			Dev:       s.dev,
		}
	}

	s.render(w, r, status, "error", errorPageData{baseData: bd, Code: status, Heading: heading, Message: message})
}

/* ---------------------------- 登录 CSRF ---------------------------- */

// issueLoginCSRF 为登录表单签发一次性令牌。
func issueLoginCSRF(w http.ResponseWriter, r *http.Request) string {
	token, err := auth.RandomToken(24)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     loginCSRFCookie,
		Value:    token,
		Path:     BasePath,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   900,
	})
	return token
}

// checkLoginCSRF 校验登录相关请求的令牌。
// 同时支持表单字段与 X-CSRF-Token 请求头，后者供滑块校验这类 AJAX 请求使用。
func checkLoginCSRF(r *http.Request) bool {
	c, err := r.Cookie(loginCSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := csrfFromRequest(r)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

/* ------------------------------ 工具 ------------------------------ */

// safeNext 限制登录后的跳转目标只能是站内路径，避免开放重定向。
func safeNext(raw string) string {
	if raw == "" {
		return BasePath
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return BasePath
	}
	if strings.HasPrefix(u.Path, "//") {
		return BasePath
	}
	return u.RequestURI()
}

// pathID 解析路径中的数字 ID。
func pathID(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	var id int64
	if _, err := fmt.Sscanf(raw, "%d", &id); err != nil || id <= 0 {
		return 0, errors.New("无效的 ID")
	}
	return id, nil
}

// AdminBootstrap 描述首次初始化管理员账号的结果。
type AdminBootstrap struct {
	Created  bool
	Username string
	Password string // 仅当密码为自动生成时返回明文，用于首次启动提示
}

// EnsureAdmin 确保数据库中存在管理员账号，不存在时按传入的用户名与密码创建。
// 未提供密码时会自动生成一个随机密码并随返回值返回，避免出现默认弱口令。
//
// 站点首次启动时，管理员账号由 /init 初始化页面创建（见 internal/web/init.go）；
// 这个函数供程序化引导与测试使用，不在 serve 启动路径上。
func EnsureAdmin(st *store.Store, username, password string) (AdminBootstrap, error) {
	var out AdminBootstrap

	hash, err := st.GetSetting(store.SettingAdminPassword)
	if err != nil {
		return out, err
	}
	if hash != "" {
		if u, err := st.GetSetting(store.SettingAdminUsername); err == nil {
			out.Username = u
		}
		return out, nil
	}

	if strings.TrimSpace(username) == "" {
		username = "admin"
	}
	if password == "" {
		if password, err = auth.RandomToken(9); err != nil { // 18 个十六进制字符
			return out, err
		}
	}

	encoded, err := auth.HashPassword(password)
	if err != nil {
		return out, err
	}

	if err := st.SetSettings(map[string]string{
		store.SettingAdminUsername: username,
		store.SettingAdminPassword: encoded,
	}); err != nil {
		return out, err
	}

	out.Created = true
	out.Username = username
	out.Password = password
	return out, nil
}
