// Command blog 启动一个基于 Go 标准库 + SQLite 的个人博客站点。
//
// 用法：
//
//	blog serve  [-addr :18080] [-db data/blog.db] [-about content/about.md] [-compress 5KB] [-dev]
//	blog import [-db data/blog.db] [-draft] [目录]     # 从 Markdown 文件导入文章
//	blog export [-db data/blog.db] [目录]              # 把文章导出为 Markdown 文件
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/tmiqpl/blog/internal/admin"
	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/captcha"
	"github.com/tmiqpl/blog/internal/compress"
	"github.com/tmiqpl/blog/internal/content"
	"github.com/tmiqpl/blog/internal/setup"
	"github.com/tmiqpl/blog/internal/store"
	"github.com/tmiqpl/blog/internal/web"
)

// 模板与静态资源编译进二进制，单文件即可部署。
// 若运行目录下存在同名文件夹，则优先使用磁盘版本，方便随时改样式。
//
//go:embed all:templates
var embeddedTemplates embed.FS

//go:embed all:static
var embeddedStatic embed.FS

const defaultDBPath = "data/blog.db"

// version 由构建时通过 -ldflags "-X main.version=..." 注入。
// 源码直接运行时保持 dev，便于区分「自己编译的」和「正式发布的」。
var version = "dev"

// versionLine 输出一行可复制的版本信息，方便部署后核对二进制。
func versionLine() string {
	return fmt.Sprintf("blog %s (%s/%s, %s)", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

func main() {
	if err := dispatch(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		return cmdServe(nil)
	}

	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "import":
		return cmdImport(args[1:])
	case "export":
		return cmdExport(args[1:])
	case "help", "-h", "--help":
		usage()
		return nil
	case "version", "-version", "--version", "-v":
		fmt.Println(versionLine())
		return nil
	default:
		// 兼容 `blog -addr :9000` 这类直接传服务参数的用法
		return cmdServe(args)
	}
}

func usage() {
	fmt.Print(`个人博客系统

用法:
  blog serve  [-addr :18080] [-db data/blog.db] [-about content/about.md]
              [-admin-user admin] [-captcha slider|image|off] [-compress 5KB|off] [-dev]
  blog import [-db data/blog.db] [-draft] [目录]
  blog export [-db data/blog.db] [目录]
  blog version

说明:
  serve   启动 HTTP 服务（默认命令），前台在 /，管理后台在 /admin
  import  从目录中的 Markdown 文件批量导入文章，默认目录 posts
  export  把数据库中的文章导出为 Markdown 文件，默认目录 posts-export
  version 打印版本与构建目标平台
  help    查看本帮助（-h / --help 等效，各子命令也支持 -h）

  -captcha 控制后台登录的人机校验方式，默认 slider：
    slider  拖动滑块完成拼图（默认）
    image   输入图形字符验证码
    off     关闭（仅建议在内网或本地开发时使用）

  -compress 控制响应压缩，一个参数同时管开关与阈值，默认 5KB：
    5KB / 10240 / 1MB   响应体超过该阈值才压缩，单位按 1024 进制
    off                 关闭压缩（也可写 none / false / 0）
    on                  开启压缩并使用默认阈值
  阈值越大压得越少，压得太小的响应反而更大、更费 CPU。

首次启动:
  数据库为空时站点处于「未初始化」状态，访问任何页面都会引导到 /init。
  在那里填好站点信息与管理员账号后即完成初始化，/init 随即对外关闭。
  -admin-user 只用于预填初始化表单里的用户名，管理员密码在初始化页面设置。

环境变量:
  BLOG_ADDR / BLOG_DB / BLOG_ABOUT
  BLOG_ADMIN_USER / BLOG_CAPTCHA / BLOG_COMPRESS
  BLOG_TITLE / BLOG_AUTHOR / BLOG_DESCRIPTION / BLOG_BIO
  BLOG_GITHUB / BLOG_EMAIL / BLOG_ICP
`)
}

/* ------------------------------- serve ------------------------------- */

func cmdServe(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	// 让 `blog serve -h/--help` 打印项目自己的帮助文本，
	// 而不是 flag 包自动生成的那份（两者容易不一致）
	flags.Usage = usage
	addr := flags.String("addr", env("BLOG_ADDR", ":18080"), "HTTP 监听地址")
	dbPath := flags.String("db", env("BLOG_DB", defaultDBPath), "SQLite 数据库文件路径")
	aboutPath := flags.String("about", env("BLOG_ABOUT", "content/about.md"), "关于页 Markdown 文件路径")
	adminUser := flags.String("admin-user", env("BLOG_ADMIN_USER", "admin"), "初始化页面预填的管理员用户名")
	captchaMode := flags.String("captcha", env("BLOG_CAPTCHA", admin.CaptchaModeSlider),
		"后台登录人机校验方式：slider（滑块拼图）| image（字符验证码）| off（关闭）")
	compressArg := flags.String("compress", env("BLOG_COMPRESS", "5KB"),
		"响应压缩：阈值（5KB / 10240 / 1MB）或 off（关闭）")
	dev := flags.Bool("dev", false, "开发模式：禁用静态资源缓存")
	_ = flags.Parse(args)

	logger := newLogger()
	slog.SetDefault(logger)

	compressSpec, err := compress.ParseSpec(*compressArg)
	if err != nil {
		return fmt.Errorf("解析 -compress 参数失败: %w", err)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	logger.Info("数据库就绪", "path", *dbPath)

	// 站点是否已完成初始化。未完成时前台与后台都会把访客引导到 /init，
	// 因此这里不再自动写入站点设置或创建管理员账号。
	initialized, err := db.IsInitialized()
	if err != nil {
		return fmt.Errorf("读取初始化状态失败: %w", err)
	}
	state := setup.NewState(initialized)
	if !initialized {
		logger.Warn("站点尚未初始化，请访问初始化页面完成设置",
			"url", "http://localhost"+normalizeAddr(*addr)+setup.InitPath)
	}

	templates := pickFS("templates", embeddedTemplates)
	staticAssets := pickFS("static", embeddedStatic)

	// 前台站点
	siteSrv, err := web.New(web.Options{
		Store:            db,
		Templates:        templates,
		Static:           staticAssets,
		AboutPath:        *aboutPath,
		Dev:              *dev,
		Logger:           logger,
		SiteDefaults:     siteInfo(),
		State:            state,
		DefaultAdminUser: *adminUser,
	})
	if err != nil {
		return err
	}

	// 管理后台
	sessions := auth.NewManager(7 * 24 * time.Hour)

	// 人机校验：按模式创建对应的管理器，关闭时不创建
	mode := normalizeCaptchaMode(*captchaMode)
	var (
		captchaMgr *captcha.Manager
		sliderMgr  *captcha.SliderManager
	)
	switch mode {
	case admin.CaptchaModeImage:
		captchaMgr = captcha.NewManager(5*time.Minute, captcha.DefaultOptions())
	case admin.CaptchaModeSlider:
		sliderMgr = captcha.NewSliderManager(5*time.Minute, 3*time.Minute, captcha.DefaultSliderOptions())
	}

	adminSrv, err := admin.New(admin.Options{
		Store:        db,
		Auth:         sessions,
		Captcha:      captchaMgr,
		Slider:       sliderMgr,
		Templates:    templates,
		Static:       staticAssets,
		SiteDefaults: siteInfo(),
		State:        state,
		Logger:       logger,
		Dev:          *dev,
	})
	if err != nil {
		return err
	}

	// 统一入口：/admin 交给后台，其余交给前台
	root := http.NewServeMux()
	root.Handle("/admin", adminSrv.Handler())
	root.Handle("/admin/", adminSrv.Handler())
	root.Handle("/", siteSrv.Handler())

	// 压缩放在日志内层：日志里的 bytes 就是实际发出的字节数，
	// 便于直观看出压缩带来的收益。关闭压缩时这一层直接透传，没有额外开销。
	compressed := compressSpec.Middleware(root)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(logger, compressed),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 定期清理过期会话
	gcDone := make(chan struct{})
	defer close(gcDone)
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				adminSrv.GC()
			case <-gcDone:
				return
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("服务已启动",
			"url", "http://localhost"+normalizeAddr(*addr),
			"admin", "http://localhost"+normalizeAddr(*addr)+"/admin",
			"captcha", displayCaptchaMode(mode),
			"compress", compressSpec.String())
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("收到退出信号，正在关闭服务…")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("关闭服务失败: %w", err)
	}
	logger.Info("服务已关闭")
	return nil
}

/* ------------------------------ import ------------------------------ */

func cmdImport(args []string) error {
	flags := flag.NewFlagSet("import", flag.ExitOnError)
	flags.Usage = usage
	dbPath := flags.String("db", env("BLOG_DB", defaultDBPath), "SQLite 数据库文件路径")
	asDraft := flags.Bool("draft", false, "全部以草稿形式导入（站点上不显示）")
	_ = flags.Parse(args)

	dir := flags.Arg(0)
	if dir == "" {
		dir = "posts"
	}

	files, err := markdownFiles(dir)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("目录 %s 中没有找到 .md 文件", dir)
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var created, updated int
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", path, err)
		}

		fm, body, err := content.Parse(string(raw))
		if err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
		if strings.TrimSpace(body) == "" {
			fmt.Printf("  跳过 %s：正文为空\n", filepath.Base(path))
			continue
		}

		title := fm.Title
		if title == "" {
			title = fallbackTitle(body, path)
		}
		slug := fm.Slug
		if slug == "" {
			slug = store.Slugify(title)
		}

		_, isNew, err := db.UpsertPost(store.PostInput{
			Title:     title,
			Slug:      slug,
			Summary:   fm.Summary,
			Content:   body,
			Published: !fm.Draft && !*asDraft,
			CreatedAt: fm.Date,
			Tags:      fm.Tags,
		})
		if err != nil {
			return fmt.Errorf("导入 %s 失败: %w", path, err)
		}

		action := "更新"
		if isNew {
			action = "新增"
			created++
		} else {
			updated++
		}
		fmt.Printf("  %s %-28s → /post/%s\n", action, title, slug)
	}

	fmt.Printf("\n完成：新增 %d 篇，更新 %d 篇。\n", created, updated)
	return nil
}

// markdownFiles 递归收集目录下的 Markdown 文件，忽略下划线开头的文件。
func markdownFiles(dir string) ([]string, error) {
	var files []string

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(name)); ext == ".md" || ext == ".markdown" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描目录 %s 失败: %w", dir, err)
	}

	sort.Strings(files)
	return files, nil
}

// fallbackTitle 在文件缺少 title 时，取正文的第一个一级标题或文件名。
func fallbackTitle(body, path string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}

/* ------------------------------ export ------------------------------ */

func cmdExport(args []string) error {
	flags := flag.NewFlagSet("export", flag.ExitOnError)
	flags.Usage = usage
	dbPath := flags.String("db", env("BLOG_DB", defaultDBPath), "SQLite 数据库文件路径")
	_ = flags.Parse(args)

	dir := flags.Arg(0)
	if dir == "" {
		dir = "posts-export"
	}

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	posts, err := db.AllPosts()
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		fmt.Println("数据库中没有文章可导出。")
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, p := range posts {
		names := make([]string, 0, len(p.Tags))
		for _, t := range p.Tags {
			names = append(names, t.Name)
		}

		var b strings.Builder
		b.WriteString("---\n")
		fmt.Fprintf(&b, "title: %s\n", p.Title)
		fmt.Fprintf(&b, "slug: %s\n", p.Slug)
		fmt.Fprintf(&b, "date: %s\n", p.CreatedAt.Format("2006-01-02 15:04:05"))
		if len(names) > 0 {
			fmt.Fprintf(&b, "tags: %s\n", strings.Join(names, ", "))
		}
		if p.Summary != "" {
			fmt.Fprintf(&b, "summary: %s\n", p.Summary)
		}
		fmt.Fprintf(&b, "draft: %t\n", !p.Published)
		b.WriteString("---\n\n")
		b.WriteString(p.Content)
		if !strings.HasSuffix(p.Content, "\n") {
			b.WriteString("\n")
		}

		name := p.Slug
		if name == "" {
			name = fmt.Sprintf("post-%d", p.ID)
		}
		path := filepath.Join(dir, name+".md")
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return fmt.Errorf("写入 %s 失败: %w", path, err)
		}
		fmt.Printf("  导出 %s\n", path)
	}

	fmt.Printf("\n完成：共导出 %d 篇到 %s/\n", len(posts), dir)
	return nil
}

/* ------------------------------ 辅助 ------------------------------ */

// pickFS 优先使用磁盘目录，不存在时回退到编译进二进制的版本。
func pickFS(dir string, embedded embed.FS) fs.FS {
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return os.DirFS(".")
	}
	return embedded
}

func newLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// logRequests 记录每个请求的方法、路径、状态码与耗时。
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		} else if rec.status >= 400 {
			level = slog.LevelWarn
		}
		logger.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start).Round(time.Millisecond).String(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// normalizeCaptchaMode 把命令行与环境变量的取值归一化为三种模式之一。
// 兼容早期把 -captcha 当布尔开关的写法：true → slider，false → off。
func normalizeCaptchaMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "image", "text", "char", "character":
		return admin.CaptchaModeImage
	case "", "on", "true", "1", "yes", "slider", "drag":
		return admin.CaptchaModeSlider
	case "off", "none", "false", "0", "no":
		return admin.CaptchaModeOff
	default:
		return admin.CaptchaModeSlider
	}
}

// displayCaptchaMode 把模式转成日志里好读的中文描述。
func displayCaptchaMode(mode string) string {
	switch mode {
	case admin.CaptchaModeSlider:
		return "滑块拼图"
	case admin.CaptchaModeImage:
		return "字符验证码"
	default:
		return "已关闭"
	}
}

func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return ":" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}

// siteInfo 返回站点元信息，可用环境变量覆盖。
func siteInfo() web.SiteInfo {
	return web.SiteInfo{
		Title:       env("BLOG_TITLE", "我的博客"),
		Description: env("BLOG_DESCRIPTION", "记录技术、工程与思考的个人空间"),
		Author:      env("BLOG_AUTHOR", "站长"),
		Bio:         env("BLOG_BIO", "写代码，也写点别的。"),
		GitHub:      env("BLOG_GITHUB", "https://github.com/"),
		Email:       env("BLOG_EMAIL", "hello@example.com"),
		ICP:         env("BLOG_ICP", ""),
	}
}
