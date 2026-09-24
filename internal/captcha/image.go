package captcha

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"strings"
)

// 点阵字模尺寸：每个字符 5 列 × 7 行。
const (
	glyphCols = 5
	glyphRows = 7
)

// font 是 5×7 点阵字模。每个字符 7 行，每行只用低 5 位，最高位对应最左侧像素。
// 使用手写点阵而非系统字体，是为了让验证码生成完全不依赖外部字体文件，
// 从而保持「编译出的单文件二进制在任何机器上都能跑」这一特性。
var font = map[rune][glyphRows]byte{
	'0': {0b01110, 0b10001, 0b10011, 0b10101, 0b11001, 0b10001, 0b01110},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00001, 0b00010, 0b00100, 0b01000, 0b11111},
	'3': {0b11111, 0b00010, 0b00100, 0b00010, 0b00001, 0b10001, 0b01110},
	'4': {0b00010, 0b00110, 0b01010, 0b10010, 0b11111, 0b00010, 0b00010},
	'5': {0b11111, 0b10000, 0b11110, 0b00001, 0b00001, 0b10001, 0b01110},
	'6': {0b00110, 0b01000, 0b10000, 0b11110, 0b10001, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b01000, 0b01000},
	'8': {0b01110, 0b10001, 0b10001, 0b01110, 0b10001, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b10001, 0b01111, 0b00001, 0b00010, 0b01100},
	'A': {0b01110, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'B': {0b11110, 0b10001, 0b10001, 0b11110, 0b10001, 0b10001, 0b11110},
	'C': {0b01110, 0b10001, 0b10000, 0b10000, 0b10000, 0b10001, 0b01110},
	'D': {0b11100, 0b10010, 0b10001, 0b10001, 0b10001, 0b10010, 0b11100},
	'E': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b11111},
	'F': {0b11111, 0b10000, 0b10000, 0b11110, 0b10000, 0b10000, 0b10000},
	'G': {0b01110, 0b10001, 0b10000, 0b10111, 0b10001, 0b10001, 0b01111},
	'H': {0b10001, 0b10001, 0b10001, 0b11111, 0b10001, 0b10001, 0b10001},
	'I': {0b01110, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b01110},
	'J': {0b00111, 0b00010, 0b00010, 0b00010, 0b00010, 0b10010, 0b01100},
	'K': {0b10001, 0b10010, 0b10100, 0b11000, 0b10100, 0b10010, 0b10001},
	'L': {0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b10000, 0b11111},
	'M': {0b10001, 0b11011, 0b10101, 0b10101, 0b10001, 0b10001, 0b10001},
	'N': {0b10001, 0b10001, 0b11001, 0b10101, 0b10011, 0b10001, 0b10001},
	'O': {0b01110, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'P': {0b11110, 0b10001, 0b10001, 0b11110, 0b10000, 0b10000, 0b10000},
	'Q': {0b01110, 0b10001, 0b10001, 0b10001, 0b10101, 0b10010, 0b01101},
	'R': {0b11110, 0b10001, 0b10001, 0b11110, 0b10100, 0b10010, 0b10001},
	'S': {0b01111, 0b10000, 0b10000, 0b01110, 0b00001, 0b00001, 0b11110},
	'T': {0b11111, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100, 0b00100},
	'U': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01110},
	'V': {0b10001, 0b10001, 0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
	'W': {0b10001, 0b10001, 0b10001, 0b10101, 0b10101, 0b11011, 0b10001},
	'X': {0b10001, 0b10001, 0b01010, 0b00100, 0b01010, 0b10001, 0b10001},
	'Y': {0b10001, 0b10001, 0b01010, 0b00100, 0b00100, 0b00100, 0b00100},
	'Z': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000, 0b10000, 0b11111},
}

// 配色：浅底深字，无论站点是深色还是浅色主题都能看清。
var (
	colorBackground = color.RGBA{R: 0xF1, G: 0xF3, B: 0xF7, A: 0xFF}
	colorGlyphs     = []color.RGBA{
		{R: 0x1F, G: 0x29, B: 0x37, A: 0xFF}, // 石板灰
		{R: 0x31, G: 0x3A, B: 0x6B, A: 0xFF}, // 靛蓝
		{R: 0x0F, G: 0x51, B: 0x32, A: 0xFF}, // 深绿
		{R: 0x7A, G: 0x2E, B: 0x2E, A: 0xFF}, // 深红
		{R: 0x4C, G: 0x1D, B: 0x95, A: 0xFF}, // 紫
	}
	colorNoiseLines = []color.RGBA{
		{R: 0xA5, G: 0xB4, B: 0xFC, A: 0xFF},
		{R: 0xFC, G: 0xA5, B: 0xA5, A: 0xFF},
		{R: 0x86, G: 0xEF, B: 0xAC, A: 0xFF},
		{R: 0xFD, G: 0xE0, B: 0x8A, A: 0xFF},
	}
)

// supersample 是内部绘制倍率。先在放大后的画布上作画，最后用均值滤波降采样，
// 这样点阵字模放大后的硬锯齿会被抹平，观感更接近矢量字体。
const supersample = 3

// render 把验证码文本绘制成图片。
func render(code string, opts Options) image.Image {
	hi := image.NewRGBA(image.Rect(0, 0, opts.Width*supersample, opts.Height*supersample))

	fillBackground(hi, colorBackground)
	drawNoiseDots(hi, opts.Height*supersample/2, 0.35)
	drawNoiseLines(hi, opts.Width*supersample, opts.Height*supersample, 3)

	runes := []rune(code)
	if len(runes) == 0 {
		return downsample(hi, supersample)
	}

	// 先算出字形尺寸，再把剩余宽度均分成 n+1 份作为间隙。
	// 这样字符之间始终留有余量，不会因为随机抖动而挤成一团。
	scale := float64(opts.Height-18) * float64(supersample) / float64(glyphRows)
	glyphW := float64(glyphCols) * scale

	canvasW := float64(opts.Width * supersample)
	minGap := float64(2 * supersample)
	gap := (canvasW - glyphW*float64(len(runes))) / float64(len(runes)+1)

	if gap < minGap {
		// 画布偏窄时按比例收缩字形，保证不重叠
		factor := canvasW / (glyphW*float64(len(runes)) + minGap*float64(len(runes)+1))
		scale *= factor
		glyphW = float64(glyphCols) * scale
		gap = (canvasW - glyphW*float64(len(runes))) / float64(len(runes)+1)
	}

	jitterX := 1.5 * float64(supersample)
	jitterY := 3.0 * float64(supersample)

	for i, ch := range runes {
		centerX := gap*float64(i+1) + glyphW*float64(i) + glyphW/2
		cx := int(centerX + (rand.Float64()*2-1)*jitterX)
		cy := int(float64(opts.Height*supersample)/2 + (rand.Float64()*2-1)*jitterY)
		angle := (rand.Float64()*0.62 - 0.31) // 约 ±18°

		drawGlyphRotated(hi, ch, cx, cy, scale, angle, colorGlyphs[rand.IntN(len(colorGlyphs))])
	}

	drawNoiseDots(hi, opts.Height*supersample/3, 0.30)

	return downsample(hi, supersample)
}

// downsample 用 ss×ss 的均值滤波把图像缩回目标尺寸，起到抗锯齿的作用。
func downsample(src *image.RGBA, ss int) *image.RGBA {
	srcBounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, srcBounds.Dx()/ss, srcBounds.Dy()/ss))

	samples := uint32(ss * ss)
	for y := 0; y < dst.Rect.Dy(); y++ {
		for x := 0; x < dst.Rect.Dx(); x++ {
			var sumR, sumG, sumB uint32
			for dy := 0; dy < ss; dy++ {
				for dx := 0; dx < ss; dx++ {
					c := src.RGBAAt(x*ss+dx, y*ss+dy)
					sumR += uint32(c.R)
					sumG += uint32(c.G)
					sumB += uint32(c.B)
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(sumR / samples),
				G: uint8(sumG / samples),
				B: uint8(sumB / samples),
				A: 0xFF,
			})
		}
	}
	return dst
}

func fillBackground(dst *image.RGBA, col color.RGBA) {
	for y := dst.Rect.Min.Y; y < dst.Rect.Max.Y; y++ {
		for x := dst.Rect.Min.X; x < dst.Rect.Max.X; x++ {
			dst.SetRGBA(x, y, col)
		}
	}
}

// drawGlyphRotated 以 (cx, cy) 为中心绘制一个带旋转的字符。
// 采用「逆变换 + 最近邻」：遍历旋转后的包围盒，把每个像素反向映射回字模坐标，
// 命中笔画则上色。这样无需引入任何图形库就能实现旋转。
func drawGlyphRotated(dst *image.RGBA, ch rune, cx, cy int, scale, angle float64, col color.RGBA) {
	rows, ok := font[ch]
	if !ok {
		return
	}

	w := float64(glyphCols) * scale
	h := float64(glyphRows) * scale
	cos, sin := math.Cos(angle), math.Sin(angle)
	radius := int(math.Ceil(math.Hypot(w, h)/2)) + 1

	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			// 逆旋转到字模自身坐标系
			lx := float64(dx)*cos + float64(dy)*sin
			ly := -float64(dx)*sin + float64(dy)*cos

			gx := int((lx + w/2) / scale)
			gy := int((ly + h/2) / scale)
			if gx < 0 || gx >= glyphCols || gy < 0 || gy >= glyphRows {
				continue
			}
			// 最高位对应最左侧像素
			if rows[gy]&(1<<(glyphCols-1-gx)) == 0 {
				continue
			}
			blend(dst, cx+dx, cy+dy, col, 1)
		}
	}
}

// drawNoiseLines 画若干条二次贝塞尔曲线作为干扰线。
func drawNoiseLines(dst *image.RGBA, width, height, count int) {
	for i := 0; i < count; i++ {
		x0 := float64(rand.IntN(width))
		y0 := float64(rand.IntN(height))
		x1 := float64(rand.IntN(width))
		y1 := float64(rand.IntN(height))
		// 控制点取两端中点附近，让曲线自然弯曲
		cx := (x0+x1)/2 + float64(rand.IntN(width/2)-width/4)
		cy := (y0+y1)/2 + float64(rand.IntN(height/2)-height/4)

		col := colorNoiseLines[rand.IntN(len(colorNoiseLines))]

		steps := width * 3
		for s := 0; s <= steps; s++ {
			t := float64(s) / float64(steps)
			mt := 1 - t
			x := mt*mt*x0 + 2*mt*t*cx + t*t*x1
			y := mt*mt*y0 + 2*mt*t*cy + t*t*y1
			blend(dst, int(x), int(y), col, 0.55)
		}
	}
}

// drawNoiseDots 撒随机噪点，ratio 为噪点占全部像素的比例。
func drawNoiseDots(dst *image.RGBA, count int, ratio float64) {
	area := dst.Rect.Dx() * dst.Rect.Dy()
	total := int(float64(area) * ratio)
	if total < count {
		total = count
	}

	for i := 0; i < total; i++ {
		x := rand.IntN(dst.Rect.Dx())
		y := rand.IntN(dst.Rect.Dy())
		col := colorNoiseLines[rand.IntN(len(colorNoiseLines))]
		blend(dst, x, y, col, 0.45)
	}
}

// blend 以 alpha 混合的方式给像素上色，越界自动忽略。
func blend(dst *image.RGBA, x, y int, col color.RGBA, alpha float64) {
	if !(image.Point{X: x, Y: y}).In(dst.Rect) {
		return
	}
	if alpha >= 1 {
		dst.SetRGBA(x, y, col)
		return
	}

	old := dst.RGBAAt(x, y)
	mix := func(a, b uint8) uint8 {
		return uint8(float64(a)*(1-alpha) + float64(b)*alpha)
	}
	dst.SetRGBA(x, y, color.RGBA{
		R: mix(old.R, col.R),
		G: mix(old.G, col.G),
		B: mix(old.B, col.B),
		A: 0xFF,
	})
}

// encodePNG 把图片编码为 PNG 字节。
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeJPEG 把图片编码为 JPEG 字节。
// 滑块底图是大面积平滑渐变，PNG 无损压缩在这里几乎不起作用（30KB+），
// 换成 JPEG 后体积能降到十分之一左右，而轻微压缩痕迹反而增加了纹理。
func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// normalizeAnswer 统一用户输入：去空格、转大写，让校验不受大小写影响。
func normalizeAnswer(answer string) string {
	return strings.ToUpper(strings.TrimSpace(answer))
}
