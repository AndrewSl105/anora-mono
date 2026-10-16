package attachments

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
)

// jpegOrientation returns the EXIF orientation (1–8) of a JPEG, or 1 when it
// has none. Re-encoding drops EXIF, so the rotation is applied to the pixels.
func jpegOrientation(b []byte) int {
	i := 2 // after SOI
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return 1
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) > 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

// tiffOrientation reads tag 0x0112 from IFD0 of a TIFF header.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 { // SHORT
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient returns img transformed so that EXIF orientation o displays upright.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 { // 5–8 swap width and height
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	ycc, _ := img.(*image.YCbCr)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl uint8
			a := uint8(0xFF)
			if ycc != nil {
				c := ycc.YCbCrAt(b.Min.X+x, b.Min.Y+y)
				r, g, bl = color.YCbCrToRGB(c.Y, c.Cb, c.Cr)
			} else {
				c := color.RGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA)
				r, g, bl, a = c.R, c.G, c.B, c.A
			}
			dx, dy := orientPoint(o, x, y, w, h)
			p := dst.PixOffset(dx, dy)
			dst.Pix[p], dst.Pix[p+1], dst.Pix[p+2], dst.Pix[p+3] = r, g, bl, a
		}
	}
	return dst
}

// orientPoint maps source pixel (x, y) of a w×h image to its upright position.
func orientPoint(o, x, y, w, h int) (int, int) {
	switch o {
	case 2: // mirrored horizontally
		return w - 1 - x, y
	case 3: // rotated 180°
		return w - 1 - x, h - 1 - y
	case 4: // mirrored vertically
		return x, h - 1 - y
	case 5: // transposed
		return y, x
	case 6: // needs 90° clockwise
		return h - 1 - y, x
	case 7: // transversed
		return h - 1 - y, w - 1 - x
	case 8: // needs 90° counter-clockwise
		return y, w - 1 - x
	}
	return x, y
}

// ---- WebP ----

const (
	vp8xEXIF      = 0x08
	vp8xXMP       = 0x04
	vp8xAnimation = 0x02
)

// stripWebP rewrites a WebP RIFF container without its EXIF and XMP chunks
// (and clears their VP8X flags). The image data is copied unchanged: the Go
// libraries cannot encode WebP. Animated WebP is rejected.
func stripWebP(b []byte) ([]byte, error) {
	if len(b) < 12 {
		return nil, errors.New("truncated WebP")
	}
	out := make([]byte, 12, len(b))
	copy(out, b[:12])
	vp8x := -1
	for i := 12; i < len(b); {
		if i+8 > len(b) {
			return nil, errors.New("truncated WebP chunk")
		}
		id := string(b[i : i+4])
		size := int(binary.LittleEndian.Uint32(b[i+4:]))
		end := i + 8 + size + size&1 // chunks are padded to an even size
		if size < 0 || i+8+size > len(b) {
			return nil, errors.New("truncated WebP chunk")
		}
		end = min(end, len(b))
		switch id {
		case "ANIM", "ANMF":
			return nil, errors.New("animated WebP images are not supported (Claude would only see the first frame)")
		case "EXIF", "XMP ":
		default:
			if id == "VP8X" {
				vp8x = len(out)
			}
			out = append(out, b[i:end]...)
		}
		i = end
	}
	if vp8x >= 0 && vp8x+8 < len(out) {
		if out[vp8x+8]&vp8xAnimation != 0 {
			return nil, errors.New("animated WebP images are not supported (Claude would only see the first frame)")
		}
		out[vp8x+8] &^= vp8xEXIF | vp8xXMP
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out, nil
}
