package httpapi

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/png"
	"strings"
	"unicode/utf8"
)

// A very small PDF writer for generated documents (the e-sign certificate): A4 pages, the two standard Helvetica
// fonts (WinAnsi text) and PNG images (signatures, flattened on white). Text outside WinAnsi is transliterated
// (Arabic → Latin letters) so names stay readable; the exact original text is kept in Paxve and its SHA-256 is
// printed in the document.

type pdfImage struct {
	w, h int
	data []byte // zlib RGB
}

type pdfPage struct {
	content bytes.Buffer
	images  []int
}

type pdfWriter struct {
	pages  []*pdfPage
	images []pdfImage
	cur    *pdfPage
	y      float64
}

const (
	pdfLeft   = 48.0
	pdfRight  = 547.0
	pdfTop    = 800.0
	pdfBottom = 56.0
)

func newPDF() *pdfWriter {
	p := &pdfWriter{}
	p.newPage()
	return p
}

func (p *pdfWriter) newPage() {
	pg := &pdfPage{}
	p.pages = append(p.pages, pg)
	p.cur = pg
	p.y = pdfTop
}

func (p *pdfWriter) ensure(h float64) {
	if p.y-h < pdfBottom {
		p.newPage()
	}
}

var arabicLatin = map[rune]string{
	'ا': "a", 'أ': "a", 'إ': "i", 'آ': "a", 'ب': "b", 'ت': "t", 'ث': "th", 'ج': "j", 'ح': "h", 'خ': "kh", 'د': "d", 'ذ': "dh", 'ر': "r", 'ز': "z",
	'س': "s", 'ش': "sh", 'ص': "s", 'ض': "d", 'ط': "t", 'ظ': "z", 'ع': "'", 'غ': "gh", 'ف': "f", 'ق': "q", 'ك': "k", 'ل': "l", 'م': "m", 'ن': "n",
	'ه': "h", 'و': "w", 'ي': "y", 'ى': "a", 'ة': "a", 'ء': "'", 'ؤ': "'", 'ئ': "'", 'پ': "p", 'چ': "ch", 'ژ': "zh", 'گ': "g", 'ک': "k", 'ی': "y",
	'،': ",", '؛': ";", '؟': "?", '٠': "0", '١': "1", '٢': "2", '٣': "3", '٤': "4", '٥': "5", '٦': "6", '٧': "7", '٨': "8", '٩': "9", 'ـ': "",
	'«': "\"", '»': "\"", '–': "-", '—': "-", '’': "'", '‘': "'", '“': "\"", '”': "\"", '…': "...", '·': "-", '→': "->",
}

// winAnsi converts text to the bytes of a PDF string in WinAnsi (escaped), transliterating what it cannot show.
func winAnsi(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r >= 32 && r < 127:
			b.WriteRune(r)
		case r >= 0xA0 && r <= 0xFF:
			b.WriteByte(byte(r))
		case r >= 0x064B && r <= 0x0652: // Arabic diacritics
		default:
			if t, ok := arabicLatin[r]; ok {
				b.WriteString(t)
			} else if r >= 32 {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}

// textWidth: a rough width of Helvetica text (good enough to wrap).
func textWidth(s string, size float64) float64 {
	return float64(utf8.RuneCountInString(s)) * size * 0.52
}

func (p *pdfWriter) textAt(x, y, size float64, bold bool, s string) {
	f := "F1"
	if bold {
		f = "F2"
	}
	fmt.Fprintf(&p.cur.content, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n", f, size, x, y, winAnsi(s))
}

// line writes wrapped text at the left margin (indent in points) and moves down.
func (p *pdfWriter) line(s string, size float64, bold bool, indent float64) {
	width := pdfRight - pdfLeft - indent
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			p.ensure(size + 4)
			p.y -= size + 4
			continue
		}
		cur := ""
		flush := func() {
			p.ensure(size + 4)
			p.textAt(pdfLeft+indent, p.y-size, size, bold, cur)
			p.y -= size + 4
			cur = ""
		}
		for _, w := range words {
			for textWidth(w, size) > width { // a very long word is cut
				n := int(width / (size * 0.52))
				if n < 1 {
					n = 1
				}
				rs := []rune(w)
				if n >= len(rs) {
					break
				}
				if cur != "" {
					flush()
				}
				cur = string(rs[:n])
				flush()
				w = string(rs[n:])
			}
			try := w
			if cur != "" {
				try = cur + " " + w
			}
			if textWidth(try, size) > width && cur != "" {
				flush()
				try = w
			}
			cur = try
		}
		if cur != "" {
			flush()
		}
	}
}

func (p *pdfWriter) gap(h float64) {
	p.y -= h
	if p.y < pdfBottom {
		p.newPage()
	}
}

func (p *pdfWriter) rule() {
	p.ensure(8)
	fmt.Fprintf(&p.cur.content, "0.75 G 0.5 w %.2f %.2f m %.2f %.2f l S 0 G\n", pdfLeft, p.y-4, pdfRight, p.y-4)
	p.y -= 10
}

// pngImage places a PNG (drawn on white) at the left margin + indent, scaled into maxW × maxH.
func (p *pdfWriter) pngImage(data []byte, indent, maxW, maxH float64) error {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	if w <= 0 || h <= 0 || w > 4000 || h > 4000 {
		return fmt.Errorf("image size")
	}
	raw := make([]byte, 0, w*h*3)
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			// alpha-composite on white (values are premultiplied, 16-bit)
			bl := func(c uint32) byte { return byte((c + (0xffff - a)) >> 8) }
			raw = append(raw, bl(r), bl(g), bl(b))
		}
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	p.images = append(p.images, pdfImage{w: w, h: h, data: z.Bytes()})
	idx := len(p.images)
	scale := maxW / float64(w)
	if s := maxH / float64(h); s < scale {
		scale = s
	}
	dw, dh := float64(w)*scale, float64(h)*scale
	p.ensure(dh + 4)
	p.cur.images = append(p.cur.images, idx)
	fmt.Fprintf(&p.cur.content, "q %.2f 0 0 %.2f %.2f %.2f cm /Im%d Do Q\n", dw, dh, pdfLeft+indent, p.y-dh, idx)
	p.y -= dh + 4
	return nil
}

var _ image.Image // image formats register through image/png

// bytes renders the document.
func (p *pdfWriter) bytes() []byte {
	var out bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	stream := func(dict string, data []byte) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n<< %s /Length %d >>\nstream\n", len(offsets), dict, len(data))
		out.Write(data)
		out.WriteString("\nendstream\nendobj\n")
	}
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	nImg := len(p.images)
	firstPage := 5 + nImg // objects: 1 catalog, 2 pages, 3-4 fonts, images, then page + content pairs
	kids := make([]string, len(p.pages))
	for i := range p.pages {
		kids[i] = fmt.Sprintf("%d 0 R", firstPage+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(p.pages)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")
	for _, im := range p.images {
		stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode", im.w, im.h), im.data)
	}
	for i, pg := range p.pages {
		xo := ""
		for _, idx := range pg.images {
			xo += fmt.Sprintf(" /Im%d %d 0 R", idx, 4+idx)
		}
		res := "/Font << /F1 3 0 R /F2 4 0 R >>"
		if xo != "" {
			res += " /XObject <<" + xo + " >>"
		}
		// footer: page number
		fmt.Fprintf(&pg.content, "BT /F1 8 Tf %.2f 30 Td (%d / %d) Tj ET\n", pdfRight-30, i+1, len(p.pages))
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << %s >> /Contents %d 0 R >>", res, firstPage+2*i+1))
		stream("", pg.content.Bytes())
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes()
}
