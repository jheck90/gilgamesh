package epub_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jheck90/gilgamesh/internal/epub"
	"github.com/jheck90/gilgamesh/internal/epub/epubtest"
)

func TestOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "book.epub")
	err := epubtest.Write(p, epubtest.Book{
		Title: "The Test Dungeon", Author: "A. Writer", Series: "Test Saga", SeriesIndex: "2",
		Chapters: []epubtest.Chapter{
			{Title: "Chapter One", Paras: []string{"Carl woke up.", "The cat & the man left."}},
			{Title: "Part Two", Paras: nil},
			{Title: "Chapter Two", Paras: []string{"Day 2 began."}, SameFile: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := epub.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != "The Test Dungeon" || b.Author != "A. Writer" || b.Language != "en" {
		t.Errorf("metadata = %q / %q / %q", b.Title, b.Author, b.Language)
	}
	if b.Series != "Test Saga" || b.SeriesIndex != 2 {
		t.Errorf("series = %q #%v", b.Series, b.SeriesIndex)
	}
	if len(b.Sections) != 3 {
		t.Fatalf("got %d sections, want 3: %+v", len(b.Sections), b.Sections)
	}
	if b.Sections[1].Title != "Part Two" || b.Sections[2].Title != "Chapter Two" ||
		!reflect.DeepEqual(b.Sections[2].Paragraphs, []string{"Chapter Two", "Day 2 began."}) {
		t.Errorf("a chapter sharing a file with its part title should be its own section: %+v", b.Sections[1:])
	}
	s := b.Sections[0]
	if s.Title != "Chapter One" {
		t.Errorf("title = %q (the toc nav should win over the landmarks nav)", s.Title)
	}
	want := []string{"Chapter One", "Carl woke up.", "The cat & the man left."}
	if !reflect.DeepEqual(s.Paragraphs, want) {
		t.Errorf("paragraphs = %q, want %q", s.Paragraphs, want)
	}
}
