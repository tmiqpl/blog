package compress

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

// bigHTML 是一段超过默认阈值的内容。
var bigHTML = strings.Repeat("<p>响应体足够大，值得压缩。</p>\n", 400)

/* ------------------------------ 编码协商 ------------------------------ */

func TestNegotiatePriority(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   Encoding
		ok     bool
	}{
		{"br 优先", "gzip, deflate, br", EncodingBrotli, true},
		{"只有 gzip 与 deflate 时选 gzip", "gzip, deflate", EncodingGzip, true},
		{"只有 deflate", "deflate", EncodingDeflate, true},
		{"顺序不影响优先级", "deflate, gzip, br", EncodingBrotli, true},
		{"br 被 q=0 拒绝则退到 gzip", "br;q=0, gzip", EncodingGzip, true},
		{"br 与 gzip 都被拒绝则退到 deflate", "br;q=0, gzip;q=0, deflate", EncodingDeflate, true},
		{"全部被拒绝", "br;q=0, gzip;q=0, deflate;q=0", "", false},
		{"通配符放行 br", "gzip;q=0, *", EncodingBrotli, true},
		{"只接受通配符时仍按优先级选 br", "*", EncodingBrotli, true},
		{"带空格的写法", " br ; q=0.8 , gzip;q=0.5 ", EncodingBrotli, true},
		{"x-gzip 视为 gzip", "x-gzip", EncodingGzip, true},
		{"identity 不算压缩算法", "identity", "", false},
		{"空头", "", "", false},
		{"质量值写坏按不接受处理", "br;q=abc, gzip", EncodingGzip, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := negotiate(tc.header)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("negotiate(%q) = (%q, %v)，期望 (%q, %v)", tc.header, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCompressibleType(t *testing.T) {
	yes := []string{
		"text/html; charset=utf-8",
		"text/css",
		"text/plain",
		"application/json",
		"application/manifest+json",
		"application/javascript",
		"image/svg+xml",
		"application/rss+xml",
	}
	no := []string{
		"",
		"image/png",
		"image/jpeg",
		"image/webp",
		"video/mp4",
		"audio/mpeg",
		"font/woff2",
		"application/zip",
		"application/gzip",
		"application/octet-stream",
	}

	for _, ct := range yes {
		if !compressibleType(ct) {
			t.Errorf("%q 应判定为可压缩", ct)
		}
	}
	for _, ct := range no {
		if compressibleType(ct) {
			t.Errorf("%q 不应判定为可压缩", ct)
		}
	}
}

/* ------------------------------ 中间件行为 ------------------------------ */

// serve 用给定请求头跑一遍中间件，返回记录器与响应体。
func serve(t *testing.T, req *http.Request, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	DefaultSpec().Middleware(handler).ServeHTTP(rec, req)
	return rec
}

func bodyHandler(contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = io.WriteString(w, body)
	}
}

// decode 按 Content-Encoding 解压响应体，便于直接断言原文。
func decode(t *testing.T, encoding string, raw []byte) string {
	t.Helper()
	if encoding == "" {
		return string(raw)
	}

	var (
		r   io.Reader
		err error
	)
	switch encoding {
	case "br":
		r = brotli.NewReader(bytes.NewReader(raw))
	case "gzip":
		r, err = gzip.NewReader(bytes.NewReader(raw))
	case "deflate":
		r, err = zlib.NewReader(bytes.NewReader(raw))
	default:
		t.Fatalf("未知编码 %q", encoding)
	}
	if err != nil {
		t.Fatalf("构造 %s 解压器失败: %v", encoding, err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("解压 %s 失败: %v", encoding, err)
	}
	return string(out)
}

func TestCompressByAcceptEncoding(t *testing.T) {
	cases := []struct {
		name    string
		accept  string
		wantEnc string
	}{
		{"优先 br", "gzip, deflate, br", "br"},
		{"其次 gzip", "gzip, deflate", "gzip"},
		{"最后 deflate", "deflate", "deflate"},
		{"客户端不支持压缩则不压", "", ""},
		{"只接受 identity 则不压", "identity", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rec := serve(t, req, bodyHandler("text/html; charset=utf-8", bigHTML))

			if got := rec.Header().Get("Content-Encoding"); got != tc.wantEnc {
				t.Fatalf("Content-Encoding = %q，期望 %q", got, tc.wantEnc)
			}
			if got := decode(t, tc.wantEnc, rec.Body.Bytes()); got != bigHTML {
				t.Fatalf("解压后的内容与原文不一致（长度 %d vs %d）", len(got), len(bigHTML))
			}
			if tc.wantEnc != "" && rec.Body.Len() >= len(bigHTML) {
				t.Errorf("压缩后 %d 字节，未小于原文 %d 字节", rec.Body.Len(), len(bigHTML))
			}
			if vary := rec.Header().Get("Vary"); !strings.Contains(vary, "Accept-Encoding") {
				t.Errorf("Vary = %q，应包含 Accept-Encoding", vary)
			}
		})
	}
}

func TestSmallResponseIsNotCompressed(t *testing.T) {
	const small = "<p>很短的一段内容</p>"

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip, deflate")
	rec := serve(t, req, bodyHandler("text/html; charset=utf-8", small))

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("小响应不该被压缩，实际 Content-Encoding = %q", got)
	}
	if got := rec.Body.String(); got != small {
		t.Fatalf("响应体 = %q，期望 %q", got, small)
	}
	// 未压缩时长度信息应保留，便于客户端按长度收包
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err != nil || n != len(small) {
			t.Errorf("Content-Length = %q，期望 %d", cl, len(small))
		}
	}
}

func TestExactlyAtThresholdIsNotCompressed(t *testing.T) {
	body := strings.Repeat("a", DefaultMinSize)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, bodyHandler("text/plain; charset=utf-8", body))

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("恰好等于阈值不该压缩（要求「大于」5KB），实际 %q", got)
	}
	if rec.Body.Len() != len(body) {
		t.Fatalf("响应体长度 = %d，期望 %d", rec.Body.Len(), len(body))
	}
}

func TestNonCompressibleTypeIsPassedThrough(t *testing.T) {
	body := strings.Repeat("x", DefaultMinSize*2)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := serve(t, req, bodyHandler("image/png", body))

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("已是压缩格式的类型不该再压缩，实际 %q", got)
	}
	if rec.Body.Len() != len(body) {
		t.Fatalf("响应体长度 = %d，期望 %d", rec.Body.Len(), len(body))
	}
}

func TestContentTypeSniffedFromBody(t *testing.T) {
	// 处理器没有设置 Content-Type，中间件需要自己嗅探，
	// 否则 net/http 嗅探到的会是压缩后的字节。
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br")
	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, bigHTML)
	})

	if got := rec.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("Content-Encoding = %q，期望 br", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q，期望以 text/html 开头", ct)
	}
	if got := decode(t, "br", rec.Body.Bytes()); got != bigHTML {
		t.Error("解压后的内容与原文不一致")
	}
}

func TestContentLengthDroppedWhenCompressed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(bigHTML)))
		_, _ = io.WriteString(w, bigHTML)
	})

	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("压缩后必须移除 Content-Length，实际 = %q", got)
	}
}

func TestSkipConditions(t *testing.T) {
	cases := []struct {
		name    string
		req     *http.Request
		handler http.HandlerFunc
	}{
		{
			name:    "HEAD 无响应体",
			req:     httptest.NewRequest(http.MethodHead, "/", nil),
			handler: bodyHandler("text/html", bigHTML),
		},
		{
			name: "Range 请求需要按字节区间返回",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("Range", "bytes=0-1023")
				return r
			}(),
			handler: bodyHandler("text/css", bigHTML),
		},
		{
			name:    "处理器已自行编码",
			req:     httptest.NewRequest(http.MethodGet, "/", nil),
			handler: bodyHandler("text/html", bigHTML),
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Header.Set("Accept-Encoding", "br, gzip")
			handler := tc.handler
			if i == 2 {
				handler = func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/html")
					w.Header().Set("Content-Encoding", "zstd")
					_, _ = io.WriteString(w, bigHTML)
				}
			}
			rec := serve(t, tc.req, handler)

			if got := rec.Header().Get("Content-Encoding"); got != "" && got != "zstd" {
				t.Fatalf("不该被再次压缩，实际 Content-Encoding = %q", got)
			}
			if rec.Body.String() != bigHTML {
				t.Fatalf("响应体应原样透传")
			}
		})
	}
}

func TestNoContentStatusNotCompressed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	if rec.Code != http.StatusNoContent {
		t.Fatalf("状态码 = %d，期望 204", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("204 不该带 Content-Encoding，实际 %q", got)
	}
}

func TestStatusAndBodyPreserved(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, bigHTML)
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", rec.Code)
	}
	if got := decode(t, "gzip", rec.Body.Bytes()); got != bigHTML {
		t.Error("解压后的内容与原文不一致")
	}
}

func TestMultipleWritesStreamAfterThreshold(t *testing.T) {
	// 分多次写入：越过阈值后应立即切换成流式压缩，而不是继续缓冲
	const chunk = "0123456789"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for i := 0; i < 2000; i++ {
			if _, err := io.WriteString(w, chunk); err != nil {
				t.Errorf("第 %d 次写入失败: %v", i, err)
				return
			}
		}
	})

	want := strings.Repeat(chunk, 2000)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q，期望 gzip", got)
	}
	if got := decode(t, "gzip", rec.Body.Bytes()); got != want {
		t.Fatalf("解压后长度 %d，期望 %d", len(got), len(want))
	}
}

func TestFlushPassesThroughUncompressed(t *testing.T) {
	// 处理器主动 Flush 说明在做流式输出，此时不该再缓冲等待阈值
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")

	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: hello\n\n")
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "data: world\n\n")
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Flush 后应原样透传，实际 Content-Encoding = %q", got)
	}
	if got := rec.Body.String(); got != "data: hello\n\ndata: world\n\n" {
		t.Fatalf("响应体 = %q", got)
	}
	if !rec.Flushed {
		t.Error("应把 Flush 透传到底层")
	}
}

func TestNoWriteStillProducesStatus(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "1")
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("响应体应为空，实际 %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("空响应不该带 Content-Encoding，实际 %q", got)
	}
}

func TestVaryNotDuplicated(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := serve(t, req, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", "Accept-Language")
		_, _ = io.WriteString(w, bigHTML)
	})

	if got := rec.Header().Values("Vary"); len(got) != 1 {
		t.Fatalf("Vary 应只有一行，实际 %v", got)
	}
	vary := rec.Header().Get("Vary")
	if !strings.Contains(vary, "Accept-Language") || !strings.Contains(vary, "Accept-Encoding") {
		t.Fatalf("Vary = %q，应同时包含 Accept-Language 与 Accept-Encoding", vary)
	}
}

/* ------------------------------ 参数解析 ------------------------------ */

func TestParseSpec(t *testing.T) {
	cases := []struct {
		raw     string
		enabled bool
		minSize int
	}{
		{"", true, DefaultMinSize},
		{"5KB", true, 5 * 1024},
		{"5kb", true, 5 * 1024},
		{"5k", true, 5 * 1024},
		{"5KiB", true, 5 * 1024},
		{" 5 KB ", true, 5 * 1024},
		{"10240", true, 10240},
		{"10240B", true, 10240},
		{"1MB", true, 1 << 20},
		{"1m", true, 1 << 20},
		{"0.5MB", true, 512 * 1024},
		{"1GB", true, 1 << 30},
		{"on", true, DefaultMinSize},
		{"ON", true, DefaultMinSize},
		{"true", true, DefaultMinSize},
		{"auto", true, DefaultMinSize},
		{"off", false, 0},
		{"OFF", false, 0},
		{"none", false, 0},
		{"false", false, 0},
		{"no", false, 0},
		{"0", false, 0},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			spec, err := ParseSpec(tc.raw)
			if err != nil {
				t.Fatalf("ParseSpec(%q) 返回错误: %v", tc.raw, err)
			}
			if spec.Enabled != tc.enabled || spec.MinSize != tc.minSize {
				t.Fatalf("ParseSpec(%q) = %+v，期望 {Enabled:%v MinSize:%d}",
					tc.raw, spec, tc.enabled, tc.minSize)
			}
		})
	}
}

func TestParseSpecRejectsBadInput(t *testing.T) {
	for _, raw := range []string{"5XB", "abc", "-1", "0KB", "0.5", "2GB", "1TB", "5..5KB", "1e3"} {
		if spec, err := ParseSpec(raw); err == nil {
			t.Errorf("ParseSpec(%q) 应当报错，实际返回 %+v", raw, spec)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int]string{
		512:     "512B",
		5120:    "5KB",
		5121:    "5121B",
		1536:    "1536B",
		1 << 20: "1MB",
		1 << 30: "1GB",
	}
	for n, want := range cases {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %q，期望 %q", n, got, want)
		}
	}
}

func TestSpecString(t *testing.T) {
	if got := (Spec{Enabled: false}).String(); got != "已关闭" {
		t.Errorf("关闭时 String() = %q，期望「已关闭」", got)
	}
	if got := DefaultSpec().String(); !strings.Contains(got, "5KB") {
		t.Errorf("默认 String() = %q，应包含 5KB", got)
	}
}

func TestDisabledSpecIsNoOp(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	if got := (Spec{Enabled: false}).Middleware(base); reflect.ValueOf(got).Pointer() != reflect.ValueOf(base).Pointer() {
		t.Fatal("关闭压缩时应原样返回 next，不做任何包装")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "br, gzip, deflate")
	rec := httptest.NewRecorder()
	Spec{Enabled: false}.Middleware(bodyHandler("text/html; charset=utf-8", bigHTML)).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("关闭压缩后不该出现 Content-Encoding，实际 %q", got)
	}
	if got := rec.Header().Get("Vary"); got != "" {
		t.Fatalf("关闭压缩后不该添加 Vary，实际 %q", got)
	}
	if rec.Body.String() != bigHTML {
		t.Fatal("响应体应原样透传")
	}
}

func TestCustomThreshold(t *testing.T) {
	const body = "<p>1234567890</p>"

	compressible := func(minSize int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		Spec{Enabled: true, MinSize: minSize}.
			Middleware(bodyHandler("text/html; charset=utf-8", body)).ServeHTTP(rec, req)
		return rec
	}

	// 阈值调到 10 字节：这个原本低于默认阈值的小响应也该被压缩
	low := compressible(10)
	if got := low.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("阈值 10 字节时应压缩，实际 Content-Encoding = %q", got)
	}
	if got := decode(t, "gzip", low.Body.Bytes()); got != body {
		t.Fatalf("解压后 = %q，期望 %q", got, body)
	}

	// 阈值调到 100 字节：同一个响应不再压缩
	high := compressible(100)
	if got := high.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("阈值 100 字节时不该压缩，实际 Content-Encoding = %q", got)
	}
	if high.Body.String() != body {
		t.Fatalf("响应体 = %q，期望原样透传", high.Body.String())
	}
}
