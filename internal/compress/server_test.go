package compress

import (
	"bytes"
	"compress/gzip"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

// 这一组测试跑在真实的 httptest.Server 上。
// httptest.ResponseRecorder 看不到传输层行为（chunked、Content-Length 的移除、
// HEAD 的实体丢弃），只有走完整的 net/http 栈才能验证。

func newServer(t *testing.T, spec Spec, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(spec.Middleware(handler))
	t.Cleanup(srv.Close)
	return srv
}

// get 发一个请求并读完响应体，返回响应与原始字节。
func get(t *testing.T, method, url, accept string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return resp, raw
}

// randomText 生成一段低压缩率（高熵）文本，确保压缩后的体积仍大于 net/http
// 的缓冲阈值，这样才能观察到 chunked 传输。
func randomText(n int) string {
	r := rand.New(rand.NewSource(1))
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.Intn(len(alphabet))]
	}
	return string(b)
}

func TestServerUsesChunkedForLargeCompressedBody(t *testing.T) {
	body := randomText(40000)
	srv := newServer(t, DefaultSpec(), bodyHandler("text/plain; charset=utf-8", body))

	resp, raw := get(t, http.MethodGet, srv.URL, "br")

	if got := resp.Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q，期望 br", got)
	}
	// 压缩后长度不再可知，必须去掉原始 Content-Length 并改用 chunked
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Errorf("压缩后不该带 Content-Length，实际 = %q", got)
	}
	if resp.ContentLength != -1 {
		t.Errorf("ContentLength = %d，期望 -1（未知）", resp.ContentLength)
	}
	if len(resp.TransferEncoding) != 1 || resp.TransferEncoding[0] != "chunked" {
		t.Errorf("TransferEncoding = %v，期望 [chunked]", resp.TransferEncoding)
	}

	decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if string(decoded) != body {
		t.Error("解压后的内容与原文不一致")
	}
}

// 压缩后体积很小时 net/http 会自行补上 Content-Length 而不是走 chunked。
// 这没问题，关键是长度必须按压缩后的体积算——用原始长度会让客户端截断响应。
func TestServerCompressedContentLengthIsNeverTheOriginal(t *testing.T) {
	srv := newServer(t, DefaultSpec(), bodyHandler("text/html; charset=utf-8", bigHTML))

	resp, raw := get(t, http.MethodGet, srv.URL, "br, gzip, deflate")

	if got := resp.Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q，期望 br", got)
	}

	if got := resp.Header.Get("Content-Length"); got != "" {
		n, err := strconv.Atoi(got)
		if err != nil {
			t.Fatalf("Content-Length = %q，不是合法数字", got)
		}
		if n == len(bigHTML) {
			t.Fatalf("Content-Length = %d，等于原始长度，说明没按压缩后的体积重算", n)
		}
		if n != len(raw) {
			t.Fatalf("Content-Length = %d，与实际收到的 %d 字节不符", n, len(raw))
		}
	}

	decoded, err := io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if string(decoded) != bigHTML {
		t.Error("解压后的内容与原文不一致")
	}
}

func TestServerKeepsContentLengthWhenNotCompressed(t *testing.T) {
	const small = "<p>小响应</p>"
	srv := newServer(t, DefaultSpec(), bodyHandler("text/html; charset=utf-8", small))

	resp, raw := get(t, http.MethodGet, srv.URL, "br, gzip")

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("小响应不该压缩，实际 %q", got)
	}
	if resp.ContentLength != int64(len(small)) {
		t.Errorf("ContentLength = %d，期望 %d", resp.ContentLength, len(small))
	}
	if len(resp.TransferEncoding) != 0 {
		t.Errorf("未压缩时应直接给长度，不该 chunked，实际 %v", resp.TransferEncoding)
	}
	if string(raw) != small {
		t.Errorf("响应体 = %q，期望 %q", raw, small)
	}
}

func TestServerHeadIsNotCompressed(t *testing.T) {
	srv := newServer(t, DefaultSpec(), bodyHandler("text/html; charset=utf-8", bigHTML))

	resp, raw := get(t, http.MethodHead, srv.URL, "br, gzip")

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("HEAD 不该带 Content-Encoding，实际 %q", got)
	}
	if len(raw) != 0 {
		t.Errorf("HEAD 响应体应为空，实际 %d 字节", len(raw))
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q，应包含 Accept-Encoding", got)
	}
}

func TestServerRangeIsPassedThrough(t *testing.T) {
	body := strings.Repeat("abcdefghij", 3000) // 30KB，远超阈值
	srv := newServer(t, DefaultSpec(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, "a.txt", time.Time{}, strings.NewReader(body))
	})

	// 先确认不带 Range 时确实会压缩，并移除了 Accept-Ranges
	resp, _ := get(t, http.MethodGet, srv.URL, "br, gzip")
	if got := resp.Header.Get("Content-Encoding"); got != "br" {
		t.Fatalf("不带 Range 时应压缩，实际 %q", got)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "" {
		t.Errorf("压缩后 Accept-Ranges 应被移除，实际 %q", got)
	}

	// 带 Range 时必须原样走 ServeContent，区间语义不能被破坏
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	req.Header.Set("Range", "bytes=0-99")

	ranged, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Range 请求失败: %v", err)
	}
	defer ranged.Body.Close()
	raw, _ := io.ReadAll(ranged.Body)

	if ranged.StatusCode != http.StatusPartialContent {
		t.Fatalf("状态码 = %d，期望 206", ranged.StatusCode)
	}
	if got := ranged.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Range 响应不该压缩，实际 %q", got)
	}
	if got := ranged.Header.Get("Content-Range"); got != "bytes 0-99/30000" {
		t.Errorf("Content-Range = %q，期望 bytes 0-99/30000", got)
	}
	if string(raw) != body[:100] {
		t.Error("返回的区间内容不正确")
	}
}

func TestServerNonCompressibleBodyIsPassedThrough(t *testing.T) {
	// 用一段合法的 PNG 头部，让 http.DetectContentType 也能识别
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a},
		bytes.Repeat([]byte{0x00}, 20*1024)...)

	srv := newServer(t, DefaultSpec(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})

	resp, raw := get(t, http.MethodGet, srv.URL, "br, gzip, deflate")

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("图片不该压缩，实际 %q", got)
	}
	if !bytes.Equal(raw, png) {
		t.Error("图片内容被改动")
	}
	if got := resp.Header.Get("Content-Length"); got != "" && got != strconv.Itoa(len(png)) {
		t.Errorf("Content-Length = %q，期望 %d", got, len(png))
	}
}

func TestServerStatusAndVaryOnErrorResponses(t *testing.T) {
	srv := newServer(t, DefaultSpec(), func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, bigHTML)
	})

	resp, raw := get(t, http.MethodGet, srv.URL, "gzip")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", resp.StatusCode)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q，应包含 Accept-Encoding", got)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q，期望 gzip", got)
	}

	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("构造 gzip 解压器失败: %v", err)
	}
	decoded, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if string(decoded) != bigHTML {
		t.Error("解压后的内容与原文不一致")
	}
}

func TestServerRedirectCarriesVary(t *testing.T) {
	srv := newServer(t, DefaultSpec(), func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Accept-Encoding", "br, gzip")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("状态码 = %d，期望 302", resp.StatusCode)
	}
	if got := resp.Header.Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Errorf("Vary = %q，应包含 Accept-Encoding", got)
	}
}

func TestServerDisabledIsFullyTransparent(t *testing.T) {
	srv := newServer(t, Spec{Enabled: false}, bodyHandler("text/html; charset=utf-8", bigHTML))

	resp, raw := get(t, http.MethodGet, srv.URL, "br, gzip, deflate")

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("关闭压缩后不该有 Content-Encoding，实际 %q", got)
	}
	if got := resp.Header.Get("Vary"); got != "" {
		t.Errorf("关闭压缩后不该有 Vary，实际 %q", got)
	}
	if string(raw) != bigHTML {
		t.Error("响应体应原样透传")
	}
	if got := resp.Header.Get("Content-Length"); got != "" && got != strconv.Itoa(len(bigHTML)) {
		t.Errorf("Content-Length = %q，期望 %d", got, len(bigHTML))
	}
}

func TestServerThresholdControlsCompression(t *testing.T) {
	// 同一段内容，只改阈值，压缩与否应当随之改变
	body := strings.Repeat("x", 3000)

	for _, tc := range []struct {
		minSize int
		want    string
	}{
		{1024, "br"},
		{5000, ""},
	} {
		srv := newServer(t, Spec{Enabled: true, MinSize: tc.minSize},
			bodyHandler("text/plain; charset=utf-8", body))
		resp, _ := get(t, http.MethodGet, srv.URL, "br")

		if got := resp.Header.Get("Content-Encoding"); got != tc.want {
			t.Errorf("阈值 %d 时 Content-Encoding = %q，期望 %q", tc.minSize, got, tc.want)
		}
	}
}
