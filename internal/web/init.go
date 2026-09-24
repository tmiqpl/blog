package web

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/setup"
	"github.com/tmiqpl/blog/internal/store"
)

const (
	// initCSRFCookie 存放初始化表单的一次性令牌。
	initCSRFCookie = "blog_init_csrf"

	// minPasswordLen 是初始化时允许的最短管理员密码长度。
	minPasswordLen = 8
	// maxFieldLen 限制单个文本字段的长度，避免写入超长内容。
	maxFieldLen = 200
	// maxUsernameLen 限制管理员用户名长度。
	maxUsernameLen = 60
)

// handleInitPage 渲染初始化表单。
func (s *Server) handleInitPage(w http.ResponseWriter, r *http.Request) {
	s.renderInit(w, r, http.StatusOK, "", s.defaultInitForm())
}

// defaultInitForm 用站点默认值与命令行指定的管理员用户名预填表单。
// 密码字段永远不回填——它是明文，不该出现在 HTML 里。
func (s *Server) defaultInitForm() initForm {
	username := strings.TrimSpace(s.defaultAdminUser)
	if username == "" {
		username = "admin"
	}
	return initForm{}.fromSite(s.siteDefaults, username)
}

// handleInitSubmit 处理初始化表单提交。
func (s *Server) handleInitSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderInit(w, r, http.StatusBadRequest, "表单解析失败，请重试", s.defaultInitForm())
		return
	}

	if !checkInitCSRF(r) {
		s.renderInit(w, r, http.StatusBadRequest, "表单已过期，请重新提交", s.defaultInitForm())
		return
	}

	form, password, err := readInitForm(r)
	if err != nil {
		s.renderInit(w, r, http.StatusUnprocessableEntity, err.Error(), form)
		return
	}

	if err := s.applyInit(form, password); err != nil {
		s.logger.Error("站点初始化失败", "error", err)
		s.renderInit(w, r, http.StatusInternalServerError, "保存失败，请查看服务日志后重试", form)
		return
	}

	// 初始化完成后立即放行后台，并把访客从 /init 引导走
	s.state.MarkDone()
	clearInitCSRFCookie(w, r)

	s.logger.Info("站点初始化完成", "title", form.Title, "admin", form.Username, "seed_samples", form.SeedSamples)

	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// applyInit 把表单内容写入数据库，并按需写入示例文章。
func (s *Server) applyInit(form initForm, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("生成密码哈希失败: %w", err)
	}

	values := map[string]string{
		store.SettingSiteTitle:       form.Title,
		store.SettingSiteDescription: form.Description,
		store.SettingSiteAuthor:      form.Author,
		store.SettingSiteBio:         form.Bio,
		store.SettingSiteGitHub:      form.GitHub,
		store.SettingSiteEmail:       form.Email,
		store.SettingSiteICP:         form.ICP,
		store.SettingAdminUsername:   form.Username,
		store.SettingAdminPassword:   hash,
	}
	if err := s.store.SetSettings(values); err != nil {
		return err
	}

	// 先落配置再标记完成：万一中间失败，站点仍停留在未初始化状态，重试即可
	if err := s.store.MarkInitialized(); err != nil {
		return err
	}

	if form.SeedSamples {
		if err := s.store.SeedSamplePosts(); err != nil {
			// 示例文章只是锦上添花，失败不该让整个初始化回滚
			s.logger.Warn("写入示例文章失败", "error", err)
		}
	}
	return nil
}

// renderInit 渲染初始化页面。
func (s *Server) renderInit(w http.ResponseWriter, r *http.Request, status int, errMsg string, form initForm) {
	data := initData{
		CSRF:       issueInitCSRF(w, r),
		InitAction: setup.InitPath,
		Error:      errMsg,
		Form:       form,
		Year:       time.Now().Year(),
		Dev:        s.dev,
		Seedable:   s.hasNoPosts(),
	}
	s.renderStandalone(w, status, "init", data)
}

// hasNoPosts 判断文章表是否为空——只有空表时「写入示例文章」才有意义。
func (s *Server) hasNoPosts() bool {
	n, err := s.store.CountPosts()
	if err != nil {
		s.logger.Error("统计文章数失败", "error", err)
		return true
	}
	return n == 0
}

/* ------------------------------ 表单解析与校验 ------------------------------ */

// readInitForm 解析并校验表单，返回填充好的字段值与明文密码。
// 校验失败时返回的 form 已带上用户输入，便于原样回显。
func readInitForm(r *http.Request) (initForm, string, error) {
	form := initForm{
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		Author:      strings.TrimSpace(r.PostFormValue("author")),
		Bio:         strings.TrimSpace(r.PostFormValue("bio")),
		GitHub:      strings.TrimSpace(r.PostFormValue("github")),
		Email:       strings.TrimSpace(r.PostFormValue("email")),
		ICP:         strings.TrimSpace(r.PostFormValue("icp")),
		Username:    strings.TrimSpace(r.PostFormValue("username")),
		SeedSamples: r.PostFormValue("seed_samples") != "",
	}
	password := r.PostFormValue("password")

	fields := []struct{ label, value string }{
		{"站点标题", form.Title},
		{"站点描述", form.Description},
		{"作者", form.Author},
		{"作者简介", form.Bio},
		{"GitHub 地址", form.GitHub},
		{"邮箱", form.Email},
		{"备案号", form.ICP},
	}
	for _, f := range fields {
		if utf8.RuneCountInString(f.value) > maxFieldLen {
			return form, password, fmt.Errorf("%s过长，请控制在 %d 字以内", f.label, maxFieldLen)
		}
	}

	switch {
	case form.Title == "":
		return form, password, errors.New("请填写站点标题")
	case form.Username == "":
		return form, password, errors.New("请填写管理员用户名")
	case utf8.RuneCountInString(form.Username) > maxUsernameLen:
		return form, password, fmt.Errorf("管理员用户名不能超过 %d 个字符", maxUsernameLen)
	}

	if form.Email != "" {
		if _, err := mail.ParseAddress(form.Email); err != nil {
			return form, password, errors.New("邮箱格式不正确")
		}
	}
	if form.GitHub != "" && !hasHTTPPrefix(form.GitHub) {
		return form, password, errors.New("GitHub 地址需要以 http:// 或 https:// 开头")
	}

	if utf8.RuneCountInString(password) < minPasswordLen {
		return form, password, fmt.Errorf("密码至少需要 %d 位", minPasswordLen)
	}
	if password != r.PostFormValue("confirm_password") {
		return form, password, errors.New("两次输入的密码不一致")
	}
	return form, password, nil
}

func hasHTTPPrefix(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

/* --------------------------------- CSRF --------------------------------- */

// issueInitCSRF 签发（或复用）初始化表单令牌。
func issueInitCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(initCSRFCookie); err == nil && c.Value != "" {
		return c.Value
	}

	token, err := auth.RandomToken(32)
	if err != nil {
		// 极端情况下退回随机字符串会削弱防护，直接让表单无法提交更安全
		return ""
	}

	http.SetCookie(w, &http.Cookie{
		Name:     initCSRFCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   3600,
	})
	return token
}

// checkInitCSRF 校验提交上来的令牌与 Cookie 是否一致。
func checkInitCSRF(r *http.Request) bool {
	c, err := r.Cookie(initCSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.PostFormValue(auth.CSRFFormField)
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

func clearInitCSRFCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     initCSRFCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// isSecureRequest 判断请求是否经由 HTTPS 到达。
// 反向代理下 TLS 在 Nginx 侧终结，因此还要看 X-Forwarded-Proto。
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
