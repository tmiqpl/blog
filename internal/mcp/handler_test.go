package mcp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tmiqpl/blog/internal/store"
)

// newHandler 构造一个待测的 /mcp handler。
func newHandler(t *testing.T, st *store.Store) http.Handler {
	t.Helper()

	srv, err := NewServer(Options{Store: st})
	if err != nil {
		t.Fatalf("构造 MCP 服务失败: %v", err)
	}
	return srv.Handler()
}

func TestHandlerTokenNotConfigured(t *testing.T) {
	h := newHandler(t, newTestStore(t))

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("未配置 Token 时应返回 404，实际 %d", rec.Code)
	}
}

func TestHandlerRejectsBadToken(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetSetting(store.SettingMCPToken, "secret-token"); err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, st)

	cases := map[string]string{
		"无 Authorization 头": "",
		"错误 Token":          "Bearer wrong",
		"缺少 Bearer 前缀":      "secret-token",
		"其他认证方案":            "Basic secret-token",
	}
	for name, header := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s：应返回 401，实际 %d", name, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s：应带 WWW-Authenticate 头", name)
		}
	}
}

func TestHandlerAcceptsValidToken(t *testing.T) {
	st := newTestStore(t)
	if err := st.SetSetting(store.SettingMCPToken, "secret-token"); err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, st)

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// 通过认证后应交给内层 MCP handler 处理（本请求不是合法 MCP 报文，
	// 因此可能返回 4xx，但绝不应是认证层的 401/404）
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusNotFound {
		t.Errorf("正确 Token 不应被认证层拦截，实际 %d", rec.Code)
	}
}
