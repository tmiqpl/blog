package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	const password = "正确的密码-2026"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("生成哈希失败: %v", err)
	}

	if strings.Contains(encoded, password) {
		t.Error("哈希结果中不应包含明文密码")
	}
	if !strings.HasPrefix(encoded, "pbkdf2-sha256$") {
		t.Errorf("哈希格式不正确: %s", encoded)
	}
	if !VerifyPassword(encoded, password) {
		t.Error("正确密码应校验通过")
	}
	if VerifyPassword(encoded, "错误的密码") {
		t.Error("错误密码不应校验通过")
	}
}

func TestHashPasswordUsesRandomSalt(t *testing.T) {
	const password = "same-password"

	first, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Error("相同密码两次哈希结果应不同（随机盐）")
	}
	if !VerifyPassword(first, password) || !VerifyPassword(second, password) {
		t.Error("两个哈希都应能校验通过")
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	if _, err := HashPassword(""); err == nil {
		t.Error("空密码应返回错误")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	cases := []string{
		"",
		"plain-text",
		"pbkdf2-sha256$notanumber$c2FsdA$aGFzaA",
		"bcrypt$1000$c2FsdA$aGFzaA",
		"pbkdf2-sha256$1000$!!!invalid-base64!!!$aGFzaA",
	}

	for _, encoded := range cases {
		if VerifyPassword(encoded, "any") {
			t.Errorf("非法哈希 %q 不应校验通过", encoded)
		}
	}
}

func TestVerifyPasswordRejectsAbsurdIterations(t *testing.T) {
	// 防止通过超大迭代次数构造 DoS
	encoded := "pbkdf2-sha256$999999999$c2FsdA$aGFzaA"
	if VerifyPassword(encoded, "any") {
		t.Error("超出上限的迭代次数应被拒绝")
	}
}

func TestSessionLifecycle(t *testing.T) {
	m := NewManager(time.Hour)

	sess, err := m.Create("admin")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if sess.ID == "" || sess.CSRF == "" {
		t.Fatal("会话 ID 与 CSRF 令牌都不应为空")
	}
	if sess.ID == sess.CSRF {
		t.Error("会话 ID 与 CSRF 令牌应不同")
	}

	got, ok := m.Get(sess.ID)
	if !ok || got.Username != "admin" {
		t.Fatal("应能取回刚创建的会话")
	}

	if _, ok := m.Get("不存在的会话"); ok {
		t.Error("不存在的会话不应返回成功")
	}
	if _, ok := m.Get(""); ok {
		t.Error("空 ID 不应返回成功")
	}

	m.Destroy(sess.ID)
	if _, ok := m.Get(sess.ID); ok {
		t.Error("销毁后不应再能取回会话")
	}
}

func TestSessionExpiry(t *testing.T) {
	m := NewManager(time.Millisecond)

	sess, err := m.Create("admin")
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)

	if _, ok := m.Get(sess.ID); ok {
		t.Error("过期会话不应有效")
	}
	if n := m.Count(); n != 0 {
		t.Errorf("过期会话应被清理，剩余 %d 个", n)
	}
}

func TestSessionGarbageCollection(t *testing.T) {
	m := NewManager(time.Millisecond)

	for i := 0; i < 5; i++ {
		if _, err := m.Create("admin"); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(5 * time.Millisecond)

	if removed := m.GC(); removed != 5 {
		t.Errorf("应清理 5 个过期会话，实际 %d", removed)
	}
	if m.Count() != 0 {
		t.Errorf("清理后应无剩余会话，实际 %d", m.Count())
	}
}

func TestDestroyAll(t *testing.T) {
	m := NewManager(time.Hour)

	for i := 0; i < 3; i++ {
		if _, err := m.Create("admin"); err != nil {
			t.Fatal(err)
		}
	}
	m.DestroyAll()

	if m.Count() != 0 {
		t.Errorf("清空后应无剩余会话，实际 %d", m.Count())
	}
}

func TestLimiterBlocksAfterLimit(t *testing.T) {
	l := NewLimiter(3, time.Minute)
	const key = "192.0.2.1"

	for i := 0; i < 3; i++ {
		if allowed, _ := l.Allowed(key); !allowed {
			t.Fatalf("第 %d 次尝试不应被拦截", i+1)
		}
		l.Fail(key)
	}

	allowed, wait := l.Allowed(key)
	if allowed {
		t.Error("达到上限后应被拦截")
	}
	if wait <= 0 {
		t.Error("被拦截时应返回剩余等待时间")
	}
}

func TestLimiterIsolatesKeys(t *testing.T) {
	l := NewLimiter(1, time.Minute)

	l.Fail("192.0.2.1")

	if allowed, _ := l.Allowed("192.0.2.1"); allowed {
		t.Error("触发上限的来源应被拦截")
	}
	if allowed, _ := l.Allowed("192.0.2.2"); !allowed {
		t.Error("其他来源不应受影响")
	}
}

func TestLimiterReset(t *testing.T) {
	l := NewLimiter(1, time.Minute)
	const key = "192.0.2.1"

	l.Fail(key)
	l.Reset(key)

	if allowed, _ := l.Allowed(key); !allowed {
		t.Error("重置后应恢复可尝试状态")
	}
}

func TestLimiterWindowExpiry(t *testing.T) {
	l := NewLimiter(1, time.Millisecond)
	const key = "192.0.2.1"

	l.Fail(key)
	time.Sleep(5 * time.Millisecond)

	if allowed, _ := l.Allowed(key); !allowed {
		t.Error("时间窗过期后应恢复可尝试状态")
	}
}

func TestRandomTokenUniqueness(t *testing.T) {
	seen := make(map[string]bool, 100)

	for i := 0; i < 100; i++ {
		token, err := RandomToken(16)
		if err != nil {
			t.Fatal(err)
		}
		if len(token) != 32 {
			t.Fatalf("16 字节应编码为 32 个十六进制字符，实际 %d", len(token))
		}
		if seen[token] {
			t.Fatal("随机令牌出现重复")
		}
		seen[token] = true
	}
}
