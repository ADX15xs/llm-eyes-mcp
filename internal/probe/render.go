// Package probe measures how well a vision model localizes regions on a
// synthetic screenshot that carries exact ground truth. It is the Go
// replacement for the retired scripts/probe_grounding_accuracy.mjs: the case
// is drawn with image/draw instead of a browser, so the ground truth comes
// from the draw parameters themselves and can never drift from the pixels.
package probe

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// Canvas is the fixed synthetic screenshot size.
const (
	W = 750
	H = 450
)

// Target is one region the model must locate. Label is the Chinese hint fed
// to the prompt (mimicking real spec descriptions); it is never drawn — the
// probe font is ASCII-only and the prompt does not need it rendered.
type Target struct {
	ID    string
	Text  string
	Label string
}

// Targets are the eight probe regions. Texts mimic a real game UI and repeat
// across cards ("Gift Name", "Claim") so a region cannot be guessed from text
// alone; duplicates resolve to the first card, like the retired DOM locator's
// .first().
var Targets = []Target{
	{"r1", "Gift Name", "礼物名称"},
	{"r2", "Voucher x1", "代金券数量"},
	{"r3", "Not Reached", "未达成"},
	{"r4", "1 Players", "一人团"},
	{"r5", "3 Players", "三人团"},
	{"r6", "10 Players", "十人团"},
	{"r7", "Claim", "领取按钮"},
	{"r8", "Expires 2026-11-30", "过期时间"},
}

// Case is one rendered probe case.
type Case struct {
	PNG []byte
	// GT maps target id -> normalized [x0,y0,x1,y1] of the drawn glyph box.
	GT map[string][4]float64
}

const (
	pagePadding = 22
	cardGap     = 18
	cardW       = 163
	cardH       = 150
)

var (
	colBackground = color.RGBA{0xf4, 0xf6, 0xfb, 0xff}
	colCard       = color.RGBA{0xff, 0xff, 0xff, 0xff}
	colName       = color.RGBA{0x8a, 0x5a, 0x2b, 0xff}
	colQty        = color.RGBA{0x2b, 0x6e, 0x8a, 0xff}
	colState      = color.RGBA{0xc0, 0x39, 0x2b, 0xff}
	colButton     = color.RGBA{0xe8, 0x45, 0x2c, 0xff}
	colButtonText = color.RGBA{0xff, 0xff, 0xff, 0xff}
	colFooter     = color.RGBA{0x55, 0x55, 0x55, 0xff}
)

// Render draws the synthetic case and encodes it as PNG.
func Render() (*Case, error) {
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), image.NewUniform(colBackground), image.Point{}, draw.Src)

	gt := make(map[string][4]float64, len(Targets))
	cards := [...]struct{ name, qty, state string }{
		{"Gift Name", "Voucher x1", "Not Reached"},
		{"Gift Name", "Voucher x2", "1 Players"},
		{"Gift Name", "Voucher x3", "3 Players"},
		{"Gift Name", "Voucher x5", "10 Players"},
	}
	for i, c := range cards {
		x0 := pagePadding + i*(cardW+cardGap)
		y0 := pagePadding
		rect(img, x0, y0, cardW, cardH, colCard)
		nameBox := drawText(img, x0+14, y0+14, c.name, colName)
		qtyBox := drawText(img, x0+14, y0+33, c.qty, colQty)
		stateBox := drawText(img, x0+14, y0+52, c.state, colState)
		btnBox := drawButton(img, x0, y0, "Claim")
		switch i {
		case 0:
			record(gt, "r1", nameBox)
			record(gt, "r2", qtyBox)
			record(gt, "r3", stateBox)
			record(gt, "r7", btnBox)
		case 1:
			record(gt, "r4", stateBox)
		case 2:
			record(gt, "r5", stateBox)
		case 3:
			record(gt, "r6", stateBox)
		}
	}

	footY := pagePadding + cardH + 2
	footBox := drawText(img, pagePadding, footY, "Expires 2026-11-30", colFooter)
	record(gt, "r8", footBox)
	drawText(img, W-pagePadding-7*len("Terms apply"), footY, "Terms apply", colFooter)

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return &Case{PNG: buf.Bytes(), GT: gt}, nil
}

// drawButton draws the red pill button with a white label at the card's
// bottom-right corner and returns the label's glyph box.
func drawButton(img *image.RGBA, cardX, cardY int, label string) image.Rectangle {
	const (
		margin = 12
		padX   = 12
		padY   = 5
	)
	textW := 7 * len(label)
	textH := basicfont.Face7x13.Height
	bx1 := cardX + cardW - margin
	by1 := cardY + cardH - margin
	rect(img, bx1-textW-2*padX, by1-textH-2*padY, textW+2*padX, textH+2*padY, colButton)
	return drawText(img, bx1-padX-textW, by1-padY-textH, label, colButtonText)
}

// drawText draws s with (x, y) as the glyph box top-left and returns that box.
// The box doubles as ground truth, so what is drawn and what is scored cannot
// drift apart.
func drawText(img *image.RGBA, x, y int, s string, c color.RGBA) image.Rectangle {
	f := basicfont.Face7x13
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: f,
		Dot:  fixed.P(x, y+f.Ascent),
	}
	d.DrawString(s)
	return image.Rect(x, y, x+f.Advance*len(s), y+f.Height)
}

func rect(img *image.RGBA, x, y, w, h int, c color.RGBA) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), image.NewUniform(c), image.Point{}, draw.Src)
}

func record(gt map[string][4]float64, id string, box image.Rectangle) {
	gt[id] = [4]float64{
		float64(box.Min.X) / W, float64(box.Min.Y) / H,
		float64(box.Max.X) / W, float64(box.Max.Y) / H,
	}
}
