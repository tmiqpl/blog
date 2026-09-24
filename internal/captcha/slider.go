package captcha

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	mrand "math/rand/v2"
	"sync"
	"time"
)

// sliderSupersample 是拼图与背景的绘制倍率，降采样后边缘更平滑。
const sliderSupersample = 2

// 滑块拼图的校验错误，均对应一句可直接展示给用户的提示。
var (
	ErrChallengeNotFound  = errors.New("验证码已失效，请刷新后重试")
	ErrPositionMismatch   = errors.New("拼图未对齐，请重试")
	ErrBehaviorSuspicious = errors.New("操作轨迹异常，请手动拖动滑块重试")
)

// SliderOptions 控制滑块拼图的尺寸与判定宽严。
type SliderOptions struct {
	Width       int // 背景图宽度，同时也是滑块轨道的宽度
	Height      int // 背景图高度
	PieceSize   int // 拼图块边长
	Tolerance   int // 允许的水平误差（像素）
	MaxAttempts int // 单次挑战允许的尝试次数
}

// DefaultSliderOptions 返回一组默认参数。
// 容差取 ±5px：320px 宽的背景里横向有约 270 个落点，容差过大会让盲猜成功率上升。
func DefaultSliderOptions() SliderOptions {
	return SliderOptions{
		Width:       320,
		Height:      160,
		PieceSize:   48,
		Tolerance:   5,
		MaxAttempts: 3,
	}
}

func (o SliderOptions) withDefaults() SliderOptions {
	d := DefaultSliderOptions()
	if o.Width <= 0 {
		o.Width = d.Width
	}
	if o.Height <= 0 {
		o.Height = d.Height
	}
	if o.PieceSize <= 0 || o.PieceSize >= o.Width/2 {
		o.PieceSize = d.PieceSize
	}
	if o.Tolerance <= 0 {
		o.Tolerance = d.Tolerance
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = d.MaxAttempts
	}
	return o
}

// 滑块挑战中两张图片的 MIME 类型。
// 底图用 JPEG 换体积，拼图块必须用 PNG 才能保留透明区域。
const (
	BackgroundMIME = "image/jpeg"
	PieceMIME      = "image/png"
)

// SliderChallenge 是一次滑块拼图挑战。
type SliderChallenge struct {
	Token      string
	Background []byte // 已挖去缺口并压暗的底图（JPEG）
	Piece      []byte // 抠出的拼图块，带透明背景（PNG）
	Y          int    // 拼图块在底图中的纵向位置
	Width      int
	Height     int
	PieceSize  int
}

// Behavior 是前端上报的拖动行为特征。
type Behavior struct {
	DurationMS int
	Track      []int // 拖动过程中采样到的横向位置
}

type sliderEntry struct {
	targetX   int
	attempts  int
	expiresAt time.Time
	seq       uint64
}

type ticketEntry struct {
	clientKey string
	expiresAt time.Time
}

// SliderManager 负责生成与校验滑块拼图，全部状态保存在内存中。
type SliderManager struct {
	mu        sync.Mutex
	items     map[string]*sliderEntry
	tickets   map[string]ticketEntry
	ttl       time.Duration
	ticketTTL time.Duration
	opts      SliderOptions
	seq       uint64
}

// NewSliderManager 创建滑块拼图管理器。
func NewSliderManager(ttl, ticketTTL time.Duration, opts SliderOptions) *SliderManager {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if ticketTTL <= 0 {
		ticketTTL = 3 * time.Minute
	}
	return &SliderManager{
		items:     make(map[string]*sliderEntry),
		tickets:   make(map[string]ticketEntry),
		ttl:       ttl,
		ticketTTL: ticketTTL,
		opts:      opts.withDefaults(),
	}
}

// Options 返回生效的配置，供前端布局使用。
func (m *SliderManager) Options() SliderOptions { return m.opts }

// TTL 返回单个挑战的有效期。
func (m *SliderManager) TTL() time.Duration { return m.ttl }

// Generate 生成一次新的滑块拼图挑战。
func (m *SliderManager) Generate() (*SliderChallenge, error) {
	background, piece, targetX, pieceY, err := renderSlider(m.opts)
	if err != nil {
		return nil, err
	}

	bgData, err := encodeJPEG(background, 80)
	if err != nil {
		return nil, fmt.Errorf("生成背景图失败: %w", err)
	}
	pieceData, err := encodePNG(piece)
	if err != nil {
		return nil, fmt.Errorf("生成拼图块失败: %w", err)
	}

	token, err := randomID()
	if err != nil {
		return nil, err
	}
	if err := m.put(token, targetX); err != nil {
		return nil, err
	}

	return &SliderChallenge{
		Token:      token,
		Background: bgData,
		Piece:      pieceData,
		Y:          pieceY,
		Width:      m.opts.Width,
		Height:     m.opts.Height,
		PieceSize:  m.opts.PieceSize,
	}, nil
}

// Verify 校验滑块位置与拖动行为。成功后签发一张短期票据，供登录时兑换。
// 每次调用都会消耗一次尝试机会，失败达上限后该挑战立即作废。
func (m *SliderManager) Verify(token string, x int, b Behavior, clientKey string) (string, error) {
	m.mu.Lock()
	entry, ok := m.items[token]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(m.items, token)
		m.mu.Unlock()
		return "", ErrChallengeNotFound
	}

	entry.attempts++
	exhausted := entry.attempts >= m.opts.MaxAttempts
	if exhausted {
		delete(m.items, token)
	}
	m.mu.Unlock()

	// 先看行为特征：明显是脚本一次性跳到终点的，直接拒绝
	if err := checkBehavior(b, m.opts); err != nil {
		return "", err
	}

	diff := x - entry.targetX
	if diff < 0 {
		diff = -diff
	}
	if diff > m.opts.Tolerance {
		return "", ErrPositionMismatch
	}

	ticket, err := randomID()
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	m.tickets[ticket] = ticketEntry{
		clientKey: clientKey,
		expiresAt: time.Now().Add(m.ticketTTL),
	}
	// 顺手清理过期票据，避免长期运行后无限增长
	now := time.Now()
	for id, t := range m.tickets {
		if now.After(t.expiresAt) {
			delete(m.tickets, id)
		}
	}
	m.mu.Unlock()

	return ticket, nil
}

// RedeemTicket 兑换票据，成功即作废。票据一次性使用，且绑定到发起校验的来源。
func (m *SliderManager) RedeemTicket(ticket, clientKey string) bool {
	if ticket == "" {
		return false
	}

	m.mu.Lock()
	t, ok := m.tickets[ticket]
	delete(m.tickets, ticket) // 一次性消费，无论后续校验是否通过
	m.mu.Unlock()

	if !ok || time.Now().After(t.expiresAt) {
		return false
	}
	if clientKey != "" && t.clientKey != "" {
		return subtle.ConstantTimeCompare([]byte(t.clientKey), []byte(clientKey)) == 1
	}
	return true
}

// Count 返回当前待校验的挑战数量。
func (m *SliderManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// GC 清理过期的挑战与票据，返回清理总数。
func (m *SliderManager) GC() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, e := range m.items {
		if now.After(e.expiresAt) {
			delete(m.items, id)
			removed++
		}
	}
	for id, t := range m.tickets {
		if now.After(t.expiresAt) {
			delete(m.tickets, id)
			removed++
		}
	}
	return removed
}

// put 保存一个挑战，超出容量上限时先清过期再淘汰最早的。调用方无需持锁。
func (m *SliderManager) put(token string, targetX int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.items) >= maxChallenges {
		now := time.Now()
		for id, e := range m.items {
			if now.After(e.expiresAt) {
				delete(m.items, id)
			}
		}
		for len(m.items) >= maxChallenges {
			threshold := m.seq
			if threshold > uint64(maxChallenges/4) {
				threshold -= uint64(maxChallenges / 4)
			} else {
				threshold = 0
			}
			dropped := 0
			for id, e := range m.items {
				if e.seq <= threshold {
					delete(m.items, id)
					dropped++
				}
			}
			if dropped == 0 {
				for id := range m.items {
					delete(m.items, id)
					dropped++
					if dropped >= maxChallenges/4 {
						break
					}
				}
			}
		}
	}

	m.seq++
	m.items[token] = &sliderEntry{
		targetX:   targetX,
		expiresAt: time.Now().Add(m.ttl),
		seq:       m.seq,
	}
	return nil
}

/* ---------------------------- 行为特征校验 ---------------------------- */

// 拖动时长与采样点数量的合理区间。
// 人类拖动再快也要 300ms 以上，再慢也很少超过 20 秒；采样点太少说明是「瞬移」。
const (
	minDragDurationMS = 300
	maxDragDurationMS = 20_000
	minTrackPoints    = 5
)

// checkBehavior 判断拖动轨迹是否像真人操作。
// 这类校验只能拦住「直接发请求」的低成本脚本，真正的防线仍是拼图位置本身不可预知。
func checkBehavior(b Behavior, opts SliderOptions) error {
	if b.DurationMS < minDragDurationMS || b.DurationMS > maxDragDurationMS {
		return ErrBehaviorSuspicious
	}
	if len(b.Track) < minTrackPoints {
		return ErrBehaviorSuspicious
	}

	// 总位移应当与背景宽度同量级，否则说明上报的数据自相矛盾
	span := b.Track[len(b.Track)-1] - b.Track[0]
	if span < opts.Width/4 {
		return ErrBehaviorSuspicious
	}

	// 单步位移不能超过全程的一半：脚本常表现为一步跳到终点
	maxStep := 0
	for i := 1; i < len(b.Track); i++ {
		step := b.Track[i] - b.Track[i-1]
		if step < 0 {
			step = -step
		}
		if step > maxStep {
			maxStep = step
		}
	}
	if maxStep*2 > span {
		return ErrBehaviorSuspicious
	}

	return nil
}

/* ------------------------------ 图像生成 ------------------------------ */

// renderSlider 生成背景图与拼图块，返回的坐标已换算到最终（降采样后）的像素空间。
func renderSlider(opts SliderOptions) (background, piece *image.RGBA, targetX, pieceY int, err error) {
	ss := sliderSupersample
	w, h := opts.Width*ss, opts.Height*ss

	canvas := image.NewRGBA(image.Rect(0, 0, w, h))
	drawSliderBackground(canvas)

	// 尺寸都取偶数，保证降采样后不会出现半像素误差
	size := opts.PieceSize * ss
	knob := int(math.Round(float64(size) * 0.2))
	if knob%2 != 0 {
		knob++
	}
	pieceH := size + knob

	// 目标横向位置至少留出 35% 宽度的拖动距离，避免答案就落在起点附近
	minX := int(float64(w) * 0.35)
	maxX := w - size - 4*ss
	if maxX <= minX {
		maxX = minX + 1
	}
	tx := minX + mrand.IntN(maxX-minX)

	minY := 4 * ss
	maxY := h - pieceH - 4*ss
	if maxY <= minY {
		maxY = minY + 1
	}
	ty := minY + mrand.IntN(maxY-minY)

	pieceHi := cutPiece(canvas, tx, ty, size, knob)
	punchHole(canvas, tx, ty, size, knob)

	return downsample(canvas, ss), downsampleAlpha(pieceHi, ss), tx / ss, ty / ss, nil
}

// insidePiece 判断拼图块自身坐标系中的某点是否属于拼图形状。
// 形状 = 正方形主体 + 顶边中点的圆形凸起。
func insidePiece(px, py, size, knob int) bool {
	dx := float64(px) - float64(size)/2
	dy := float64(py) - float64(knob)
	if dx*dx+dy*dy <= float64(knob*knob) {
		return true
	}
	return px >= 0 && px < size && py >= knob && py < knob+size
}

// cutPiece 从底图上抠出拼图块，非形状区域保持透明。
func cutPiece(src *image.RGBA, x, y, size, knob int) *image.RGBA {
	piece := image.NewRGBA(image.Rect(0, 0, size, size+knob))

	for py := 0; py < size+knob; py++ {
		for px := 0; px < size; px++ {
			if !insidePiece(px, py, size, knob) {
				continue
			}
			sx, sy := x+px, y+py
			if !(image.Point{X: sx, Y: sy}).In(src.Rect) {
				continue
			}
			c := src.RGBAAt(sx, sy)
			piece.SetRGBA(px, py, color.RGBA{R: c.R, G: c.G, B: c.B, A: 255})
		}
	}
	return piece
}

// punchHole 在底图上把拼图块所在位置压暗，形成「缺口」的观感。
func punchHole(dst *image.RGBA, x, y, size, knob int) {
	for py := 0; py < size+knob; py++ {
		for px := 0; px < size; px++ {
			if !insidePiece(px, py, size, knob) {
				continue
			}
			sx, sy := x+px, y+py
			if !(image.Point{X: sx, Y: sy}).In(dst.Rect) {
				continue
			}
			c := dst.RGBAAt(sx, sy)
			dst.SetRGBA(sx, sy, color.RGBA{
				R: uint8(int(c.R) * 32 / 100),
				G: uint8(int(c.G) * 32 / 100),
				B: uint8(int(c.B) * 32 / 100),
				A: 255,
			})
		}
	}
}

// drawSliderBackground 程序化生成一张有足够局部纹理的底图。
// 纯渐变太平滑，用户无法判断拼图块该落在哪里，所以叠了若干半透明圆形与细圆环。
//
// 这里刻意少用逐像素噪点：噪点几乎不可压缩，会让 PNG 体积膨胀数倍，
// 而平滑图形同样能提供足够的局部纹理。
func drawSliderBackground(dst *image.RGBA) {
	w, h := dst.Rect.Dx(), dst.Rect.Dy()

	base := mrand.Float64() * 360
	from := hslToRGB(base, 0.52, 0.66)
	mid := hslToRGB(math.Mod(base+45, 360), 0.58, 0.50)
	to := hslToRGB(math.Mod(base+100, 360), 0.46, 0.34)

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// 对角渐变，t ∈ [0,1]
			t := (float64(x)/float64(w) + float64(y)/float64(h)) / 2
			var col color.RGBA
			if t < 0.5 {
				col = lerpColor(from, mid, t*2)
			} else {
				col = lerpColor(mid, to, (t-0.5)*2)
			}
			dst.SetRGBA(x, y, col)
		}
	}

	// 半透明圆形色块，制造局部色彩差异
	for i := 0; i < 9; i++ {
		cx := mrand.IntN(w)
		cy := mrand.IntN(h)
		r := h/6 + mrand.IntN(h/3)
		col := hslToRGB(mrand.Float64()*360, 0.62, 0.62)
		drawCircleBlend(dst, cx, cy, r, col, 0.17)
	}

	// 几道细圆环，提供更细的纹理层次
	for i := 0; i < 4; i++ {
		cx := mrand.IntN(w)
		cy := mrand.IntN(h)
		r := h/5 + mrand.IntN(h/2)
		col := hslToRGB(mrand.Float64()*360, 0.45, 0.85)
		drawRingBlend(dst, cx, cy, r, 2*sliderSupersample, col, 0.5)
	}

	// 稀疏噪点，打散大块纯色区域
	for i := 0; i < w*h/90; i++ {
		x := mrand.IntN(w)
		y := mrand.IntN(h)
		c := dst.RGBAAt(x, y)
		delta := mrand.IntN(13) - 6
		dst.SetRGBA(x, y, color.RGBA{
			R: clampUint8(int(c.R) + delta),
			G: clampUint8(int(c.G) + delta),
			B: clampUint8(int(c.B) + delta),
			A: 255,
		})
	}
}

// drawRingBlend 画一个指定线宽的半透明圆环。
func drawRingBlend(dst *image.RGBA, cx, cy, radius, thickness int, col color.RGBA, alpha float64) {
	outer := radius + thickness/2
	inner := radius - thickness/2
	outer2, inner2 := outer*outer, inner*inner

	for y := cy - outer; y <= cy+outer; y++ {
		for x := cx - outer; x <= cx+outer; x++ {
			dx, dy := x-cx, y-cy
			d2 := dx*dx + dy*dy
			if d2 > outer2 || d2 < inner2 {
				continue
			}
			blend(dst, x, y, col, alpha)
		}
	}
}

func drawCircleBlend(dst *image.RGBA, cx, cy, r int, col color.RGBA, alpha float64) {
	r2 := r * r
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy > r2 {
				continue
			}
			blend(dst, x, y, col, alpha)
		}
	}
}

// downsampleAlpha 与 downsample 类似，但保留 alpha 通道，用于带透明背景的拼图块。
// 先按 alpha 加权再平均，否则半透明边缘会被黑色稀释出黑边。
func downsampleAlpha(src *image.RGBA, ss int) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()/ss, b.Dy()/ss))
	samples := uint32(ss * ss)

	for y := 0; y < dst.Rect.Dy(); y++ {
		for x := 0; x < dst.Rect.Dx(); x++ {
			var sumR, sumG, sumB, sumA uint32
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := src.RGBAAt(x*ss+dx, y*ss+dy)
					a := uint32(c.A)
					sumR += uint32(c.R) * a / 255
					sumG += uint32(c.G) * a / 255
					sumB += uint32(c.B) * a / 255
					sumA += a
				}
			}

			outA := sumA / samples
			var r, g, bl uint8
			if sumA > 0 {
				r = clampUint8(int(sumR * 255 / sumA))
				g = clampUint8(int(sumG * 255 / sumA))
				bl = clampUint8(int(sumB * 255 / sumA))
			}
			dst.SetRGBA(x, y, color.RGBA{R: r, G: g, B: bl, A: uint8(outA)})
		}
	}
	return dst
}

/* ------------------------------ 颜色工具 ------------------------------ */

func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	mix := func(x, y uint8) uint8 {
		return clampUint8(int(float64(x)*(1-t) + float64(y)*t + 0.5))
	}
	return color.RGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: 255}
}

func clampUint8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// hslToRGB 把 HSL 转换为 RGB，方便按色相生成协调的随机配色。
func hslToRGB(hue, sat, light float64) color.RGBA {
	h := math.Mod(math.Mod(hue, 360)+360, 360) / 360

	var r, g, b float64
	if sat == 0 {
		r, g, b = light, light, light
	} else {
		var q float64
		if light < 0.5 {
			q = light * (1 + sat)
		} else {
			q = light + sat - light*sat
		}
		p := 2*light - q
		r = hueToChannel(p, q, h+1.0/3)
		g = hueToChannel(p, q, h)
		b = hueToChannel(p, q, h-1.0/3)
	}

	return color.RGBA{
		R: clampUint8(int(r*255 + 0.5)),
		G: clampUint8(int(g*255 + 0.5)),
		B: clampUint8(int(b*255 + 0.5)),
		A: 255,
	}
}

func hueToChannel(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6:
		return p + (q-p)*6*t
	case t < 1.0/2:
		return q
	case t < 2.0/3:
		return p + (q-p)*(2.0/3-t)*6
	default:
		return p
	}
}
