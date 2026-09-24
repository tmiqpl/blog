package captcha

import (
	"os"
	"testing"
)

// TestRenderGlyphSheet 生成一张包含全部字符的字形对照表，仅供人工检查可辨识度。
// 通过 CAPTCHA_SHEET=路径 环境变量启用。
func TestRenderGlyphSheet(t *testing.T) {
	out := os.Getenv("CAPTCHA_SHEET")
	if out == "" {
		t.Skip("未设置 CAPTCHA_SHEET，跳过字形对照表生成")
	}

	opts := DefaultOptions()
	opts.CharSet = "34679ACDEFGHJKMNPQRTUVWXY"
	opts.Length = len(opts.CharSet)
	opts.Width = opts.Length * 46
	opts.Height = 64

	img := render(opts.CharSet, opts)

	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	data, err := encodePNG(img)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	t.Logf("字形对照表已写入 %s（%d 字节）", out, len(data))
}
