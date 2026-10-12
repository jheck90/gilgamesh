package ocr

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/jheck90/gilgamesh/internal/text"
)

// testdata/loomings.txt is chapter 1 of Moby-Dick from Project Gutenberg
// (ebook #2701), a paragraph a line. testdata/page.jpg is part of it,
// rendered once like a paperback page: a running head and page number,
// words hyphenated at line ends, and a sentence cut off at the bottom.
func TestTesseractReadsAPage(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("the tesseract program isn't installed")
	}
	photo, err := os.ReadFile("testdata/page.jpg")
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := os.ReadFile("testdata/loomings.txt")
	if err != nil {
		t.Fatal(err)
	}
	var sents []string
	for _, p := range strings.Split(strings.TrimSpace(string(chapter)), "\n") {
		sents = append(sents, text.Split(p)...)
	}

	got, err := Tesseract{}.ReadPhoto(context.Background(), photo)
	if err != nil {
		t.Fatal(err)
	}
	page, ok := text.MatchPage(got, sents)
	if !ok {
		t.Fatalf("the page wasn't found from what Tesseract read:\n%s", got)
	}
	if first := sents[page.First]; !strings.HasPrefix(first, "There now is your insular city") {
		t.Errorf("the page starts at %q, want the sentence about the insular city", first)
	}
	if last := sents[page.Last]; last != "And there they stand—miles of them—leagues." || !page.Cut {
		t.Errorf("the page ends at %q (cut: %v), want the cut-off sentence about the crowds", last, page.Cut)
	}
}

// A phone keeps the pixels the way the sensor saw them and records in EXIF
// how to turn them upright. Tesseract ignores that, so a photo held upright
// reads as gibberish unless it's turned first.
func TestTesseractReadsAPhotoTheWayThePhoneHeldIt(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("the tesseract program isn't installed")
	}
	raw, err := os.ReadFile("testdata/page.jpg")
	if err != nil {
		t.Fatal(err)
	}
	upright, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := os.ReadFile("testdata/loomings.txt")
	if err != nil {
		t.Fatal(err)
	}
	var sents []string
	for _, p := range strings.Split(strings.TrimSpace(string(chapter)), "\n") {
		sents = append(sents, text.Split(p)...)
	}

	for _, tc := range []struct {
		name        string
		turns       int // quarter turns clockwise applied to the upright page
		orientation uint16
	}{
		{"held upright (stored a quarter turn left)", 3, 6},
		{"held upside down", 2, 3},
		{"held the other way on its side", 1, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := upright
			for range tc.turns {
				img = quarterTurn(img)
			}
			var b bytes.Buffer
			if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
				t.Fatal(err)
			}
			got, err := Tesseract{}.ReadPhoto(context.Background(), withEXIF(b.Bytes(), tc.orientation))
			if err != nil {
				t.Fatal(err)
			}
			page, ok := text.MatchPage(got, sents)
			if !ok || !strings.HasPrefix(sents[page.First], "There now is your insular city") {
				t.Errorf("the page wasn't found (%d of %d words matched)", page.Matched, page.Read)
			}
		})
	}
}

// quarterTurn turns img a quarter turn clockwise.
func quarterTurn(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.Set(b.Max.Y-1-y, x-b.Min.X, img.At(x, y))
		}
	}
	return out
}

// withEXIF adds an EXIF block to a JPEG the way an iPhone writes one:
// little-endian, with the camera's make ahead of the orientation.
func withEXIF(jpg []byte, orientation uint16) []byte {
	le := binary.LittleEndian
	var tiff bytes.Buffer
	tiff.WriteString("II*\x00")
	binary.Write(&tiff, le, uint32(8)) // IFD0
	binary.Write(&tiff, le, uint16(2))
	// Make: ASCII "Apple\0", stored after the IFD.
	binary.Write(&tiff, le, uint16(0x010F))
	binary.Write(&tiff, le, uint16(2))
	binary.Write(&tiff, le, uint32(6))
	binary.Write(&tiff, le, uint32(8+2+2*12+4))
	// Orientation: SHORT, held in the entry itself.
	binary.Write(&tiff, le, uint16(0x0112))
	binary.Write(&tiff, le, uint16(3))
	binary.Write(&tiff, le, uint32(1))
	binary.Write(&tiff, le, orientation)
	binary.Write(&tiff, le, uint16(0))
	binary.Write(&tiff, le, uint32(0)) // no IFD1
	tiff.WriteString("Apple\x00")
	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)}
	return append(append(append([]byte{0xFF, 0xD8}, seg...), app1...), jpg[2:]...)
}
