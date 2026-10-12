package admin

import (
	"net/http"

	"github.com/tmiqpl/blog/internal/auth"
	"github.com/tmiqpl/blog/internal/store"
)

// mcpData 用于「MCP 接入」页面。
type mcpData struct {
	baseData
	Token      string
	Configured bool
}

// handleMCP 渲染 MCP 接入页面，展示当前访问令牌与客户端配置示例。
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	token, err := s.store.GetSetting(store.SettingMCPToken)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "加载失败", "读取 MCP 配置失败")
		return
	}

	data := mcpData{
		baseData:   s.baseAuthed(w, r, sess, navMCP, "MCP 接入"),
		Token:      token,
		Configured: token != "",
	}
	s.render(w, r, http.StatusOK, "mcp", data)
}

// handleMCPTokenGenerate 生成或重置 MCP 访问令牌。
// 重置后旧令牌立即失效，需要同步更新客户端配置。
func (s *Server) handleMCPTokenGenerate(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	raw, err := auth.RandomToken(24)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "生成失败", "生成令牌失败")
		return
	}
	token := mcpTokenPrefix + raw

	if err := s.store.SetSetting(store.SettingMCPToken, token); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "保存失败", "写入数据库失败")
		return
	}

	s.logger.Info("生成 MCP 访问令牌", "by", sess.Username)
	s.redirectWithFlash(w, r, BasePath+"/mcp", "success", "已生成新的 MCP 访问令牌，旧令牌已立即失效")
}

// handleMCPTokenRevoke 撤销 MCP 访问令牌。清空后 /mcp 端点对外返回 404。
func (s *Server) handleMCPTokenRevoke(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	if err := s.store.SetSetting(store.SettingMCPToken, ""); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, "撤销失败", "写入数据库失败")
		return
	}

	s.logger.Info("撤销 MCP 访问令牌", "by", sess.Username)
	s.redirectWithFlash(w, r, BasePath+"/mcp", "success", "已撤销 MCP 访问令牌，/mcp 端点已关闭")
}

// mcpTokenPrefix 是 MCP 令牌的前缀，便于在配置里一眼识别。
const mcpTokenPrefix = "bmt_"
