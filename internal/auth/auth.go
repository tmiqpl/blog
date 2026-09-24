// Package auth 提供密码哈希、内存会话与 CSRF 令牌能力。
//
// 会话保存在进程内存中，适合单实例部署的个人博客；
// 进程重启后所有登录态失效，需要重新登录。
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// SessionCookieName 是登录态 Cookie 的名称。
	SessionCookieName = "blog_session"

	// CSRFHeaderName 是 AJAX 请求携带 CSRF 令牌的请求头。
	CSRFHeaderName = "X-CSRF-Token"

	// CSRFFormField 是表单携带 CSRF 令牌的字段名。
	CSRFFormField = "_csrf"

	pbkdf2Iterations = 120_000
	pbkdf2KeyLength  = 32
	saltLength       = 16
)

/* ---------------------------- 密码哈希 ---------------------------- */

var errBadHashFormat = errors.New("密码哈希格式不正确")

// HashPassword 使用 PBKDF2-HMAC-SHA256 生成可存储的密码哈希。
// 输出格式：pbkdf2-sha256$迭代次数$salt(base64)$hash(base64)
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("密码不能为空")
	}

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成随机盐失败: %w", err)
	}

	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyLength)
	if err != nil {
		return "", fmt.Errorf("计算密码哈希失败: %w", err)
	}

	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword 校验明文密码是否与存储的哈希匹配，使用恒定时间比较。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > 5_000_000 {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

/* ------------------------------ 会话 ------------------------------ */

// Session 表示一次登录会话。
type Session struct {
	ID        string
	Username  string
	CSRF      string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Expired 判断会话是否已过期。
func (s *Session) Expired() bool { return time.Now().After(s.ExpiresAt) }

// Manager 管理内存中的会话集合。
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
}

// NewManager 创建会话管理器，ttl 为会话有效期。
func NewManager(ttl time.Duration) *Manager {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &Manager{
		sessions: make(map[string]*Session),
		ttl:      ttl,
	}
}

// Create 为指定用户创建新会话。
func (m *Manager) Create(username string) (*Session, error) {
	id, err := RandomToken(32)
	if err != nil {
		return nil, err
	}
	csrf, err := RandomToken(32)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	sess := &Session{
		ID:        id,
		Username:  username,
		CSRF:      csrf,
		CreatedAt: now,
		ExpiresAt: now.Add(m.ttl),
	}

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	return sess, nil
}

// Get 按 ID 查找有效会话。
func (m *Manager) Get(id string) (*Session, bool) {
	if id == "" {
		return nil, false
	}

	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return nil, false
	}
	if sess.Expired() {
		m.Destroy(id)
		return nil, false
	}
	return sess, true
}

// Destroy 销毁指定会话。
func (m *Manager) Destroy(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

// DestroyAll 清空所有会话（例如修改密码后强制重新登录）。
func (m *Manager) DestroyAll() {
	m.mu.Lock()
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()
}

// Count 返回当前有效会话数量。
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// GC 清理已过期会话，返回清理数量。
func (m *Manager) GC() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, sess := range m.sessions {
		if now.After(sess.ExpiresAt) {
			delete(m.sessions, id)
			removed++
		}
	}
	return removed
}

/* --------------------------- 登录限流 --------------------------- */

// Limiter 对登录失败次数做简单的时间窗限流，防止暴力破解。
type Limiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
	limit    int
	window   time.Duration
}

type attemptRecord struct {
	count int
	until time.Time
}

// NewLimiter 创建限流器：window 时间窗内最多允许 limit 次失败。
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{
		attempts: make(map[string]*attemptRecord),
		limit:    limit,
		window:   window,
	}
}

// Allowed 判断来源是否仍可尝试登录。
func (l *Limiter) Allowed(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.attempts[key]
	if !ok {
		return true, 0
	}
	if time.Now().After(rec.until) {
		delete(l.attempts, key)
		return true, 0
	}
	if rec.count >= l.limit {
		return false, time.Until(rec.until)
	}
	return true, 0
}

// Fail 记录一次失败。
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec, ok := l.attempts[key]
	if !ok || time.Now().After(rec.until) {
		l.attempts[key] = &attemptRecord{count: 1, until: time.Now().Add(l.window)}
		return
	}
	rec.count++
}

// Reset 在登录成功后清除该来源的失败记录。
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

/* ------------------------------ 工具 ------------------------------ */

// RandomToken 生成 n 字节随机数的十六进制字符串。
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}
