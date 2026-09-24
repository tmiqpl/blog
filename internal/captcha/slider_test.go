package captcha

import (
	"bytes"
	"image/jpeg"
	"image/png"
	"testing"
	"time"
)

func newTestSlider(t *testing.T) *SliderManager {
	t.Helper()
	return NewSliderManager(5*time.Minute, 3*time.Minute, DefaultSliderOptions())
}

func mustGenerateSlider(t *testing.T, m *SliderManager) *SliderChallenge {
	t.Helper()

	c, err := m.Generate()
	if err != nil {
		t.Fatalf("生成滑块挑战失败: %v", err)
	}
	return c
}

// targetXOf 直接读取内部状态拿到正确答案。白盒测试，仅用于验证判定逻辑。
func targetXOf(t *testing.T, m *SliderManager, token string) int {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.items[token]
	if !ok {
		t.Fatal("挑战不存在，可能已过期或被消费")
	}
	return e.targetX
}

// dragTo 构造一段看起来像真人拖动的行为数据。
func dragTo(target int) Behavior {
	const steps = 16

	track := make([]int, 0, steps+1)
	for i := 0; i <= steps; i++ {
		track = append(track, target*i/steps)
	}
	return Behavior{DurationMS: 900, Track: track}
}

/* ------------------------------ 生成 ------------------------------ */

func TestSliderGenerateProducesValidImages(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)

	if c.Token == "" {
		t.Fatal("令牌不应为空")
	}
	if len(c.Background) == 0 || len(c.Piece) == 0 {
		t.Fatal("背景图与拼图块都不应为空")
	}

	// 底图是 JPEG，拼图块是 PNG
	bg, err := jpeg.Decode(bytes.NewReader(c.Background))
	if err != nil {
		t.Fatalf("背景图解码失败: %v", err)
	}
	if b := bg.Bounds(); b.Dx() != c.Width || b.Dy() != c.Height {
		t.Errorf("背景图尺寸 = %dx%d，期望 %dx%d", b.Dx(), b.Dy(), c.Width, c.Height)
	}

	piece, err := png.Decode(bytes.NewReader(c.Piece))
	if err != nil {
		t.Fatalf("拼图块解码失败: %v", err)
	}
	pb := piece.Bounds()
	if pb.Dx() != c.PieceSize {
		t.Errorf("拼图块宽度 = %d，期望 %d", pb.Dx(), c.PieceSize)
	}
	// 高度 = 边长 + 顶部圆形凸起，允许一定浮动
	if pb.Dy() <= c.PieceSize || pb.Dy() > c.PieceSize*3/2 {
		t.Errorf("拼图块高度 = %d，应略大于边长 %d", pb.Dy(), c.PieceSize)
	}
}

func TestSliderPieceShapeAlpha(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)

	piece, err := png.Decode(bytes.NewReader(c.Piece))
	if err != nil {
		t.Fatal(err)
	}

	b := piece.Bounds()
	alphaAt := func(x, y int) uint8 {
		_, _, _, a := piece.At(x, y).RGBA()
		return uint8(a >> 8)
	}

	// 形状 = 顶边中点的圆形凸起 + 下方正方形主体
	// 因此上方两角在形状之外（透明），下方两角属于正方形（不透明）
	if got := alphaAt(b.Min.X, b.Min.Y); got != 0 {
		t.Errorf("左上角应透明，实际 alpha=%d", got)
	}
	if got := alphaAt(b.Max.X-1, b.Min.Y); got != 0 {
		t.Errorf("右上角应透明，实际 alpha=%d", got)
	}
	if got := alphaAt(b.Min.X, b.Max.Y-1); got != 255 {
		t.Errorf("左下角属于正方形主体，应不透明，实际 alpha=%d", got)
	}
	if got := alphaAt(b.Max.X-1, b.Max.Y-1); got != 255 {
		t.Errorf("右下角属于正方形主体，应不透明，实际 alpha=%d", got)
	}

	// 凸起顶点位于顶部正中。这里是抗锯齿边缘，只要求有覆盖（alpha > 0）
	midX := b.Min.X + b.Dx()/2
	if got := alphaAt(midX, b.Min.Y); got == 0 {
		t.Errorf("圆形凸起顶点应有覆盖，实际 alpha=%d", got)
	}
	// 往下几个像素应已完全落在圆内
	if got := alphaAt(midX, b.Min.Y+3); got != 255 {
		t.Errorf("圆形凸起内部应不透明，实际 alpha=%d", got)
	}

	// 整体应同时存在透明与不透明像素
	var transparent, opaque int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if alphaAt(x, y) == 0 {
				transparent++
			} else {
				opaque++
			}
		}
	}
	if transparent == 0 {
		t.Error("拼图块应当存在透明区域")
	}
	if opaque == 0 {
		t.Error("拼图块应当存在不透明区域")
	}
	// 正方形占了绝大部分面积，透明区域只应出现在顶部两角
	if transparent > b.Dx()*b.Dy()/3 {
		t.Errorf("透明像素过多（%d/%d），形状可能不对", transparent, b.Dx()*b.Dy())
	}
}

func TestSliderPieceIsOpaqueAtCenter(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)

	piece, err := png.Decode(bytes.NewReader(c.Piece))
	if err != nil {
		t.Fatal(err)
	}

	// 正方形主体的中心必定属于拼图块
	b := piece.Bounds()
	cx := b.Min.X + b.Dx()/2
	cy := b.Min.Y + b.Dy()*3/4
	_, _, _, a := piece.At(cx, cy).RGBA()
	if a>>8 != 255 {
		t.Errorf("主体中心应完全不透明，实际 alpha=%d", a>>8)
	}
}

func TestSliderYWithinBounds(t *testing.T) {
	m := newTestSlider(t)

	for i := 0; i < 30; i++ {
		c := mustGenerateSlider(t, m)
		if c.Y < 0 || c.Y+c.PieceSize > c.Height {
			t.Fatalf("拼图块纵向位置 %d 超出背景高度 %d", c.Y, c.Height)
		}
	}
}

func TestSliderTargetLeavesRoomToDrag(t *testing.T) {
	m := newTestSlider(t)

	opts := m.Options()
	for i := 0; i < 30; i++ {
		c := mustGenerateSlider(t, m)
		x := targetXOf(t, m, c.Token)

		if x < 0 || x+opts.PieceSize > opts.Width {
			t.Fatalf("目标位置 %d 超出可用范围", x)
		}
		if x < opts.Width/4 {
			t.Fatalf("目标位置 %d 太靠近起点，拖动距离过短", x)
		}
	}
}

func TestSliderGeneratesDifferentImages(t *testing.T) {
	m := newTestSlider(t)

	a := mustGenerateSlider(t, m)
	b := mustGenerateSlider(t, m)

	if bytes.Equal(a.Background, b.Background) {
		t.Error("两次生成的背景图完全相同")
	}
}

/* ------------------------------ 校验 ------------------------------ */

func TestSliderVerifySuccess(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	ticket, err := m.Verify(c.Token, target, dragTo(target), "192.0.2.1")
	if err != nil {
		t.Fatalf("位置正确时应校验通过，实际: %v", err)
	}
	if ticket == "" {
		t.Fatal("校验通过应签发票据")
	}
}

func TestSliderVerifyWithinTolerance(t *testing.T) {
	m := newTestSlider(t)
	opts := m.Options()

	for _, delta := range []int{-opts.Tolerance, opts.Tolerance} {
		c := mustGenerateSlider(t, m)
		target := targetXOf(t, m, c.Token)

		if _, err := m.Verify(c.Token, target+delta, dragTo(target), "192.0.2.1"); err != nil {
			t.Errorf("偏差 %d px 在容差内，应通过，实际: %v", delta, err)
		}
	}
}

func TestSliderVerifyOutsideTolerance(t *testing.T) {
	m := newTestSlider(t)
	opts := m.Options()

	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	if _, err := m.Verify(c.Token, target+opts.Tolerance+1, dragTo(target), "192.0.2.1"); err != ErrPositionMismatch {
		t.Errorf("超出容差应返回 ErrPositionMismatch，实际: %v", err)
	}
}

func TestSliderVerifyRejectsUnknownToken(t *testing.T) {
	m := newTestSlider(t)

	if _, err := m.Verify("不存在的令牌", 100, dragTo(100), "192.0.2.1"); err != ErrChallengeNotFound {
		t.Errorf("未知令牌应返回 ErrChallengeNotFound，实际: %v", err)
	}
}

func TestSliderVerifyExhaustsAttempts(t *testing.T) {
	m := newTestSlider(t)
	opts := m.Options()

	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	// 前 MaxAttempts-1 次失败仍然保留挑战
	for i := 0; i < opts.MaxAttempts-1; i++ {
		if _, err := m.Verify(c.Token, target+50, dragTo(target), "192.0.2.1"); err != ErrPositionMismatch {
			t.Fatalf("第 %d 次失败应返回位置不匹配，实际: %v", i+1, err)
		}
	}

	// 达到上限后挑战作废
	if _, err := m.Verify(c.Token, target+50, dragTo(target), "192.0.2.1"); err != ErrPositionMismatch {
		t.Fatalf("最后一次失败仍应返回位置不匹配，实际: %v", err)
	}
	if _, err := m.Verify(c.Token, target, dragTo(target), "192.0.2.1"); err != ErrChallengeNotFound {
		t.Errorf("尝试次数用尽后挑战应作废，实际: %v", err)
	}
}

func TestSliderVerifyRejectsExpired(t *testing.T) {
	m := NewSliderManager(time.Millisecond, time.Minute, DefaultSliderOptions())
	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	time.Sleep(5 * time.Millisecond)

	if _, err := m.Verify(c.Token, target, dragTo(target), "192.0.2.1"); err != ErrChallengeNotFound {
		t.Errorf("过期挑战应返回 ErrChallengeNotFound，实际: %v", err)
	}
}

/* ---------------------------- 行为特征 ---------------------------- */

func TestSliderBehaviorChecks(t *testing.T) {
	opts := DefaultSliderOptions()

	valid := dragTo(200)

	cases := []struct {
		name string
		b    Behavior
		want error
	}{
		{"正常拖动", valid, nil},
		{"拖动过快", Behavior{DurationMS: 50, Track: valid.Track}, ErrBehaviorSuspicious},
		{"拖动过慢", Behavior{DurationMS: 60_000, Track: valid.Track}, ErrBehaviorSuspicious},
		{"采样点过少", Behavior{DurationMS: 900, Track: []int{0, 200}}, ErrBehaviorSuspicious},
		{"没有轨迹", Behavior{DurationMS: 900}, ErrBehaviorSuspicious},
		{
			name: "一步跳到终点",
			b:    Behavior{DurationMS: 900, Track: []int{0, 1, 2, 3, 4, 200}},
			want: ErrBehaviorSuspicious,
		},
		{
			name: "总位移过小",
			b:    Behavior{DurationMS: 900, Track: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}},
			want: ErrBehaviorSuspicious,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checkBehavior(c.b, opts); got != c.want {
				t.Errorf("checkBehavior() = %v，期望 %v", got, c.want)
			}
		})
	}
}

func TestSliderVerifyRejectsSuspiciousBehavior(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	// 位置正确但轨迹是瞬移，仍应拒绝
	b := Behavior{DurationMS: 900, Track: []int{0, 1, 2, 3, 4, target}}
	if _, err := m.Verify(c.Token, target, b, "192.0.2.1"); err != ErrBehaviorSuspicious {
		t.Errorf("瞬移轨迹应返回 ErrBehaviorSuspicious，实际: %v", err)
	}
}

/* ------------------------------ 票据 ------------------------------ */

func TestSliderTicketSingleUse(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	ticket, err := m.Verify(c.Token, target, dragTo(target), "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}

	if !m.RedeemTicket(ticket, "192.0.2.1") {
		t.Fatal("首次兑换应成功")
	}
	if m.RedeemTicket(ticket, "192.0.2.1") {
		t.Error("票据不应被重复兑换")
	}
}

func TestSliderTicketBindsToClient(t *testing.T) {
	m := newTestSlider(t)
	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	ticket, err := m.Verify(c.Token, target, dragTo(target), "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}

	if m.RedeemTicket(ticket, "198.51.100.7") {
		t.Error("票据绑定到发起校验的来源，其他来源不应能兑换")
	}
}

func TestSliderTicketExpiry(t *testing.T) {
	m := NewSliderManager(time.Minute, time.Millisecond, DefaultSliderOptions())
	c := mustGenerateSlider(t, m)
	target := targetXOf(t, m, c.Token)

	ticket, err := m.Verify(c.Token, target, dragTo(target), "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)

	if m.RedeemTicket(ticket, "192.0.2.1") {
		t.Error("过期票据不应可兑换")
	}
}

func TestSliderRedeemRejectsEmpty(t *testing.T) {
	m := newTestSlider(t)

	if m.RedeemTicket("", "192.0.2.1") {
		t.Error("空票据不应可兑换")
	}
}

/* --------------------------- 生命周期与配置 --------------------------- */

func TestSliderCountAndGC(t *testing.T) {
	m := NewSliderManager(time.Millisecond, time.Millisecond, DefaultSliderOptions())

	for i := 0; i < 4; i++ {
		mustGenerateSlider(t, m)
	}
	if n := m.Count(); n != 4 {
		t.Errorf("应有 4 个待校验挑战，实际 %d", n)
	}

	time.Sleep(5 * time.Millisecond)

	if removed := m.GC(); removed < 4 {
		t.Errorf("应至少清理 4 项，实际 %d", removed)
	}
	if n := m.Count(); n != 0 {
		t.Errorf("清理后应为 0，实际 %d", n)
	}
}

func TestSliderOptionsFallback(t *testing.T) {
	m := NewSliderManager(0, 0, SliderOptions{})
	c := mustGenerateSlider(t, m)

	d := DefaultSliderOptions()
	if c.Width != d.Width || c.Height != d.Height || c.PieceSize != d.PieceSize {
		t.Errorf("零值配置应回退到默认值，实际 %dx%d piece=%d", c.Width, c.Height, c.PieceSize)
	}
	if m.TTL() != 5*time.Minute {
		t.Errorf("TTL 应回退到 5 分钟，实际 %v", m.TTL())
	}
}

func TestSliderCustomOptions(t *testing.T) {
	opts := SliderOptions{Width: 400, Height: 200, PieceSize: 60, Tolerance: 8, MaxAttempts: 2}
	m := NewSliderManager(time.Minute, time.Minute, opts)

	c := mustGenerateSlider(t, m)
	if c.Width != 400 || c.Height != 200 || c.PieceSize != 60 {
		t.Errorf("自定义尺寸未生效: %dx%d piece=%d", c.Width, c.Height, c.PieceSize)
	}

	bg, err := jpeg.Decode(bytes.NewReader(c.Background))
	if err != nil {
		t.Fatal(err)
	}
	if b := bg.Bounds(); b.Dx() != 400 || b.Dy() != 200 {
		t.Errorf("背景图尺寸 = %dx%d，期望 400x200", b.Dx(), b.Dy())
	}

	target := targetXOf(t, m, c.Token)
	if _, err := m.Verify(c.Token, target+7, dragTo(target), "k"); err != nil {
		t.Errorf("自定义容差 8 内应通过，实际: %v", err)
	}
}

func TestInsidePieceShape(t *testing.T) {
	const size, knob = 96, 20

	// 正方形主体内部
	if !insidePiece(48, 60, size, knob) {
		t.Error("正方形主体内部应属于拼图块")
	}
	// 顶部圆形凸起的顶点
	if !insidePiece(48, 0, size, knob) {
		t.Error("圆形凸起顶点应属于拼图块")
	}
	// 左上角在形状之外
	if insidePiece(0, 0, size, knob) {
		t.Error("左上角应不属于拼图块")
	}
	// 底部中间在主体内
	if !insidePiece(48, knob+size-1, size, knob) {
		t.Error("主体底部应属于拼图块")
	}
}

func TestHslToRGB(t *testing.T) {
	cases := []struct {
		h, s, l float64
		want    [3]uint8
	}{
		{0, 1, 0.5, [3]uint8{255, 0, 0}},     // 红
		{120, 1, 0.5, [3]uint8{0, 255, 0}},   // 绿
		{240, 1, 0.5, [3]uint8{0, 0, 255}},   // 蓝
		{0, 0, 0.5, [3]uint8{128, 128, 128}}, // 无饱和度 → 灰
	}

	for _, c := range cases {
		got := hslToRGB(c.h, c.s, c.l)
		if got.R != c.want[0] || got.G != c.want[1] || got.B != c.want[2] {
			t.Errorf("hslToRGB(%v,%v,%v) = (%d,%d,%d)，期望 %v", c.h, c.s, c.l, got.R, got.G, got.B, c.want)
		}
	}
}
