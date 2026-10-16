package attachments

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"

	"golang.org/x/image/webp"
)

func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 100, 255})
		}
	}
	// A red marker in the top-left corner shows where the image's origin went.
	for y := 0; y < h/4; y++ {
		for x := 0; x < w/4; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// exifSegment builds an APP1 segment with an orientation tag and a GPS
// pointer, the kind of metadata a phone writes.
func exifSegment(orientation uint16) []byte {
	var t bytes.Buffer
	t.WriteString("MM")
	_ = binary.Write(&t, binary.BigEndian, uint16(42))
	_ = binary.Write(&t, binary.BigEndian, uint32(8))
	_ = binary.Write(&t, binary.BigEndian, uint16(2)) // entries
	// Orientation, SHORT, count 1
	_ = binary.Write(&t, binary.BigEndian, []uint16{0x0112, 3})
	_ = binary.Write(&t, binary.BigEndian, uint32(1))
	_ = binary.Write(&t, binary.BigEndian, []uint16{orientation, 0})
	// GPSInfo IFD pointer, LONG
	_ = binary.Write(&t, binary.BigEndian, []uint16{0x8825, 4})
	_ = binary.Write(&t, binary.BigEndian, uint32(1))
	_ = binary.Write(&t, binary.BigEndian, uint32(0x1234))
	_ = binary.Write(&t, binary.BigEndian, uint32(0)) // next IFD
	t.WriteString("GPS-SECRET-LOCATION")
	payload := append([]byte("Exif\x00\x00"), t.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(seg, payload...)
}

func withExif(jpg []byte, orientation uint16) []byte {
	out := append([]byte{}, jpg[:2]...)
	out = append(out, exifSegment(orientation)...)
	return append(out, jpg[2:]...)
}

func isRed(c color.Color) bool {
	r, g, b, _ := c.RGBA()
	return r > 0xC000 && g < 0x4000 && b < 0x4000
}

func TestProcessJPEGStripsExifAndAppliesOrientation(t *testing.T) {
	src := withExif(encodeJPEG(t, testImage(80, 40)), 6) // stored sideways: needs 90° clockwise
	if jpegOrientation(src) != 6 {
		t.Fatalf("fixture orientation = %d", jpegOrientation(src))
	}
	img, err := Process("photo.jpg", src)
	if err != nil {
		t.Fatal(err)
	}
	if img.Mime != "image/jpeg" || img.Ext != "jpg" || img.Width != 40 || img.Height != 80 {
		t.Fatalf("got %s %s %dx%d, want image/jpeg jpg 40x80", img.Mime, img.Ext, img.Width, img.Height)
	}
	if bytes.Contains(img.Data, []byte("Exif")) || bytes.Contains(img.Data, []byte("GPS-SECRET")) {
		t.Fatal("EXIF survived re-encoding")
	}
	out, err := jpeg.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatal(err)
	}
	// After a 90° clockwise turn the top-left marker is in the top-right corner.
	if !isRed(out.At(img.Width-3, 2)) || isRed(out.At(2, 2)) {
		t.Fatal("orientation 6 was not applied")
	}
	if jpegOrientation(img.Data) != 1 {
		t.Fatal("output still carries an orientation")
	}
}

func TestOrientPointAllOrientations(t *testing.T) {
	// The top-left source pixel of a 4×2 image must land where each
	// orientation's upright view has it.
	want := map[int][2]int{1: {0, 0}, 2: {3, 0}, 3: {3, 1}, 4: {0, 1}, 5: {0, 0}, 6: {1, 0}, 7: {1, 3}, 8: {0, 3}}
	for o, w := range want {
		x, y := orientPoint(o, 0, 0, 4, 2)
		if x != w[0] || y != w[1] {
			t.Errorf("orientation %d: (0,0) -> (%d,%d), want (%d,%d)", o, x, y, w[0], w[1])
		}
	}
}

func TestProcessPNGDropsTextChunks(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, testImage(10, 10)); err != nil {
		t.Fatal(err)
	}
	// Insert a tEXt chunk after IHDR (8-byte signature + 25-byte IHDR chunk).
	src := b.Bytes()
	text := pngChunk("tEXt", []byte("Comment\x00secret author note"))
	src = append(append(append([]byte{}, src[:33]...), text...), src[33:]...)
	if _, err := png.Decode(bytes.NewReader(src)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	img, err := Process("shot.png", src)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(img.Data, []byte("secret author")) || img.Mime != "image/png" {
		t.Fatal("tEXt chunk survived")
	}
}

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32IEEE(append([]byte(typ), data...))
	_ = binary.Write(&b, binary.BigEndian, crc)
	return b.Bytes()
}

func crc32IEEE(b []byte) uint32 {
	c := ^uint32(0)
	for _, x := range b {
		c ^= uint32(x)
		for k := 0; k < 8; k++ {
			if c&1 != 0 {
				c = c>>1 ^ 0xEDB88320
			} else {
				c >>= 1
			}
		}
	}
	return ^c
}

func riffChunk(id string, data []byte) []byte {
	b := append([]byte(id), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// extendedWebP wraps a simple (VP8L) WebP in a VP8X container with EXIF and
// XMP chunks, as cameras and editors write it.
func extendedWebP(t *testing.T, flags byte) []byte {
	t.Helper()
	simple, err := os.ReadFile("testdata/gopher.lossless.webp")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := webp.DecodeConfig(bytes.NewReader(simple))
	if err != nil {
		t.Fatal(err)
	}
	vp8x := make([]byte, 10)
	vp8x[0] = flags
	putUint24 := func(b []byte, v int) { b[0], b[1], b[2] = byte(v), byte(v>>8), byte(v>>16) }
	putUint24(vp8x[4:], cfg.Width-1)
	putUint24(vp8x[7:], cfg.Height-1)
	body := []byte("WEBP")
	body = append(body, riffChunk("VP8X", vp8x)...)
	body = append(body, simple[12:]...) // the VP8L chunk
	body = append(body, riffChunk("EXIF", []byte("MM\x00\x2a GPS-SECRET-LOCATION"))...)
	body = append(body, riffChunk("XMP ", []byte("<x:xmpmeta>creator secret</x:xmpmeta>"))...)
	out := append([]byte("RIFF"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)))
	return append(out, body...)
}

func TestProcessWebPStripsMetadataChunks(t *testing.T) {
	src := extendedWebP(t, vp8xEXIF|vp8xXMP)
	img, err := Process("x.webp", src)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(img.Data, []byte("GPS-SECRET")) || bytes.Contains(img.Data, []byte("xmpmeta")) {
		t.Fatal("metadata chunk survived")
	}
	if string(img.Data[12:16]) != "VP8X" { // first chunk after the header is still VP8X
		t.Fatalf("unexpected layout %q", img.Data[12:16])
	}
	if flags := img.Data[20]; flags&(vp8xEXIF|vp8xXMP) != 0 {
		t.Fatalf("VP8X flags still announce metadata: %#x", flags)
	}
	if got := binary.LittleEndian.Uint32(img.Data[4:]); int(got) != len(img.Data)-8 {
		t.Fatalf("RIFF size %d, file %d", got, len(img.Data))
	}
	if _, err := webp.Decode(bytes.NewReader(img.Data)); err != nil {
		t.Fatalf("stripped WebP does not decode: %v", err)
	}
	if img.Mime != "image/webp" || img.Width == 0 {
		t.Fatalf("got %+v", img)
	}
}

func TestProcessRejects(t *testing.T) {
	var g bytes.Buffer
	pal := []color.Color{color.Black, color.White}
	anim := &gif.GIF{Image: []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 4, 4), pal), image.NewPaletted(image.Rect(0, 0, 4, 4), pal)}, Delay: []int{10, 10}}
	if err := gif.EncodeAll(&g, anim); err != nil {
		t.Fatal(err)
	}
	huge := encodeJPEG(t, image.NewGray(image.Rect(0, 0, 8, 8)))
	// Patch the SOF0 dimensions to 10000×10000 (100 MP) without the pixels.
	sof := bytes.Index(huge, []byte{0xFF, 0xC0})
	binary.BigEndian.PutUint16(huge[sof+5:], 10000)
	binary.BigEndian.PutUint16(huge[sof+7:], 10000)

	cases := map[string]struct {
		data []byte
		want string
	}{
		"svg":           {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "not a PNG, JPEG, WebP or GIF"},
		"pdf named jpg": {[]byte("%PDF-1.7 ..."), "not a PNG, JPEG, WebP or GIF"},
		"empty":         {nil, "is empty"},
		"too big":       {make([]byte, MaxFileBytes+1), "larger than 40 MB"},
		"animated gif":  {g.Bytes(), "animated GIFs"},
		"animated webp": {extendedWebP(t, vp8xAnimation), "animated WebP"},
		"megapixels":    {huge, "megapixels"},
		"truncated png": {[]byte("\x89PNG\r\n\x1a\n\x00\x00"), "cannot read"},
	}
	for name, c := range cases {
		_, err := Process(name+".jpg", c.data)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalid containing %q", name, err, c.want)
		}
	}
}

func TestProcessGIFAndThumbnail(t *testing.T) {
	var g bytes.Buffer
	if err := gif.Encode(&g, testImage(1000, 500), nil); err != nil {
		t.Fatal(err)
	}
	img, err := Process("anim.gif", g.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if img.Mime != "image/gif" || img.Width != 1000 || img.Height != 500 {
		t.Fatalf("got %s %dx%d", img.Mime, img.Width, img.Height)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(img.Thumb))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != ThumbSize || cfg.Height != ThumbSize/2 {
		t.Fatalf("thumbnail %dx%d, want %dx%d", cfg.Width, cfg.Height, ThumbSize, ThumbSize/2)
	}
}

func TestCleanName(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd":       "passwd",
		`C:\Users\me\shot.png`:   "shot.png",
		"a\x00b\nc.png":          "abc.png",
		"":                       "image.png",
		strings.Repeat("é", 300): strings.Repeat("é", maxName),
	}
	for in, want := range cases {
		if got := cleanName(in, "png"); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}
