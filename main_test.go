package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// flushableRecorder 在 httptest.ResponseRecorder 基础上补一个 Flush，
// 用来探测 Flush 是否真的透传到了底层连接。
type flushableRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (r *flushableRecorder) Flush() {
	r.flushed = true
	r.ResponseRecorder.Flush()
}

// TestLogRequestsForwardsFlush 验证日志中间件的 statusRecorder 不阻断 Flush：
// 内层处理器（如 MCP 的 SSE 流式响应）调用 http.ResponseController.Flush() 时，
// 底层连接必须收到 Flush，否则流式输出会被缓冲到响应结束才发出。
func TestVersionLineIncludesBuildDate(t *testing.T) {
	oldV, oldD := version, buildDate
	t.Cleanup(func() { version, buildDate = oldV, oldD })

	version, buildDate = "1.2.3", "2026-11-11T08:30:00Z"

	line := versionLine()
	if !strings.Contains(line, "2026-11-11") {
		t.Errorf("版本信息应包含编译日期，实际 %q", line)
	}
	if !strings.Contains(line, "1.2.3") {
		t.Errorf("版本信息应包含版本号，实际 %q", line)
	}
}

func TestLogRequestsForwardsFlush(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush 不应返回错误: %v", err)
		}
	})

	rec := &flushableRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)

	logRequests(logger, inner).ServeHTTP(rec, req)

	if !rec.flushed {
		t.Error("内层处理器的 Flush 应透传到底层 ResponseWriter，实际没有到达")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("状态码应为 200，实际 %d", rec.Code)
	}
}
