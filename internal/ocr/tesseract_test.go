package ocr

import (
	"context"
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
