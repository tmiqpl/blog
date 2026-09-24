package captcha

import (
	"bytes"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(5*time.Minute, DefaultOptions())
}

func mustGenerate(t *testing.T, m *Manager) *Challenge {
	t.Helper()

	c, err := m.Generate()
	if err != nil {
		t.Fatalf("生成验证码失败: %v", err)
	}
	return c
}

func TestGenerateProducesValidPNG(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	if len(c.Image) == 0 {
		t.Fatal("图片数据为空")
	}
	if len(c.Code) != DefaultOptions().Length {
		t.Errorf("验证码长度 = %d，期望 %d", len(c.Code), DefaultOptions().Length)
	}
	if len(c.ID) != 32 {
		t.Errorf("标识应为 32 个十六进制字符，实际 %d", len(c.ID))
	}

	img, err := png.Decode(bytes.NewReader(c.Image))
	if err != nil {
		t.Fatalf("PNG 解码失败: %v", err)
	}

	opts := DefaultOptions()
	if b := img.Bounds(); b.Dx() != opts.Width || b.Dy() != opts.Height {
		t.Errorf("图片尺寸 = %dx%d，期望 %dx%d", b.Dx(), b.Dy(), opts.Width, opts.Height)
	}
}

func TestGenerateCodeUsesCharSet(t *testing.T) {
	m := newTestManager(t)
	set := DefaultOptions().CharSet

	for i := 0; i < 50; i++ {
		c := mustGenerate(t, m)
		for _, ch := range c.Code {
			if !strings.ContainsRune(set, ch) {
				t.Fatalf("验证码 %q 含字符集外的字符 %q", c.Code, ch)
			}
		}
	}
}

func TestGenerateProducesDifferentCodes(t *testing.T) {
	m := newTestManager(t)

	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		seen[mustGenerate(t, m).Code] = true
	}
	// 4 位字符集共 25^4 ≈ 39 万种，100 次里出现大量重复说明随机源有问题
	if len(seen) < 80 {
		t.Errorf("100 次生成只有 %d 个不同结果，随机性不足", len(seen))
	}
}

func TestGenerateProducesDifferentImages(t *testing.T) {
	m := newTestManager(t)

	first := mustGenerate(t, m)
	second := mustGenerate(t, m)

	// 即使答案偶然相同，噪点与旋转也应让图片不同
	if bytes.Equal(first.Image, second.Image) {
		t.Error("两次生成的图片完全相同，说明噪点未生效")
	}
}

func TestVerifyCorrectAnswer(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	if !m.Verify(c.ID, c.Code) {
		t.Error("正确答案应校验通过")
	}
}

func TestVerifyIsCaseAndSpaceInsensitive(t *testing.T) {
	cases := []string{
		"  %s  ",
		"%s",
	}

	for _, format := range cases {
		m := newTestManager(t)
		c := mustGenerate(t, m)

		input := strings.ToLower(c.Code) // 同时测小写
		if !m.Verify(c.ID, strings.TrimSpace(strings.ReplaceAll(format, "%s", input))) {
			t.Errorf("输入 %q 应校验通过（大小写与空格不应影响结果）", input)
		}
	}
}

func TestVerifyRejectsWrongAnswer(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	if m.Verify(c.ID, c.Code+"X") {
		t.Error("错误答案不应校验通过")
	}
}

func TestVerifyIsSingleUse(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	if !m.Verify(c.ID, c.Code) {
		t.Fatal("首次校验应通过")
	}
	if m.Verify(c.ID, c.Code) {
		t.Error("同一个验证码不应被重复使用")
	}
}

func TestVerifyConsumesEvenOnFailure(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	if m.Verify(c.ID, "WRONG") {
		t.Fatal("错误答案不应通过")
	}
	// 失败后立即作废，防止对同一张图暴力试错
	if m.Verify(c.ID, c.Code) {
		t.Error("校验失败后该验证码也应作废")
	}
}

func TestVerifyRejectsUnknownOrEmpty(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	if m.Verify("", c.Code) {
		t.Error("空标识不应通过")
	}
	if m.Verify("不存在的标识", c.Code) {
		t.Error("未知标识不应通过")
	}
	if m.Verify(c.ID, "") {
		t.Error("空答案不应通过")
	}
	if m.Verify(c.ID, "   ") {
		t.Error("纯空格答案不应通过")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	m := NewManager(time.Millisecond, DefaultOptions())
	c := mustGenerate(t, m)

	time.Sleep(5 * time.Millisecond)

	if m.Verify(c.ID, c.Code) {
		t.Error("过期验证码不应通过")
	}
}

func TestCountAndGC(t *testing.T) {
	m := NewManager(time.Millisecond, DefaultOptions())

	for i := 0; i < 5; i++ {
		mustGenerate(t, m)
	}
	if n := m.Count(); n != 5 {
		t.Errorf("应有 5 个待校验验证码，实际 %d", n)
	}

	time.Sleep(5 * time.Millisecond)

	if removed := m.GC(); removed != 5 {
		t.Errorf("应清理 5 个过期验证码，实际 %d", removed)
	}
	if n := m.Count(); n != 0 {
		t.Errorf("清理后应为 0，实际 %d", n)
	}
}

func TestCapacityEviction(t *testing.T) {
	m := newTestManager(t)

	// 塞满到上限
	for i := 0; i < maxChallenges; i++ {
		if _, err := m.Generate(); err != nil {
			t.Fatalf("第 %d 次生成失败: %v", i, err)
		}
	}

	// 超出上限后应淘汰旧数据而不是报错或无限增长
	c, err := m.Generate()
	if err != nil {
		t.Fatalf("达到上限后生成失败: %v", err)
	}
	if n := m.Count(); n > maxChallenges {
		t.Errorf("数量 %d 超过上限 %d", n, maxChallenges)
	}
	if !m.Verify(c.ID, c.Code) {
		t.Error("新生成的验证码应仍然可用")
	}
}

func TestOptionsFallbackToDefaults(t *testing.T) {
	m := NewManager(0, Options{})
	c := mustGenerate(t, m)

	img, err := png.Decode(bytes.NewReader(c.Image))
	if err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	if b := img.Bounds(); b.Dx() != opts.Width || b.Dy() != opts.Height {
		t.Errorf("零值配置应回退到默认尺寸 %dx%d，实际 %dx%d", opts.Width, opts.Height, b.Dx(), b.Dy())
	}
	if len(c.Code) != opts.Length {
		t.Errorf("零值配置应回退到默认长度 %d，实际 %d", opts.Length, len(c.Code))
	}
}

func TestCustomOptions(t *testing.T) {
	m := NewManager(time.Minute, Options{Width: 240, Height: 80, Length: 6, CharSet: "AB"})
	c := mustGenerate(t, m)

	if len(c.Code) != 6 {
		t.Errorf("长度 = %d，期望 6", len(c.Code))
	}
	for _, ch := range c.Code {
		if ch != 'A' && ch != 'B' {
			t.Errorf("字符 %q 不在自定义字符集中", ch)
		}
	}

	img, _ := png.Decode(bytes.NewReader(c.Image))
	if b := img.Bounds(); b.Dx() != 240 || b.Dy() != 80 {
		t.Errorf("尺寸 = %dx%d，期望 240x80", b.Dx(), b.Dy())
	}
}

func TestFontCoversDefaultCharSet(t *testing.T) {
	for _, ch := range DefaultOptions().CharSet {
		if _, ok := font[ch]; !ok {
			t.Errorf("字符集包含 %q，但点阵字模中没有该字形", ch)
		}
	}
}

func TestImageHasVisibleInk(t *testing.T) {
	m := newTestManager(t)
	c := mustGenerate(t, m)

	img, err := png.Decode(bytes.NewReader(c.Image))
	if err != nil {
		t.Fatal(err)
	}

	// 抗锯齿会让大量边缘像素轻微偏离背景色，因此只统计「差异明显」的像素。
	const threshold = 70
	bgLuma := luminance(colorBackground)

	ink, total := 0, 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			total++
			r, g, bl, _ := img.At(x, y).RGBA()
			c := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 0xFF}

			diff := int(luminance(c)) - int(bgLuma)
			if diff < 0 {
				diff = -diff
			}
			if diff > threshold {
				ink++
			}
		}
	}

	ratio := float64(ink) / float64(total)
	if ratio < 0.04 {
		t.Errorf("明显的笔画像素仅占 %.1f%%，图像可能是空白的", ratio*100)
	}
	if ratio > 0.75 {
		t.Errorf("明显的笔画像素占 %.1f%%，噪点过多可能影响可读性", ratio*100)
	}
}

// luminance 用感知亮度公式计算灰度值，用于判断像素与背景的差异程度。
func luminance(c color.RGBA) uint8 {
	return uint8((299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000)
}

func TestNormalizeAnswer(t *testing.T) {
	cases := map[string]string{
		" abcd ": "ABCD",
		"AbCd":   "ABCD",
		"":       "",
		"  ":     "",
	}
	for input, want := range cases {
		if got := normalizeAnswer(input); got != want {
			t.Errorf("normalizeAnswer(%q) = %q，期望 %q", input, got, want)
		}
	}
}
