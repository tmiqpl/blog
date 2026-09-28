// Package compress 提供 HTTP 响应压缩中间件。
//
// 设计要点：
//
//   - 只有响应体超过阈值（默认 5KB）才压缩。小响应压缩后往往更大，
//     还白白多花 CPU，不如原样发出去。阈值可由启动参数调整，也可整体关闭。
//   - 编码按客户端 Accept-Encoding 协商，优先级固定为 br > gzip > deflate。
//     客户端用 q=0 明确拒绝的算法不会被选中。
//   - 是否压缩必须等响应体攒够阈值才能判断，因此 WriteHeader 会延迟到底层，
//     先缓冲内容；一旦超过阈值就立刻定下编码并开始流式压缩，不会把整个响应
//     憋在内存里。
//   - 处理器主动 Flush（流式输出）时不再缓冲，直接按原样透传。
package compress

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/andybalholm/brotli"
)

// DefaultMinSize 是触发压缩的响应体大小阈值（字节）。
// 5KB 以下的响应压缩收益有限，反而增加 CPU 开销与延迟。
const DefaultMinSize = 5 * 1024

// maxMinSize 是阈值上限。比这更大的阈值等于「基本不压」，不如直接写 off。
const maxMinSize = 1 << 30

// Encoding 是受支持的压缩算法，取值即 HTTP Content-Encoding 的令牌。
type Encoding string

const (
	EncodingBrotli  Encoding = "br"
	EncodingGzip    Encoding = "gzip"
	EncodingDeflate Encoding = "deflate"
)

// priority 定义协商优先级：先 br，再 gzip，最后 deflate。
var priority = []Encoding{EncodingBrotli, EncodingGzip, EncodingDeflate}

// newEncoder 按算法构造压缩器。调用方保证 e 一定在 priority 之中。
func newEncoder(e Encoding, dst io.Writer) (io.WriteCloser, error) {
	switch e {
	case EncodingBrotli:
		return brotli.NewWriterLevel(dst, brotli.DefaultCompression), nil
	case EncodingGzip:
		return gzip.NewWriterLevel(dst, gzip.DefaultCompression)
	case EncodingDeflate:
		// HTTP 的 deflate 指 RFC 1950 的 zlib 包装格式，不是裸 deflate 流
		return zlib.NewWriterLevel(dst, zlib.DefaultCompression)
	default:
		return nil, fmt.Errorf("不支持的压缩算法: %q", e)
	}
}

// Spec 是压缩配置：是否启用，以及触发压缩的响应体大小阈值。
type Spec struct {
	Enabled bool
	MinSize int
}

// DefaultSpec 返回默认配置：启用压缩，阈值 DefaultMinSize。
func DefaultSpec() Spec {
	return Spec{Enabled: true, MinSize: DefaultMinSize}
}

// threshold 返回实际生效的阈值，未设置时回落到默认值。
func (s Spec) threshold() int {
	if s.MinSize > 0 {
		return s.MinSize
	}
	return DefaultMinSize
}

// String 返回便于写进日志的描述。
func (s Spec) String() string {
	if !s.Enabled {
		return "已关闭"
	}
	return "br/gzip/deflate，阈值 " + HumanSize(s.threshold())
}

// Middleware 返回压缩中间件。未启用压缩时原样返回 next，不增加任何开销。
func (s Spec) Middleware(next http.Handler) http.Handler {
	if !s.Enabled {
		return next
	}
	minSize := s.threshold()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HEAD 没有响应体；Range 请求要求按字节区间返回，
		// 压缩会破坏区间语义，两者都原样放行。
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" {
			addVary(w.Header(), "Accept-Encoding")
			next.ServeHTTP(w, r)
			return
		}

		cw := &responseWriter{ResponseWriter: w, req: r, minSize: minSize}
		// 处理器返回后收尾：把没超过阈值的内容原样写出，
		// 并关闭压缩器写入收尾字节（如 gzip 的 CRC 与长度）。
		defer func() { _ = cw.Close() }()

		next.ServeHTTP(cw, r)
	})
}

/* ------------------------------ 参数解析 ------------------------------ */

// ParseSpec 解析 -compress 的取值，一个参数同时管「开关」与「阈值」。
//
// 接受三类写法：
//
//	off / none / false / no / 0        关闭压缩
//	on / true / yes / auto             启用压缩，阈值用默认值
//	5120 / 5KB / 1MB / 0.5MB           启用压缩，阈值按 1024 进制换算
//
// 空字符串视为默认配置。
func ParseSpec(raw string) (Spec, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return DefaultSpec(), nil
	}

	switch value {
	case "off", "none", "false", "no", "disable", "disabled", "0":
		return Spec{Enabled: false}, nil
	case "on", "true", "yes", "enable", "enabled", "auto", "default":
		return DefaultSpec(), nil
	}

	size, err := parseSize(value)
	if err != nil {
		return Spec{}, err
	}
	return Spec{Enabled: true, MinSize: size}, nil
}

// parseSize 解析 "5120"、"5KB"、"1MB" 这类大小写法，单位按 1024 进制换算。
// 允许小数（如 0.5MB），也接受 KiB / MiB 这类写法。
func parseSize(value string) (int, error) {
	digits, unit := value, ""
	for i, r := range value {
		if (r < '0' || r > '9') && r != '.' {
			digits, unit = value[:i], value[i:]
			break
		}
	}

	// 单位允许写成 K / KB / KiB，含义相同；纯数字按字节计
	multiplier := 1
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "", "b":
	case "k", "kb", "kib":
		multiplier = 1 << 10
	case "m", "mb", "mib":
		multiplier = 1 << 20
	case "g", "gb", "gib":
		multiplier = 1 << 30
	default:
		return 0, fmt.Errorf("无法识别的大小单位 %q", unit)
	}

	n, err := strconv.ParseFloat(digits, 64)
	if err != nil {
		return 0, fmt.Errorf("无法识别的大小 %q", value)
	}

	total := n * float64(multiplier)
	switch {
	case total < 1:
		return 0, fmt.Errorf("压缩阈值必须大于 0 字节")
	case total > maxMinSize:
		return 0, fmt.Errorf("压缩阈值不能超过 %s；若要关闭压缩请用 off", HumanSize(maxMinSize))
	}
	return int(total), nil
}

// HumanSize 把字节数写成便于阅读的形式：5120 → 5KB、1048576 → 1MB。
// 不是整数倍时保留原始字节数，避免 5121 被四舍五入成 5KB 造成误导。
func HumanSize(n int) string {
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return strconv.Itoa(n>>30) + "GB"
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.Itoa(n>>20) + "MB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.Itoa(n>>10) + "KB"
	default:
		return strconv.Itoa(n) + "B"
	}
}

// responseWriter 是一个延迟决策的 ResponseWriter：
// 先把内容攒在缓冲区，直到能判断出「是否值得压缩」为止。
type responseWriter struct {
	http.ResponseWriter
	req     *http.Request
	minSize int

	status      int
	wroteHeader bool // 处理器是否已调用过 WriteHeader
	decided     bool // 是否已经定下编码并把响应提交到底层
	skip        bool // 命中无需压缩的情形，原样透传
	buf         bytes.Buffer

	enc     io.WriteCloser
	flusher interface{ Flush() error }
}

func (w *responseWriter) WriteHeader(status int) {
	// 1xx 是中间响应，之后还会有一个真正的状态行，直接透传且不占用状态位
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status

	// 这些响应要么没有实体，要么内容已被处理器自行编码，不该再动
	if status == http.StatusNoContent ||
		status == http.StatusResetContent ||
		status == http.StatusNotModified ||
		w.Header().Get("Content-Encoding") != "" {
		w.skip = true
	}

	// 无论最终是否压缩都要声明，否则共享缓存可能把压缩结果发给不支持该算法的客户端
	addVary(w.Header(), "Accept-Encoding")

	if w.skip {
		w.decided = true
		w.ResponseWriter.WriteHeader(status)
	}
	// 否则先按下不表，等 Write 攒够内容再决定
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.decided {
		return w.writeDirect(b)
	}

	w.buf.Write(b)
	if w.buf.Len() > w.minSize {
		// 越过阈值，此刻才值得压缩
		if err := w.commit(true); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// writeDirect 在已经定下编码后写入内容。
func (w *responseWriter) writeDirect(b []byte) (int, error) {
	if w.enc != nil {
		return w.enc.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// commit 定下编码，把状态行与已缓冲的内容提交到底层。
// compressed 表示缓冲区已超过阈值、满足压缩条件。
func (w *responseWriter) commit(compressed bool) error {
	if w.decided {
		return nil
	}
	w.decided = true

	// 先尝试建好压缩器再写状态行：万一构造失败还能干净地退回不压缩
	var (
		name Encoding
		cw   io.WriteCloser
	)
	if compressed && !w.skip && w.compressible() {
		if e, ok := negotiate(w.req.Header.Get("Accept-Encoding")); ok {
			if c, err := newEncoder(e, w.ResponseWriter); err == nil {
				name, cw = e, c
			}
		}
	}

	if cw == nil {
		w.ResponseWriter.WriteHeader(w.status)
		_, err := w.buf.WriteTo(w.ResponseWriter)
		w.buf = bytes.Buffer{}
		return err
	}

	h := w.Header()
	h.Set("Content-Encoding", string(name))
	// 长度与可寻址性都随压缩改变，必须清掉，交给 chunked 传输
	h.Del("Content-Length")
	h.Del("Accept-Ranges")

	w.enc = cw
	if f, ok := cw.(interface{ Flush() error }); ok {
		w.flusher = f
	}

	w.ResponseWriter.WriteHeader(w.status)
	_, err := w.buf.WriteTo(cw)
	w.buf = bytes.Buffer{}
	return err
}

// compressible 判断当前响应是否值得压缩。
// Content-Type 缺失时（交给 net/http 嗅探的情形）从已缓冲的内容里推断。
func (w *responseWriter) compressible() bool {
	ct := w.Header().Get("Content-Type")
	if ct == "" {
		if w.buf.Len() == 0 {
			return false
		}
		ct = http.DetectContentType(w.buf.Bytes())
		// 自行定下类型：压缩后 net/http 嗅探到的是压缩字节，会识别错
		w.Header().Set("Content-Type", ct)
	}
	return compressibleType(ct)
}

// Close 收尾：没超过阈值的内容原样写出，并关闭压缩器。
func (w *responseWriter) Close() error {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.decided {
		if err := w.commit(false); err != nil {
			return err
		}
	}
	if w.enc != nil {
		err := w.enc.Close()
		w.enc = nil
		return err
	}
	return nil
}

// Flush 立刻把已有内容发出去。处理器主动 Flush 意味着它在做流式输出，
// 此时不该继续缓冲等待阈值，直接按原样透传。
func (w *responseWriter) Flush() { _ = w.FlushError() }

// FlushError 是 Flush 的带错误版本，http.ResponseController 优先使用它。
func (w *responseWriter) FlushError() error {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if !w.decided {
		if err := w.commit(false); err != nil {
			return err
		}
	}
	if w.flusher != nil {
		if err := w.flusher.Flush(); err != nil {
			return err
		}
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

/* ------------------------------ 编码协商 ------------------------------ */

// acceptEncoding 记录 Accept-Encoding 中各令牌的质量值（q）。
type acceptEncoding map[string]float64

// q 返回某个编码的质量值。显式列出以显式值为准，否则由通配符 * 兜底，
// 两者都没有则视为客户端不接受该编码。
func (a acceptEncoding) q(token string) float64 {
	if q, ok := a[token]; ok {
		return q
	}
	if q, ok := a["*"]; ok {
		return q
	}
	return 0
}

// negotiate 按 br > gzip > deflate 的优先级挑选算法。
// 客户端用 q=0 拒绝的算法会被跳过。
func negotiate(header string) (Encoding, bool) {
	if strings.TrimSpace(header) == "" {
		return "", false
	}
	accepted := parseAcceptEncoding(header)
	for _, e := range priority {
		if accepted.q(string(e)) > 0 {
			return e, true
		}
	}
	return "", false
}

// parseAcceptEncoding 解析 Accept-Encoding，形如 "gzip, deflate;q=0.5, br;q=1.0"。
func parseAcceptEncoding(header string) acceptEncoding {
	out := make(acceptEncoding)
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		token = strings.ToLower(strings.TrimSpace(token))
		if token == "" {
			continue
		}
		// x-gzip 是 gzip 的历史别名
		if token == "x-gzip" {
			token = "gzip"
		}

		q := 1.0
		for _, p := range strings.Split(params, ";") {
			key, value, ok := strings.Cut(p, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(key), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				// 无法解析的质量值按「不接受」处理，与 RFC 的保守做法一致
				parsed = 0
			}
			q = parsed
		}

		// 同一编码重复出现时取最高质量值
		if old, ok := out[token]; !ok || q > old {
			out[token] = q
		}
	}
	return out
}

/* ------------------------------ 类型判断 ------------------------------ */

// compressibleType 判断某个 MIME 类型是否值得压缩。
// 用白名单而非黑名单：图片、音视频、woff/woff2 等本身已是压缩格式，
// 再压一遍只会更大，漏判的成本也比错判低。
func compressibleType(contentType string) bool {
	if contentType == "" {
		return false
	}

	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.TrimSpace(strings.Split(contentType, ";")[0])
	}
	mt = strings.ToLower(mt)

	if strings.HasPrefix(mt, "text/") {
		return true
	}
	// application/manifest+json、image/svg+xml 这类带后缀的变体
	if strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml") {
		return true
	}

	switch mt {
	case "application/json",
		"application/javascript",
		"application/x-javascript",
		"application/ecmascript",
		"application/xml",
		"application/rss+xml",
		"application/atom+xml",
		"application/wasm",
		"application/x-www-form-urlencoded",
		"image/svg+xml",
		"image/vnd.microsoft.icon",
		"image/x-icon",
		"font/ttf",
		"font/otf",
		"font/collection",
		"application/vnd.ms-fontobject",
		"application/x-font-ttf",
		"application/x-font-opentype",
		"application/x-font-truetype":
		return true
	}
	return false
}

// addVary 把 value 并入 Vary，已存在则跳过，避免重复累积。
// 多个 Vary 值合并成一行，保持响应头整洁。
func addVary(h http.Header, value string) {
	lines := h.Values("Vary")
	if len(lines) == 0 {
		h.Set("Vary", value)
		return
	}

	fields := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		for _, field := range strings.Split(line, ",") {
			field = strings.TrimSpace(field)
			if field == "" {
				continue
			}
			// Vary: * 已经表示「任何请求头都可能导致不同响应」，再加没有意义
			if field == "*" {
				return
			}
			if strings.EqualFold(field, value) {
				return
			}
			fields = append(fields, field)
		}
	}
	h.Set("Vary", strings.Join(append(fields, value), ", "))
}
