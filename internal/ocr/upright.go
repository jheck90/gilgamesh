package ocr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png" // phones send JPEG; a screenshot may be PNG
)

// upright returns photo as Tesseract should see it: turned the way the
// phone was held, in shades of grey. A phone keeps the pixels as the sensor
// saw them and records in EXIF how to turn them, which Tesseract ignores;
// read sideways, a page is gibberish. Grey also reads better than colour
// off a real page. ok is false if photo can't be decoded here, and then it
// goes to Tesseract as it is.
func upright(photo []byte) (pgm []byte, ok bool) {
	img, _, err := image.Decode(bytes.NewReader(photo))
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	o := orientation(photo)
	if o >= 5 { // a quarter turn swaps width and height
		w, h = h, w
	}
	// A JPEG's luma is already its shades of grey.
	lum := func(x, y int) uint8 {
		return color.GrayModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
	}
	if yc, ok := img.(*image.YCbCr); ok {
		lum = func(x, y int) uint8 { return yc.Y[yc.YOffset(b.Min.X+x, b.Min.Y+y)] }
	}
	grey := image.NewGray(image.Rect(0, 0, w, h))
	for y := range b.Dy() {
		for x := range b.Dx() {
			gx, gy := place(o, x, y, b.Dx(), b.Dy())
			grey.Pix[gy*grey.Stride+gx] = lum(x, y)
		}
	}
	// A binary PGM: Leptonica reads it with no decoding to speak of.
	var out bytes.Buffer
	fmt.Fprintf(&out, "P5\n%d %d\n255\n", w, h)
	out.Grow(len(grey.Pix))
	for y := range h {
		out.Write(grey.Pix[y*grey.Stride : y*grey.Stride+w])
	}
	return out.Bytes(), true
}

// place maps pixel (x, y) of a w×h stored image to where it shows once
// turned by EXIF orientation o.
func place(o, x, y, w, h int) (int, int) {
	switch o {
	case 2: // mirrored
		return w - 1 - x, y
	case 3: // upside down
		return w - 1 - x, h - 1 - y
	case 4: // mirrored upside down
		return x, h - 1 - y
	case 5: // mirrored, a quarter turn left
		return y, x
	case 6: // a quarter turn right
		return h - 1 - y, x
	case 7: // mirrored, a quarter turn right
		return h - 1 - y, w - 1 - x
	case 8: // a quarter turn left
		return y, w - 1 - x
	}
	return x, y
}

// orientation reads the EXIF orientation (1 to 8) from a JPEG's APP1
// segment, or 1 if it has none.
func orientation(jpg []byte) int {
	if len(jpg) < 4 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(jpg) && jpg[i] == 0xFF; {
		marker := jpg[i+1]
		if marker == 0xDA { // start of scan: no more metadata
			break
		}
		n := int(binary.BigEndian.Uint16(jpg[i+2:]))
		if n < 2 || i+2+n > len(jpg) {
			break
		}
		if seg := jpg[i+4 : i+2+n]; marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return tiffOrientation(seg[6:])
		}
		i += 2 + n
	}
	return 1
}

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
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 1
	}
	for k := range int(bo.Uint16(t[ifd:])) {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			break
		}
		if bo.Uint16(t[e:]) == 0x0112 { // Orientation, a SHORT held in the entry
			if o := int(bo.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			break
		}
	}
	return 1
}
