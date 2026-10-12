package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/store"
)

// loginSession 用关闭验证码的后台完成一次登录，返回会话 Cookie。
func loginSession(t *testing.T, srv *Server) string {
	t.Helper()

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		loginForm(testUser, testPass, ""), baseCookies(nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("登录失败，状态码 %d", rec.Code)
	}
	session := sessionCookieOf(rec)
	if session == "" {
		t.Fatal("登录未下发会话 Cookie")
	}
	return session
}

func TestMCPPageUnconfigured(t *testing.T) {
	srv := newTestServer(t, CaptchaModeOff).server
	session := loginSession(t, srv)

	rec := doRequest(srv, http.MethodGet, "/admin/mcp", nil,
		map[string]string{auth.SessionCookieName: session})
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "尚未生成令牌") {
		t.Error("未配置时应提示尚未生成令牌")
	}
	if strings.Contains(body, `id="mcp-config"`) {
		t.Error("未配置时不应展示客户端配置示例")
	}
}

func TestMCPTokenGenerateAndRevoke(t *testing.T) {
	env := newTestServer(t, CaptchaModeOff)
	srv := env.server
	session := loginSession(t, srv)
	cookies := map[string]string{auth.SessionCookieName: session}

	// 生成令牌需要会话 CSRF 令牌
	csrf := sessionCSRF(t, srv, session)
	form := url.Values{auth.CSRFFormField: {csrf}}

	// --- 生成 ---
	rec := doRequest(srv, http.MethodPost, "/admin/mcp/token", form, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("生成令牌应返回 303，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	token, err := env.store.GetSetting(store.SettingMCPToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, mcpTokenPrefix) {
		t.Errorf("令牌应带前缀 %q，实际 %q", mcpTokenPrefix, token)
	}
	if len(token) <= len(mcpTokenPrefix) {
		t.Errorf("令牌内容为空: %q", token)
	}

	// 页面上应展示令牌与配置示例
	page := doRequest(srv, http.MethodGet, "/admin/mcp", nil, cookies)
	body := page.Body.String()
	if !strings.Contains(body, token) {
		t.Error("页面应展示当前令牌")
	}
	if !strings.Contains(body, `id="mcp-config"`) {
		t.Error("已配置时应展示客户端配置示例")
	}

	// --- 重新生成应换掉令牌 ---
	rec = doRequest(srv, http.MethodPost, "/admin/mcp/token", form, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("重新生成应返回 303，实际 %d", rec.Code)
	}
	token2, _ := env.store.GetSetting(store.SettingMCPToken)
	if token2 == token {
		t.Error("重新生成应产生不同的令牌")
	}

	// --- 撤销 ---
	rec = doRequest(srv, http.MethodPost, "/admin/mcp/token/delete", form, cookies)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("撤销应返回 303，实际 %d", rec.Code)
	}
	token3, _ := env.store.GetSetting(store.SettingMCPToken)
	if token3 != "" {
		t.Errorf("撤销后令牌应为空，实际 %q", token3)
	}
}

func TestMCPTokenRequiresCSRF(t *testing.T) {
	env := newTestServer(t, CaptchaModeOff)
	srv := env.server
	session := loginSession(t, srv)

	// 不带 CSRF 令牌的写操作应被拒绝
	rec := doRequest(srv, http.MethodPost, "/admin/mcp/token", url.Values{}, map[string]string{
		auth.SessionCookieName: session,
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("缺少 CSRF 应返回 403，实际 %d", rec.Code)
	}
}
