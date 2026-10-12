// Package mcp 提供博客的 MCP（Model Context Protocol）服务，
// 让支持 MCP 的 AI 客户端能够读取、新增与编辑文章。
//
// 服务以 Streamable HTTP 方式挂在 /mcp 上，通过 Bearer Token 鉴权，
// 与前台、后台共用同一个数据库连接。
package mcp

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmiqpl/blog/internal/store"
)

// Server 封装 MCP 服务的依赖。
type Server struct {
	store  *store.Store
	impl   *sdk.Implementation
	server *sdk.Server
}

// Options 是构造 MCP 服务所需的依赖。
type Options struct {
	Store *store.Store
	// Version 是站点版本号，会作为 MCP 服务实现的版本上报给客户端。
	Version string
}

// NewServer 构造 MCP 服务并注册全部工具。
func NewServer(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("MCP 服务需要数据库连接")
	}

	version := opts.Version
	if version == "" {
		version = "dev"
	}

	s := &Server{
		store: opts.Store,
		impl: &sdk.Implementation{
			Name:        "blog",
			Title:       "博客 MCP 服务",
			Description: "读取、新增与编辑博客文章",
			Version:     version,
		},
	}

	s.server = sdk.NewServer(s.impl, &sdk.ServerOptions{
		Instructions: "这是个人博客的内容管理服务。可以用 get_post / list_posts 查看现有文章，" +
			"用 create_post 新增、update_post 按 ID 编辑。正文使用 Markdown。",
	})
	s.registerTools()
	return s, nil
}

// Server 暴露底层的 MCP server，供测试用内存传输对接。
func (s *Server) MCP() *sdk.Server { return s.server }

// Handler 返回 /mcp 端点的 http.Handler（Streamable HTTP + Bearer 鉴权）。
func (s *Server) Handler() http.Handler {
	inner := sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return s.server }, nil)
	return s.requireToken(inner)
}

// registerTools 注册全部 MCP 工具。
func (s *Server) registerTools() {
	sdk.AddTool(s.server, &sdk.Tool{
		Name:  "create_post",
		Title: "新增文章",
		Description: "新增一篇博客文章。需要提供标题与 Markdown 正文；" +
			"slug、摘要、标签、发布时间都可选，slug 省略时由标题生成。默认立即发布。",
		Annotations: &sdk.ToolAnnotations{
			Title:           "新增文章",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false),
			IdempotentHint:  false,
			OpenWorldHint:   ptr(false),
		},
	}, s.handleCreatePost)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:  "update_post",
		Title: "编辑文章",
		Description: "按 ID 编辑已有文章。只需传入要修改的字段，未传的字段保持原值。" +
			"可以修改标题、正文、别名、摘要、标签与发布状态。",
		Annotations: &sdk.ToolAnnotations{
			Title:           "编辑文章",
			ReadOnlyHint:    false,
			DestructiveHint: ptr(false), // 只更新，不删除
			IdempotentHint:  true,
			OpenWorldHint:   ptr(false),
		},
	}, s.handleUpdatePost)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "list_posts",
		Title:       "列出文章",
		Description: "列出文章（含草稿），返回 ID、标题、别名、摘要、标签、状态与时间。不含正文，可用 get_post 读取全文。",
		Annotations: &sdk.ToolAnnotations{
			Title:           "列出文章",
			ReadOnlyHint:    true,
			DestructiveHint: ptr(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptr(false),
		},
	}, s.handleListPosts)

	sdk.AddTool(s.server, &sdk.Tool{
		Name:        "get_post",
		Title:       "读取文章全文",
		Description: "按 ID 读取一篇文章的完整内容（含 Markdown 正文），编辑前可先用它获取最新内容。",
		Annotations: &sdk.ToolAnnotations{
			Title:           "读取文章全文",
			ReadOnlyHint:    true,
			DestructiveHint: ptr(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptr(false),
		},
	}, s.handleGetPost)
}

// requireToken 校验请求头中的 Bearer Token。
//
// Token 从 settings 表读取（后台「MCP 接入」页面生成）：
// 未配置时返回 404，相当于默认关闭；配置后校验 Authorization 头，不匹配返回 401。
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := s.store.GetSetting(store.SettingMCPToken)
		if err != nil {
			http.Error(w, "读取 MCP 配置失败", http.StatusInternalServerError)
			return
		}
		if token == "" {
			// 未启用 MCP：当作端点不存在，避免暴露它的存在
			http.NotFound(w, r)
			return
		}

		presented := bearerToken(r)
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="blog-mcp"`)
			http.Error(w, "无效的 MCP 访问令牌", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// bearerToken 从 Authorization 头中提取 Bearer Token。
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

func ptr[T any](v T) *T { return &v }
