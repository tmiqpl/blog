package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/captcha"
)

// wideTolerance 让任意横向位置都能通过，便于测试「拼图已对齐」之后的流程。
func wideTolerance() captcha.SliderOptions {
	opts := captcha.DefaultSliderOptions()
	opts.Tolerance = 999
	return opts
}

// sliderDrag 构造一段像样的拖动轨迹。
func sliderDrag(target int) captcha.Behavior {
	const steps = 16

	track := make([]int, 0, steps+1)
	for i := 0; i <= steps; i++ {
		track = append(track, target*i/steps)
	}
	return captcha.Behavior{DurationMS: 900, Track: track}
}

func postJSON(srv *Server, target string, payload any, csrf string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set(auth.CSRFHeaderName, csrf)
	}
	req.AddCookie(&http.Cookie{Name: loginCSRFCookie, Value: testCSRF})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func fetchChallenge(t *testing.T, srv *Server) sliderChallengeResponse {
	t.Helper()

	rec := doRequest(srv, http.MethodGet, "/admin/captcha/slider", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("获取滑块挑战失败，状态码 %d", rec.Code)
	}

	var out sliderChallengeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析滑块挑战失败: %v", err)
	}
	return out
}

/* ---------------------------- 页面与接口 ---------------------------- */

func TestLoginPageRendersSlider(t *testing.T) {
	srv := newTestServer(t, CaptchaModeSlider).server

	rec := doRequest(srv, http.MethodGet, "/admin/login", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`id="slider-captcha"`,
		`id="slider-stage"`,
		`id="slider-handle"`,
		`name="captcha_ticket"`,
		"/admin/captcha/slider",
		"/admin/captcha/verify",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("登录页缺少 %q", want)
		}
	}

	// 滑块模式下不应再出现字符验证码的输入框
	if strings.Contains(body, `name="captcha"`) {
		t.Error("滑块模式不应渲染字符验证码输入框")
	}
}

func TestSliderChallengeEndpoint(t *testing.T) {
	srv := newTestServer(t, CaptchaModeSlider).server

	rec := doRequest(srv, http.MethodGet, "/admin/captcha/slider", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q，应禁止缓存", cc)
	}

	var out sliderChallengeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}

	if out.Token == "" {
		t.Error("应下发挑战令牌")
	}
	// 底图用 JPEG 换体积，拼图块必须用 PNG 保留透明
	for name, c := range map[string]struct{ dataURL, want string }{
		"背景图": {out.Background, "data:image/jpeg;base64,"},
		"拼图块": {out.Piece, "data:image/png;base64,"},
	} {
		if !strings.HasPrefix(c.dataURL, c.want) {
			t.Errorf("%s 应以 %q 开头，实际前缀 %q", name, c.want, truncate(c.dataURL, 40))
		}
	}
	if out.Width <= 0 || out.Height <= 0 || out.PieceSize <= 0 {
		t.Errorf("尺寸信息不完整: %+v", out)
	}
	if out.Y < 0 || out.Y+out.PieceSize > out.Height {
		t.Errorf("拼图块纵向位置 %d 超出背景高度 %d", out.Y, out.Height)
	}
}

func TestSliderEndpointsDisabledInOtherModes(t *testing.T) {
	for _, mode := range []string{CaptchaModeImage, CaptchaModeOff} {
		srv := newTestServer(t, mode).server

		if rec := doRequest(srv, http.MethodGet, "/admin/captcha/slider", nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("模式 %q 下挑战接口应返回 404，实际 %d", mode, rec.Code)
		}
		if rec := postJSON(srv, "/admin/captcha/verify", map[string]any{}, testCSRF); rec.Code != http.StatusNotFound {
			t.Errorf("模式 %q 下校验接口应返回 404，实际 %d", mode, rec.Code)
		}
	}
}

/* ------------------------------ 校验接口 ------------------------------ */

func TestSliderVerifyRequiresCSRF(t *testing.T) {
	srv := newTestServer(t, CaptchaModeSlider).server
	ch := fetchChallenge(t, srv)

	rec := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 200, DurationMS: 900, Track: sliderDrag(200).Track,
	}, "" /* 不带 CSRF */)

	if rec.Code != http.StatusForbidden {
		t.Errorf("缺少 CSRF 应返回 403，实际 %d", rec.Code)
	}
}

func TestSliderVerifySuccessIssuesTicket(t *testing.T) {
	srv := newTestServerWithSlider(t, CaptchaModeSlider, wideTolerance()).server
	ch := fetchChallenge(t, srv)

	rec := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 200, DurationMS: 900, Track: sliderDrag(200).Track,
	}, testCSRF)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}

	var out struct {
		OK     bool   `json:"ok"`
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Ticket == "" {
		t.Fatalf("校验应通过并签发票据，实际 %+v（响应：%s）", out, rec.Body.String())
	}
}

func TestSliderVerifyWrongPosition(t *testing.T) {
	// 目标位置至少落在 35% 宽度之后，所以 x=0 必定不匹配
	srv := newTestServer(t, CaptchaModeSlider).server
	ch := fetchChallenge(t, srv)

	rec := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 0, DurationMS: 900, Track: sliderDrag(200).Track,
	}, testCSRF)

	var out struct {
		OK    bool   `json:"ok"`
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Fatal("位置不匹配不应通过")
	}
	if out.Code != "position" {
		t.Errorf("错误码 = %q，期望 position（响应：%s）", out.Code, rec.Body.String())
	}
	if out.Error == "" {
		t.Error("应返回可直接展示的错误提示")
	}
}

func TestSliderVerifySuspiciousBehavior(t *testing.T) {
	srv := newTestServerWithSlider(t, CaptchaModeSlider, wideTolerance()).server
	ch := fetchChallenge(t, srv)

	// 位置无所谓（容差很大），但轨迹是「一步跳到终点」
	rec := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 200, DurationMS: 900, Track: []int{0, 1, 2, 3, 4, 200},
	}, testCSRF)

	var out struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Fatal("可疑轨迹不应通过")
	}
	if out.Code != "behavior" {
		t.Errorf("错误码 = %q，期望 behavior", out.Code)
	}
}

func TestSliderVerifyExpiredToken(t *testing.T) {
	srv := newTestServer(t, CaptchaModeSlider).server

	rec := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: "不存在的令牌", X: 200, DurationMS: 900, Track: sliderDrag(200).Track,
	}, testCSRF)

	var out struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != "expired" {
		t.Errorf("错误码 = %q，期望 expired", out.Code)
	}
}

/* ------------------------------ 登录流程 ------------------------------ */

func sliderLoginForm(ticket string) url.Values {
	return url.Values{
		auth.CSRFFormField: {testCSRF},
		"username":         {testUser},
		"password":         {testPass},
		"captcha_ticket":   {ticket},
	}
}

func TestLoginSucceedsWithSliderTicket(t *testing.T) {
	srv := newTestServerWithSlider(t, CaptchaModeSlider, wideTolerance()).server
	ch := fetchChallenge(t, srv)

	verify := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 200, DurationMS: 900, Track: sliderDrag(200).Track,
	}, testCSRF)

	var verified struct {
		OK     bool   `json:"ok"`
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(verify.Body.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}
	if !verified.OK {
		t.Fatalf("滑块校验未通过：%s", verify.Body.String())
	}

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		sliderLoginForm(verified.Ticket), baseCookies(nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("登录应成功并重定向，实际 %d（响应：%s）", rec.Code, truncate(rec.Body.String(), 300))
	}
	if sessionCookieOf(rec) == "" {
		t.Error("登录成功应下发会话 Cookie")
	}
}

func TestLoginRejectsMissingTicket(t *testing.T) {
	srv := newTestServerWithSlider(t, CaptchaModeSlider, wideTolerance()).server

	rec := doRequest(srv, http.MethodPost, "/admin/login",
		sliderLoginForm(""), baseCookies(nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少票据应返回 401，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "滑块") {
		t.Error("应提示需要先完成滑块验证")
	}
	if sessionCookieOf(rec) != "" {
		t.Error("校验失败时不应下发会话 Cookie")
	}
}

func TestLoginTicketIsSingleUse(t *testing.T) {
	srv := newTestServerWithSlider(t, CaptchaModeSlider, wideTolerance()).server
	ch := fetchChallenge(t, srv)

	verify := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 200, DurationMS: 900, Track: sliderDrag(200).Track,
	}, testCSRF)

	var verified struct {
		OK     bool   `json:"ok"`
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(verify.Body.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}

	first := doRequest(srv, http.MethodPost, "/admin/login",
		sliderLoginForm(verified.Ticket), baseCookies(nil))
	if first.Code != http.StatusSeeOther {
		t.Fatalf("首次使用票据应成功，实际 %d", first.Code)
	}

	second := doRequest(srv, http.MethodPost, "/admin/login",
		sliderLoginForm(verified.Ticket), baseCookies(nil))
	if second.Code != http.StatusUnauthorized {
		t.Errorf("票据不应被重复使用，实际 %d", second.Code)
	}
}

func TestLoginTicketBoundToClient(t *testing.T) {
	srv := newTestServerWithSlider(t, CaptchaModeSlider, wideTolerance()).server
	ch := fetchChallenge(t, srv)

	verify := postJSON(srv, "/admin/captcha/verify", sliderVerifyRequest{
		Token: ch.Token, X: 200, DurationMS: 900, Track: sliderDrag(200).Track,
	}, testCSRF)

	var verified struct {
		OK     bool   `json:"ok"`
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(verify.Body.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}

	// 换一个来源地址提交同一张票据
	form := sliderLoginForm(verified.Ticket)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "198.51.100.7:5555"
	req.AddCookie(&http.Cookie{Name: loginCSRFCookie, Value: testCSRF})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("票据绑定来源，异地提交应被拒绝，实际 %d", rec.Code)
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
