package admin

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tmiqpl/blog/internal/auth"
)

const flashCookieName = "blog_flash"

// captchaCookieName 存放当前验证码的挑战标识。
// 答案本身只存在服务端，Cookie 里只有一个不可预测的随机 ID。
const captchaCookieName = "blog_captcha"

// authedHandler 是已通过鉴权的处理器，可直接拿到当前会话。
type authedHandler func(w http.ResponseWriter, r *http.Request, sess *auth.Session)

/* --------------------------- 会话 Cookie --------------------------- */

// isSecureRequest 判断当前请求是否走 HTTPS（兼容反向代理）。
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) sessionFrom(r *http.Request) (*auth.Session, bool) {
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	return s.auth.Get(c.Value)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, sess *auth.Session) {
	maxAge := int(time.Until(sess.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  sess.ExpiresAt,
		MaxAge:   maxAge,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

/* ----------------------------- 提示消息 ----------------------------- */

// setFlash 通过 Cookie 传递一次性提示消息。
func setFlash(w http.ResponseWriter, r *http.Request, kind, message string) {
	value := base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + message))
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    value,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30,
	})
}

// popFlash 读取并立即清除提示消息。
func popFlash(w http.ResponseWriter, r *http.Request) *flashMessage {
	c, err := r.Cookie(flashCookieName)
	if err != nil || c.Value == "" {
		return nil
	}

	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})

	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	kind, message, ok := strings.Cut(string(raw), "|")
	if !ok || message == "" {
		return nil
	}
	return &flashMessage{Kind: kind, Message: message}
}

// redirectWithFlash 设置提示并跳转。
func (s *Server) redirectWithFlash(w http.ResponseWriter, r *http.Request, to, kind, message string) {
	setFlash(w, r, kind, message)
	http.Redirect(w, r, to, http.StatusSeeOther)
}

/* ---------------------------- 验证码 Cookie ---------------------------- */

func setCaptchaCookie(w http.ResponseWriter, r *http.Request, id string, ttl time.Duration) {
	maxAge := int(ttl.Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     captchaCookieName,
		Value:    id,
		Path:     BasePath,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func clearCaptchaCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     captchaCookieName,
		Value:    "",
		Path:     BasePath,
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// captchaIDFromRequest 取出当前验证码的挑战标识。
func captchaIDFromRequest(r *http.Request) string {
	c, err := r.Cookie(captchaCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

/* ------------------------------ CSRF ------------------------------ */

// isWriteMethod 判断是否为需要 CSRF 校验的写操作。
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// csrfFromRequest 从请求头或表单中提取 CSRF 令牌。
func csrfFromRequest(r *http.Request) string {
	if token := r.Header.Get(auth.CSRFHeaderName); token != "" {
		return token
	}

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseForm(); err == nil {
			return r.PostFormValue(auth.CSRFFormField)
		}
	}
	return ""
}

func validCSRF(r *http.Request, sess *auth.Session) bool {
	token := csrfFromRequest(r)
	if token == "" || sess.CSRF == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRF)) == 1
}

/* ---------------------------- 鉴权中间件 ---------------------------- */

// requireAuth 包装需要登录的处理器，并统一处理 CSRF 校验。
func (s *Server) requireAuth(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.sessionFrom(r)
		if !ok {
			s.clearSessionCookie(w, r)
			if r.Method == http.MethodGet {
				target := "/admin/login?next=" + url.QueryEscape(r.URL.RequestURI())
				http.Redirect(w, r, target, http.StatusSeeOther)
				return
			}
			http.Error(w, "登录状态已失效，请重新登录", http.StatusUnauthorized)
			return
		}

		if isWriteMethod(r.Method) && !validCSRF(r, sess) {
			s.logger.Warn("CSRF 校验失败", "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "CSRF 校验失败，请刷新页面后重试", http.StatusForbidden)
			return
		}

		next(w, r, sess)
	}
}

// redirectIfAuthed 用于登录页：已登录用户直接跳转到后台首页。
func (s *Server) redirectIfAuthed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.sessionFrom(r); ok {
			http.Redirect(w, r, "/admin", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}
