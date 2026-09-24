package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tmiqpl/blog/internal/setup"
	"github.com/tmiqpl/blog/internal/site"
	"github.com/tmiqpl/blog/internal/store"
)

var csrfRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// newTestServer 构造一个跑在临时数据库上的前台服务。
// initialized 控制站点是否已初始化，用来覆盖两条不同的分支。
func newTestServer(t *testing.T, initialized bool) (*Server, *store.Store) {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv, err := New(Options{
		Store:            st,
		Templates:        os.DirFS("../.."),
		Static:           os.DirFS("../.."),
		SiteDefaults:     site.Info{Title: "默认标题", Author: "默认作者"},
		DefaultAdminUser: "tester",
		State:            setup.NewState(initialized),
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("构造前台服务失败: %v", err)
	}
	return srv, st
}

// do 发一个请求到服务，cookies 为额外的 Cookie 键值对。
func do(srv *Server, method, target string, form url.Values, cookies map[string]string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// fetchInitCSRF 取一份初始化表单的令牌与对应 Cookie。
func fetchInitCSRF(t *testing.T, srv *Server) (token, cookie string) {
	t.Helper()

	rec := do(srv, http.MethodGet, setup.InitPath, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("初始化页面应返回 200，实际 %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == initCSRFCookie && c.Value != "" {
			m := csrfRe.FindStringSubmatch(rec.Body.String())
			if m == nil {
				t.Fatal("页面里没有 CSRF 隐藏字段")
			}
			return m[1], c.Value
		}
	}
	t.Fatal("未签发 CSRF Cookie")
	return "", ""
}

// validInitForm 返回一份能通过校验的表单。
func validInitForm(token string) url.Values {
	return url.Values{
		"_csrf":            {token},
		"title":            {"我的博客"},
		"description":      {"记录工程与思考"},
		"author":           {"站长"},
		"bio":              {"写代码，也写点别的。"},
		"github":           {"https://github.com/tmiqpl"},
		"email":            {"hi@example.com"},
		"icp":              {"京ICP备12345678号"},
		"username":         {"blogger"},
		"password":         {"my-secret-pass-2026"},
		"confirm_password": {"my-secret-pass-2026"},
	}
}

/* ------------------------------- 门控行为 ------------------------------- */

func TestGateRedirectsToInitWhenUninitialized(t *testing.T) {
	srv, _ := newTestServer(t, false)

	paths := []string{"/", "/tags", "/about", "/post/whatever", "/search?q=x", "/nope"}
	for _, p := range paths {
		rec := do(srv, http.MethodGet, p, nil, nil)
		if rec.Code != http.StatusFound {
			t.Errorf("%s 未初始化时应 302，实际 %d", p, rec.Code)
			continue
		}
		if loc := rec.Header().Get("Location"); loc != setup.InitPath {
			t.Errorf("%s 应跳转到 %s，实际 %s", p, setup.InitPath, loc)
		}
	}
}

func TestGateLetsStaticAndHealthThrough(t *testing.T) {
	srv, _ := newTestServer(t, false)

	for _, p := range []string{"/static/css/style.css", "/static/css/tokens.css", "/healthz"} {
		if rec := do(srv, http.MethodGet, p, nil, nil); rec.Code != http.StatusOK {
			t.Errorf("%s 未初始化时也应放行，实际 %d", p, rec.Code)
		}
	}
}

func TestInitPageAccessibleBeforeInitialization(t *testing.T) {
	srv, _ := newTestServer(t, false)

	for _, p := range []string{setup.InitPath, setup.InitHTMLPath} {
		rec := do(srv, http.MethodGet, p, nil, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s 应返回 200，实际 %d", p, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "站点初始化") {
			t.Errorf("%s 页面内容不像初始化页", p)
		}
	}
}

func TestInitLockedAfterInitialized(t *testing.T) {
	srv, _ := newTestServer(t, true)

	for _, p := range []string{setup.InitPath, setup.InitHTMLPath} {
		rec := do(srv, http.MethodGet, p, nil, nil)
		if rec.Code != http.StatusFound {
			t.Errorf("已初始化时 %s 应 302，实际 %d", p, rec.Code)
			continue
		}
		if loc := rec.Header().Get("Location"); loc != "/" {
			t.Errorf("已初始化时 %s 应跳回首页，实际 %s", p, loc)
		}
	}
}

func TestInitPostRejectedAfterInitialized(t *testing.T) {
	srv, _ := newTestServer(t, true)

	rec := do(srv, http.MethodPost, setup.InitPath, url.Values{"title": {"x"}}, nil)
	if rec.Code != http.StatusFound {
		t.Errorf("已初始化后 POST /init 应被挡下，实际 %d", rec.Code)
	}
}

func TestPagesWorkAfterInitialized(t *testing.T) {
	srv, _ := newTestServer(t, true)

	for _, p := range []string{"/", "/tags", "/about"} {
		if rec := do(srv, http.MethodGet, p, nil, nil); rec.Code != http.StatusOK {
			t.Errorf("已初始化时 %s 应返回 200，实际 %d", p, rec.Code)
		}
	}
}

/* ------------------------------- 表单校验 ------------------------------- */

func TestInitFormPrefilledFromDefaults(t *testing.T) {
	srv, _ := newTestServer(t, false)

	body := do(srv, http.MethodGet, setup.InitPath, nil, nil).Body.String()
	if !strings.Contains(body, `value="默认标题"`) {
		t.Error("站点标题应使用默认值预填")
	}
	if !strings.Contains(body, `value="tester"`) {
		t.Error("管理员用户名应使用 DefaultAdminUser 预填")
	}
	if !strings.Contains(body, "checked") {
		t.Error("「写入示例文章」应默认勾选")
	}
}

func TestInitSubmitRequiresCSRF(t *testing.T) {
	srv, st := newTestServer(t, false)

	rec := do(srv, http.MethodPost, setup.InitPath, validInitForm("伪造的令牌"), nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("CSRF 不匹配应返回 400，实际 %d", rec.Code)
	}

	if ok, _ := st.IsInitialized(); ok {
		t.Error("CSRF 校验失败不应写入初始化状态")
	}
}

func TestInitSubmitValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{"标题为空", func(f url.Values) { f.Set("title", "") }, "请填写站点标题"},
		{"用户名过长", func(f url.Values) { f.Set("username", strings.Repeat("a", 61)) }, "不能超过 60"},
		{"邮箱格式错误", func(f url.Values) { f.Set("email", "not-an-email") }, "邮箱格式不正确"},
		{"GitHub 缺协议", func(f url.Values) { f.Set("github", "github.com/x") }, "http://"},
		{"密码过短", func(f url.Values) { f.Set("password", "short"); f.Set("confirm_password", "short") }, "至少需要 8 位"},
		{"两次密码不一致", func(f url.Values) { f.Set("confirm_password", "another-pass") }, "两次输入的密码不一致"},
		{"标题过长", func(f url.Values) { f.Set("title", strings.Repeat("标", 201)) }, "过长"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, st := newTestServer(t, false)
			token, cookie := fetchInitCSRF(t, srv)

			form := validInitForm(token)
			c.mutate(form)

			rec := do(srv, http.MethodPost, setup.InitPath, form,
				map[string]string{initCSRFCookie: cookie})

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("应返回 422，实际 %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Errorf("错误提示里应包含 %q", c.want)
			}
			if ok, _ := st.IsInitialized(); ok {
				t.Error("校验失败不应写入初始化状态")
			}
		})
	}
}

// 密码是明文，任何情况下都不该被回显到 HTML 里。
func TestInitFormNeverEchoesPassword(t *testing.T) {
	srv, _ := newTestServer(t, false)
	token, cookie := fetchInitCSRF(t, srv)

	form := validInitForm(token)
	form.Set("title", "") // 故意触发校验失败，让页面重新渲染
	form.Set("password", "super-secret-value")

	rec := do(srv, http.MethodPost, setup.InitPath, form,
		map[string]string{initCSRFCookie: cookie})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("应返回 422，实际 %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "super-secret-value") {
		t.Error("密码被回显到了 HTML 里")
	}
}

func TestInitRejectsOverlongField(t *testing.T) {
	srv, _ := newTestServer(t, false)
	token, cookie := fetchInitCSRF(t, srv)

	form := validInitForm(token)
	form.Set("description", strings.Repeat("x", maxFieldLen+1))

	rec := do(srv, http.MethodPost, setup.InitPath, form,
		map[string]string{initCSRFCookie: cookie})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("超长字段应返回 422，实际 %d", rec.Code)
	}
}

/* ------------------------------- 初始化成功 ------------------------------- */

func TestInitSubmitSuccess(t *testing.T) {
	srv, st := newTestServer(t, false)
	token, cookie := fetchInitCSRF(t, srv)

	form := validInitForm(token)
	form.Set("seed_samples", "1")

	rec := do(srv, http.MethodPost, setup.InitPath, form,
		map[string]string{initCSRFCookie: cookie})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("成功应 303，实际 %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/login" {
		t.Errorf("应跳转到后台登录页，实际 %s", loc)
	}

	// 设置项落库
	settings, err := st.Settings()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		store.SettingSiteTitle:       "我的博客",
		store.SettingSiteDescription: "记录工程与思考",
		store.SettingSiteAuthor:      "站长",
		store.SettingSiteEmail:       "hi@example.com",
		store.SettingSiteICP:         "京ICP备12345678号",
		store.SettingAdminUsername:   "blogger",
		store.SettingSiteInitialized: store.InitializedValue,
	}
	for k, v := range want {
		if settings[k] != v {
			t.Errorf("设置 %s 应为 %q，实际 %q", k, v, settings[k])
		}
	}
	if settings[store.SettingAdminPassword] == "" {
		t.Error("应写入密码哈希")
	}
	if strings.Contains(settings[store.SettingAdminPassword], "my-secret-pass-2026") {
		t.Error("密码不应以明文存储")
	}

	// 示例文章按勾选写入
	if n, _ := st.CountPosts(); n == 0 {
		t.Error("勾选后应写入示例文章")
	}

	// 初始化完成后 /init 立即关闭
	after := do(srv, http.MethodGet, setup.InitPath, nil, nil)
	if after.Code != http.StatusFound {
		t.Errorf("初始化完成后 /init 应 302，实际 %d", after.Code)
	}
	// 前台恢复访问
	if home := do(srv, http.MethodGet, "/", nil, nil); home.Code != http.StatusOK {
		t.Errorf("初始化完成后首页应 200，实际 %d", home.Code)
	}
}

func TestInitSubmitWithoutSeedLeavesNoPosts(t *testing.T) {
	srv, st := newTestServer(t, false)
	token, cookie := fetchInitCSRF(t, srv)

	// 不提交 seed_samples，等同于取消勾选
	rec := do(srv, http.MethodPost, setup.InitPath, validInitForm(token),
		map[string]string{initCSRFCookie: cookie})

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("成功应 303，实际 %d", rec.Code)
	}
	if n, _ := st.CountPosts(); n != 0 {
		t.Errorf("未勾选时不应写入示例文章，实际 %d 篇", n)
	}
	if ok, _ := st.IsInitialized(); !ok {
		t.Error("仍应标记为已初始化")
	}
}

// 已有文章时不应再展示「写入示例文章」这个选项。
func TestSeedOptionHiddenWhenPostsExist(t *testing.T) {
	srv, st := newTestServer(t, false)

	if _, _, err := st.UpsertPost(store.PostInput{
		Title: "已有文章", Slug: "existing", Content: "正文", Published: true,
	}); err != nil {
		t.Fatal(err)
	}

	body := do(srv, http.MethodGet, setup.InitPath, nil, nil).Body.String()
	if strings.Contains(body, `name="seed_samples"`) {
		t.Error("已有文章时不应展示写入示例文章的选项")
	}
}
