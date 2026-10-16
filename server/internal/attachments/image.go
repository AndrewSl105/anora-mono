// Package attachments validates, cleans and stores images attached to tasks
// and messages. Files live on the host at <ATTACHMENTS_DIR>/<task_id>/ and
// are mounted read-only into the task's agent container at /attachments.
package attachments

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

const (
	MaxFileBytes    = 40 << 20  // one file
	MaxRequestBytes = 100 << 20 // all files of one request
	MaxFiles        = 10        // files per request
	// MaxPixels bounds decode memory: 50 MP is 200 MB as RGBA.
	MaxPixels = 50_000_000
	ThumbSize = 480 // longest side of a thumbnail
	maxName   = 200 // runes kept of the original file name
)

// ErrInvalid wraps every rejection of an uploaded file (HTTP 400).
var ErrInvalid = errors.New("invalid attachment")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Image is a validated upload with its metadata removed.
type Image struct {
	Name          string // original file name, cleaned
	Mime          string
	Ext           string // file extension without the dot
	Width, Height int
	Data          []byte // the cleaned file
	Thumb         []byte // JPEG, longest side at most ThumbSize
}

type format struct{ mime, ext string }

var (
	fmtPNG  = format{"image/png", "png"}
	fmtJPEG = format{"image/jpeg", "jpg"}
	fmtGIF  = format{"image/gif", "gif"}
	fmtWebP = format{"image/webp", "webp"}
)

// sniff detects the type from the content; the name and the declared
// Content-Type are ignored.
func sniff(b []byte) (format, bool) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return fmtPNG, true
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return fmtJPEG, true
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return fmtGIF, true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return fmtWebP, true
	}
	return format{}, false
}

// decodeSem serialises decoding so concurrent uploads cannot multiply the
// peak memory of a large image.
var decodeSem = make(chan struct{}, 1)

// Process validates one uploaded file and returns it without metadata:
// JPEG, PNG and GIF are decoded and re-encoded (JPEG after applying its EXIF
// orientation); WebP keeps its image data and loses its EXIF and XMP chunks.
func Process(name string, data []byte) (*Image, error) {
	if len(data) == 0 {
		return nil, invalid("%s is empty", displayName(name))
	}
	if len(data) > MaxFileBytes {
		return nil, invalid("%s is larger than %d MB", displayName(name), MaxFileBytes>>20)
	}
	f, ok := sniff(data)
	if !ok {
		return nil, invalid("%s is not a PNG, JPEG, WebP or GIF image", displayName(name))
	}
	cfg, err := decodeConfig(f, data)
	if err != nil {
		return nil, invalid("%s: cannot read the image (%v)", displayName(name), err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, invalid("%s has no pixels", displayName(name))
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return nil, invalid("%s is %dx%d; at most %d megapixels are allowed", displayName(name), cfg.Width, cfg.Height, MaxPixels/1_000_000)
	}

	decodeSem <- struct{}{}
	defer func() { <-decodeSem }()

	img, clean, err := clean(f, data)
	if err != nil {
		return nil, invalid("%s: %v", displayName(name), err)
	}
	b := img.Bounds()
	thumb, err := thumbnail(img)
	if err != nil {
		return nil, err
	}
	return &Image{
		Name: cleanName(name, f.ext), Mime: f.mime, Ext: f.ext,
		Width: b.Dx(), Height: b.Dy(), Data: clean, Thumb: thumb,
	}, nil
}

func decodeConfig(f format, data []byte) (image.Config, error) {
	r := bytes.NewReader(data)
	switch f {
	case fmtPNG:
		return png.DecodeConfig(r)
	case fmtJPEG:
		return jpeg.DecodeConfig(r)
	case fmtGIF:
		return gif.DecodeConfig(r)
	default:
		return webp.DecodeConfig(r)
	}
}

// clean decodes the image and returns it with the metadata-free file.
func clean(f format, data []byte) (image.Image, []byte, error) {
	var buf bytes.Buffer
	switch f {
	case fmtJPEG:
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, nil, err
		}
		img = orient(img, jpegOrientation(data))
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
			return nil, nil, err
		}
		return img, buf.Bytes(), nil
	case fmtPNG:
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, nil, err
		}
		if err := png.Encode(&buf, img); err != nil {
			return nil, nil, err
		}
		return img, buf.Bytes(), nil
	case fmtGIF:
		g, err := gif.DecodeAll(bytes.NewReader(data))
		if err != nil {
			return nil, nil, err
		}
		if len(g.Image) != 1 {
			return nil, nil, errors.New("animated GIFs are not supported (Claude would only see the first frame)")
		}
		// EncodeAll writes only the image data: comments and application
		// extensions (XMP) are dropped.
		out := &gif.GIF{Image: g.Image, Delay: g.Delay, Disposal: g.Disposal, Config: g.Config, BackgroundIndex: g.BackgroundIndex}
		if err := gif.EncodeAll(&buf, out); err != nil {
			return nil, nil, err
		}
		return g.Image[0], buf.Bytes(), nil
	default:
		out, err := stripWebP(data)
		if err != nil {
			return nil, nil, err
		}
		img, err := webp.Decode(bytes.NewReader(out))
		if err != nil {
			return nil, nil, err
		}
		return img, out, nil
	}
}

// thumbnail scales img so its longest side is at most ThumbSize and encodes
// it as JPEG over white (transparent areas become white).
func thumbnail(img image.Image) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > ThumbSize || h > ThumbSize {
		if w >= h {
			w, h = ThumbSize, max(1, h*ThumbSize/b.Dx())
		} else {
			w, h = max(1, w*ThumbSize/b.Dy()), ThumbSize
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// cleanName keeps the base name without control characters, at most maxName
// runes; an empty result becomes "image.<ext>".
func cleanName(name, ext string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "image." + ext
	}
	if utf8.RuneCountInString(name) > maxName {
		name = string([]rune(name)[:maxName])
	}
	return name
}

func displayName(name string) string {
	if n := cleanName(name, ""); n != "image." {
		return fmt.Sprintf("%q", n)
	}
	return "the file"
}
