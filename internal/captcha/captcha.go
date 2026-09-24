// Package captcha 提供零依赖的图形验证码。
//
// 设计要点：
//   - 服务端用点阵字模直接绘制 PNG，不依赖任何外部字体或图形库
//   - 答案只保存在服务端内存中，图片里不含明文，前端拿不到答案
//   - 每个验证码只能校验一次（无论对错都立即作废），避免对同一张图反复试答案
//   - 超出容量上限时优先淘汰过期项，再淘汰最早生成的项，防止被刷爆内存
package captcha

import (
	crand "crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"sync"
	"time"
)

// ErrTooManyChallenges 表示当前待校验的验证码数量已达上限。
var ErrTooManyChallenges = errors.New("验证码数量已达上限")

// maxChallenges 是内存中同时保留的验证码数量上限。
const maxChallenges = 2000

// Options 控制验证码的长度与外观。
type Options struct {
	Width   int
	Height  int
	Length  int
	CharSet string
}

// DefaultOptions 返回一组默认参数。
// 字符集刻意剔除了 0/O、1/I/L、2/Z、5/S、8/B 等易混淆字符，降低用户看错的概率。
func DefaultOptions() Options {
	return Options{
		Width:   180,
		Height:  56,
		Length:  4,
		CharSet: "34679ACDEFGHJKMNPQRTUVWXY",
	}
}

func (o Options) withDefaults() Options {
	d := DefaultOptions()
	if o.Width <= 0 {
		o.Width = d.Width
	}
	if o.Height <= 0 {
		o.Height = d.Height
	}
	if o.Length <= 0 {
		o.Length = d.Length
	}
	if o.CharSet == "" {
		o.CharSet = d.CharSet
	}
	return o
}

// Challenge 是一次验证码挑战。
type Challenge struct {
	ID    string // 挑战标识，通过 Cookie 下发
	Code  string // 正确答案，仅服务端可见
	Image []byte // PNG 图片数据
}

type entry struct {
	code      string
	expiresAt time.Time
	seq       uint64 // 生成序号，用于容量淘汰
}

// Manager 负责生成与校验验证码，所有状态保存在内存中。
type Manager struct {
	mu    sync.Mutex
	items map[string]entry
	ttl   time.Duration
	opts  Options
	seq   uint64
}

// NewManager 创建验证码管理器，ttl 为单个验证码的有效期。
func NewManager(ttl time.Duration, opts Options) *Manager {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Manager{
		items: make(map[string]entry),
		ttl:   ttl,
		opts:  opts.withDefaults(),
	}
}

// TTL 返回验证码有效期。
func (m *Manager) TTL() time.Duration { return m.ttl }

// Generate 生成一张新的验证码。
func (m *Manager) Generate() (*Challenge, error) {
	code := m.randomCode()

	img, err := encodePNG(render(code, m.opts))
	if err != nil {
		return nil, fmt.Errorf("生成验证码图片失败: %w", err)
	}

	id, err := randomID()
	if err != nil {
		return nil, err
	}

	if err := m.put(id, code); err != nil {
		return nil, err
	}

	return &Challenge{ID: id, Code: code, Image: img}, nil
}

// Verify 校验答案。无论成功与否，该验证码都会被立即作废。
func (m *Manager) Verify(id, answer string) bool {
	answer = normalizeAnswer(answer)
	if id == "" || answer == "" {
		return false
	}

	m.mu.Lock()
	it, ok := m.items[id]
	delete(m.items, id) // 一次性消费，杜绝重复试错
	m.mu.Unlock()

	if !ok || time.Now().After(it.expiresAt) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(answer), []byte(it.code)) == 1
}

// Count 返回当前待校验的验证码数量。
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// GC 清理过期验证码，返回清理数量。
func (m *Manager) GC() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, it := range m.items {
		if now.After(it.expiresAt) {
			delete(m.items, id)
			removed++
		}
	}
	return removed
}

// put 保存一个验证码，必要时按容量上限淘汰旧数据。
func (m *Manager) put(id, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.items) >= maxChallenges {
		m.evictLocked()
		if len(m.items) >= maxChallenges {
			return ErrTooManyChallenges
		}
	}

	m.seq++
	m.items[id] = entry{
		code:      code,
		expiresAt: time.Now().Add(m.ttl),
		seq:       m.seq,
	}
	return nil
}

// evictLocked 先清理过期项；若仍超限，则淘汰最早生成的一批。调用方需持有锁。
func (m *Manager) evictLocked() {
	now := time.Now()
	for id, it := range m.items {
		if now.After(it.expiresAt) {
			delete(m.items, id)
		}
	}

	// 过期项清完还是满的，说明是短时间内的异常流量，按序号淘汰最早的四分之一
	for len(m.items) >= maxChallenges {
		threshold := m.seq
		if threshold > uint64(maxChallenges/4) {
			threshold -= uint64(maxChallenges / 4)
		} else {
			threshold = 0
		}
		removed := 0
		for id, it := range m.items {
			if it.seq <= threshold {
				delete(m.items, id)
				removed++
			}
		}
		if removed == 0 {
			// 兜底：序号都较新时直接丢掉任意一批，保证不会无限增长
			for id := range m.items {
				delete(m.items, id)
				removed++
				if removed >= maxChallenges/4 {
					break
				}
			}
		}
	}
}

// randomCode 从字符集中随机取 Length 个字符。
func (m *Manager) randomCode() string {
	set := []rune(m.opts.CharSet)
	out := make([]rune, m.opts.Length)
	for i := range out {
		out[i] = set[mrand.IntN(len(set))]
	}
	return string(out)
}

// randomID 生成不可预测的挑战标识。
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", fmt.Errorf("生成验证码标识失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}
