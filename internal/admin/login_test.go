package admin

import (
	"bytes"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/captcha"
	"github.com/tmiqpl/blog/internal/store"
)

const (
	testUser = "admin"
	testPass = "correct-horse-battery"
	testCSRF = "test-csrf-token"
)

// testEnv 把测试用到的服务与其内部的人机校验管理器打包返回。
type testEnv struct {
	server  *Server
	captcha *captcha.Manager
	slider  *captcha.SliderManager
}

// newTestServer 构造一个跑在临时数据库上的后台服务。
// 模板与静态资源直接读仓库目录，避免测试依赖 go:embed。
func newTestServer(t *testing.T, mode string) *testEnv {
	t.Helper()
	return newTestServerWithSlider(t, mode, captcha.DefaultSliderOptions())
}

func newTestServerWithSlider(t *testing.T, mode string, sliderOpts captcha.SliderOptions) *testEnv {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := EnsureAdmin(st, testUser, testPass); err != nil {
		t.Fatalf("初始化管理员失败: %v", err)
	}

	env := &testEnv{}
	switch mode {
	case CaptchaModeImage:
		env.captcha = captcha.NewManager(5*time.Minute, captcha.DefaultOptions())
	case CaptchaModeSlider:
		env.slider = captcha.NewSliderManager(5*time.Minute, 3*time.Minute, sliderOpts)
	}

	srv, err := New(Options{
		Store:     st,
		Auth:      auth.NewManager(time.Hour),
		Captcha:   env.captcha,
		Slider:    env.slider,
		Templates: os.DirFS("../.."),
		Static:    os.DirFS("../.."),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("构造后台服务失败: %v", err)
	}
	env.server = srv
	return env
}

func doRequest(srv *Server, method, target string, form url.Values, cookies map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for name, value := range cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// loginForm 组装一份带 CSRF 与验证码的登录表单。
func loginForm(username, password, captchaCode string) url.Values {
	form := url.Values{
		auth.CSRFFormField: {testCSRF},
		"username":         {username},
		"password":         {password},
	}
	if captchaCode != "" {
		form.Set("captcha", captchaCode)
	}
	return form
}

func baseCookies(extra map[string]string) map[string]string {
	cookies := map[string]string{loginCSRFCookie: testCSRF}
	for k, v := range extra {
		cookies[k] = v
	}
	return cookies
}

func sessionCookieOf(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// sessionCSRF 从已登录的后台页面里取出当前会话的 CSRF 令牌。
// 登录表单用的是独立的登录令牌，两者不同，写操作必须用会话令牌。
func sessionCSRF(t *testing.T, srv *Server, sessionID string) string {
	t.Helper()

	rec := doRequest(srv, http.MethodGet, "/admin", nil,
		map[string]string{auth.SessionCookieName: sessionID})
	if rec.Code != http.StatusOK {
		t.Fatalf("获取后台首页失败，状态码 %d", rec.Code)
	}

	body := rec.Body.String()
	idx := strings.Index(body, `action="/admin/logout"`)
	if idx < 0 {
		t.Fatal("后台首页中找不到登出表单")
	}

	rest := body[idx:]
	start := strings.Index(rest, `value="`)
	if start < 0 {
		t.Fatal("登出表单缺少 CSRF 令牌")
	}
	rest = rest[start+len(`value="`):]

	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("CSRF 令牌格式异常")
	}
	return rest[:end]
}

func TestLoginPageRendersCaptcha(t *testing.T) {
	srv := newTestServer(t, CaptchaModeImage).server

	rec := doRequest(srv, http.MethodGet, "/admin/login", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{`name="captcha"`, `id="captcha-img"`, "/admin/captcha?t="} {
		if !strings.Contains(body, want) {
			t.Errorf("登录页缺少 %q", want)
		}
	}
}

func TestLoginPageHidesCaptchaWhenDisabled(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	rec := doRequest(srv, http.MethodGet, "/admin/login", nil, nil)
	if body := rec.Body.String(); strings.Contains(body, `name="captcha"`) {
		t.Error("关闭验证码后登录页不应渲染验证码输入框")
	}
}

func TestCaptchaEndpointReturnsPNG(t *testing.T) {
	srv := newTestServer(t, CaptchaModeImage).server

	rec := doRequest(srv, http.MethodGet, "/admin/captcha", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q，期望 image/png", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q，应禁止缓存", cc)
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Fatalf("响应不是合法的 PNG: %v", err)
	}

	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == captchaCookieName && c.Value != "" {
			found = true
			if !c.HttpOnly {
				t.Error("验证码 Cookie 应设置 HttpOnly")
			}
		}
	}
	if !found {
		t.Error("响应应下发验证码 Cookie")
	}
}

func TestCaptchaEndpointDisabled(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	rec := doRequest(srv, http.MethodGet, "/admin/captcha", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("关闭验证码时该接口应返回 404，实际 %d", rec.Code)
	}
}

func TestLoginRequiresCaptcha(t *testing.T) {
	srv := newTestServer(t, CaptchaModeImage).server

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, ""), baseCookies(nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少验证码应返回 401，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "验证码不正确") {
		t.Error("应提示验证码不正确")
	}
	if sessionCookieOf(rec) != "" {
		t.Error("验证码失败时不应下发会话 Cookie")
	}
}

func TestLoginRejectsWrongCaptcha(t *testing.T) {
	env := newTestServer(t, CaptchaModeImage)
	srv, mgr := env.server, env.captcha

	challenge, err := mgr.Generate()
	if err != nil {
		t.Fatal(err)
	}

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, "ZZZZ"), // 字符集里没有 Z，必定错误
		baseCookies(map[string]string{captchaCookieName: challenge.ID}))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("验证码错误应返回 401，实际 %d", rec.Code)
	}
	if sessionCookieOf(rec) != "" {
		t.Error("验证码错误时不应下发会话 Cookie")
	}
}

func TestLoginCaptchaIsSingleUse(t *testing.T) {
	env := newTestServer(t, CaptchaModeImage)
	srv, mgr := env.server, env.captcha

	challenge, err := mgr.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cookies := baseCookies(map[string]string{captchaCookieName: challenge.ID})

	// 第一次用错误密码，验证码会被消费掉
	first := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, "wrong-password", challenge.Code), cookies)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("密码错误应返回 401，实际 %d", first.Code)
	}

	// 第二次即便验证码填对，也应因已作废而失败
	second := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, challenge.Code), cookies)
	if second.Code != http.StatusUnauthorized {
		t.Errorf("已消费的验证码不应再次通过，实际 %d", second.Code)
	}
}

func TestLoginSucceedsWithCaptcha(t *testing.T) {
	env := newTestServer(t, CaptchaModeImage)
	srv, mgr := env.server, env.captcha

	challenge, err := mgr.Generate()
	if err != nil {
		t.Fatal(err)
	}

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, challenge.Code),
		baseCookies(map[string]string{captchaCookieName: challenge.ID}))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("登录成功应返回 303，实际 %d（响应：%s）", rec.Code, rec.Body.String())
	}

	sessionID := sessionCookieOf(rec)
	if sessionID == "" {
		t.Fatal("登录成功应下发会话 Cookie")
	}

	// 带上会话 Cookie 应能访问后台首页
	dashboard := doRequest(srv, http.MethodGet, "/admin", nil,
		map[string]string{auth.SessionCookieName: sessionID})
	if dashboard.Code != http.StatusOK {
		t.Errorf("登录后访问后台首页应返回 200，实际 %d", dashboard.Code)
	}
}

func TestLoginCaptchaIsCaseInsensitive(t *testing.T) {
	env := newTestServer(t, CaptchaModeImage)
	srv, mgr := env.server, env.captcha

	challenge, err := mgr.Generate()
	if err != nil {
		t.Fatal(err)
	}

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, strings.ToLower(challenge.Code)),
		baseCookies(map[string]string{captchaCookieName: challenge.ID}))

	if rec.Code != http.StatusSeeOther {
		t.Errorf("验证码应不区分大小写，实际状态码 %d", rec.Code)
	}
}

func TestLoginWithoutCaptchaWhenDisabled(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, ""), baseCookies(nil))

	if rec.Code != http.StatusSeeOther {
		t.Errorf("关闭验证码时应能直接登录，实际 %d", rec.Code)
	}
	if sessionCookieOf(rec) == "" {
		t.Error("登录成功应下发会话 Cookie")
	}
}

func TestLoginRejectsBadCSRF(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	form := loginForm(testUser, testPass, "")
	form.Set(auth.CSRFFormField, "tampered")

	rec := doRequest(srv, http.MethodPost, "/admin/login", form, baseCookies(nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("CSRF 不匹配应返回 400，实际 %d", rec.Code)
	}
}

func TestLoginRateLimitCountsCaptchaFailures(t *testing.T) {
	env := newTestServer(t, CaptchaModeImage)
	srv, mgr := env.server, env.captcha

	// 限流阈值是 8 次；连续失败 8 次后应被拦截
	for i := 0; i < 8; i++ {
		challenge, err := mgr.Generate()
		if err != nil {
			t.Fatal(err)
		}
		doRequest(srv, http.MethodPost, "/admin/login",
			loginForm(testUser, testPass, "ZZZZ"),
			baseCookies(map[string]string{captchaCookieName: challenge.ID}))
	}

	challenge, err := mgr.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, challenge.Code),
		baseCookies(map[string]string{captchaCookieName: challenge.ID}))

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("超过失败次数上限后应返回 429，实际 %d", rec.Code)
	}
}

func TestAdminRequiresLogin(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	rec := doRequest(srv, http.MethodGet, "/admin/posts", nil, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("未登录访问后台应重定向，实际 %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/login") {
		t.Errorf("应重定向到登录页，实际 %q", loc)
	}
}

func TestLogoutDestroysSession(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	login := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, ""), baseCookies(nil))
	sessionID := sessionCookieOf(login)
	if sessionID == "" {
		t.Fatal("登录失败，无法继续测试登出")
	}

	token := sessionCSRF(t, srv, sessionID)

	logout := doRequest(srv, http.MethodPost, "/admin/logout",
		url.Values{auth.CSRFFormField: {token}},
		map[string]string{auth.SessionCookieName: sessionID})
	if logout.Code != http.StatusSeeOther {
		t.Fatalf("登出应重定向，实际 %d", logout.Code)
	}

	after := doRequest(srv, http.MethodGet, "/admin", nil,
		map[string]string{auth.SessionCookieName: sessionID})
	if after.Code != http.StatusSeeOther {
		t.Errorf("登出后会话应失效，实际 %d", after.Code)
	}
}

func TestLogoutRejectsWrongCSRF(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server

	login := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, ""), baseCookies(nil))
	sessionID := sessionCookieOf(login)
	if sessionID == "" {
		t.Fatal("登录失败")
	}

	rec := doRequest(srv, http.MethodPost, "/admin/logout",
		url.Values{auth.CSRFFormField: {"forged-token"}},
		map[string]string{auth.SessionCookieName: sessionID})

	if rec.Code != http.StatusForbidden {
		t.Errorf("CSRF 不匹配的登出应返回 403，实际 %d", rec.Code)
	}

	// 会话应仍然有效
	after := doRequest(srv, http.MethodGet, "/admin", nil,
		map[string]string{auth.SessionCookieName: sessionID})
	if after.Code != http.StatusOK {
		t.Errorf("登出失败后会话应保留，实际 %d", after.Code)
	}
}
