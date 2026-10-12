package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jheck90/gilgamesh/internal/epub/epubtest"
	"github.com/jheck90/gilgamesh/internal/store"
)

// past is when every file in a test library was last changed, long enough
// ago that none is still arriving.
var past = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func newLibrary(t *testing.T) *Library {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	root := filepath.Join(dir, "books")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return &Library{Root: root, Store: st, Now: func() time.Time { return past.Add(time.Hour) }}
}

// write puts an EPUB at rel under the library, changed at past.
func write(t *testing.T, l *Library, rel string, b epubtest.Book) string {
	t.Helper()
	path := filepath.Join(l.Root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if b.Chapters == nil {
		b.Chapters = []epubtest.Chapter{{Title: "One", Paras: []string{
			"The crawlers walked a long way down the stairs and argued about where the boots had gone this time."}}}
	}
	if err := epubtest.Write(path, b); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	return path
}

func scan(t *testing.T, l *Library) map[string]File {
	t.Helper()
	fs, err := l.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]File{}
	for _, f := range fs {
		out[f.Path] = f
	}
	return out
}

func TestScanListsEPUBsWithTheirDetails(t *testing.T) {
	l := newLibrary(t)
	write(t, l, "Dinniman/Carl's Doomsday Scenario.epub", epubtest.Book{Title: "Carl's Doomsday Scenario", Author: "Matt Dinniman", Series: "Dungeon Crawler Carl", SeriesIndex: "2"})
	write(t, l, "Standalone.EPUB", epubtest.Book{Title: "Standalone", Author: "Someone"})
	if err := os.WriteFile(filepath.Join(l.Root, "notes.txt"), []byte("not a book"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := scan(t, l)
	if len(got) != 2 {
		t.Fatalf("listed %v, want the two EPUBs", got)
	}
	f := got["Dinniman/Carl's Doomsday Scenario.epub"]
	if f.Title != "Carl's Doomsday Scenario" || f.Author != "Matt Dinniman" || f.Series != "Dungeon Crawler Carl" || f.SeriesIndex != 2 {
		t.Errorf("details: %+v", f)
	}
	if len(f.SHA256) != 64 || f.Arriving || f.Unreadable != "" {
		t.Errorf("state: %+v", f)
	}
	if _, ok := got["Standalone.EPUB"]; !ok {
		t.Errorf("an upper-case .EPUB isn't listed: %v", got)
	}
}

func TestFilesThatCantBeAddedSayWhy(t *testing.T) {
	l := newLibrary(t)
	if err := os.WriteFile(filepath.Join(l.Root, "broken.epub"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(filepath.Join(l.Root, "broken.epub"), past, past)
	// No text: what a DRM-protected book looks like once opened.
	write(t, l, "locked.epub", epubtest.Book{Title: "Locked", Chapters: []epubtest.Chapter{{Title: "One"}}})
	fresh := write(t, l, "fresh.epub", epubtest.Book{Title: "Fresh"})
	now := l.Now()
	os.Chtimes(fresh, now.Add(-30*time.Second), now.Add(-30*time.Second))

	got := scan(t, l)
	for _, p := range []string{"broken.epub", "locked.epub"} {
		if got[p].Unreadable != "can't read this file" {
			t.Errorf("%s: %+v, want it unreadable", p, got[p])
		}
	}
	if f := got["fresh.epub"]; !f.Arriving || f.Title != "" || f.SHA256 != "" {
		t.Errorf("a file changed 30 seconds ago: %+v, want it still arriving and not read", f)
	}
}

func TestOnlyNewOrChangedFilesAreRead(t *testing.T) {
	l := newLibrary(t)
	path := write(t, l, "book.epub", epubtest.Book{Title: "First Title"})
	if f := scan(t, l)["book.epub"]; f.Title != "First Title" {
		t.Fatalf("first scan: %+v", f)
	}

	// Same size and time: the cached details stand, so the file isn't read
	// again (overwriting it with as many bytes and resetting its time is
	// the only way to tell).
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, make([]byte, info.Size()), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, past, past)
	if f := scan(t, l)["book.epub"]; f.Title != "First Title" || f.Unreadable != "" {
		t.Errorf("an unchanged file was read again: %+v", f)
	}

	later := past.Add(time.Minute)
	os.Chtimes(path, later, later)
	if f := scan(t, l)["book.epub"]; f.Unreadable == "" {
		t.Errorf("a changed file wasn't read again: %+v", f)
	}
}

func TestPathsStayInsideTheRoot(t *testing.T) {
	l := newLibrary(t)
	ctx := context.Background()
	inside := write(t, l, "Series/inside.epub", epubtest.Book{Title: "Inside"})
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.epub")
	if err := epubtest.Write(outside, epubtest.Book{Title: "Outside", Chapters: []epubtest.Chapter{{Title: "One", Paras: []string{
		"Nobody should ever be able to read this file through the library, however its path is spelled out."}}}}); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(outside, past, past)
	if err := os.Symlink(outside, filepath.Join(l.Root, "escape.epub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(l.Root, "elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(l.Root, "shortcut.epub")); err != nil {
		t.Fatal(err)
	}

	got := scan(t, l)
	for p := range got {
		if p != "Series/inside.epub" && p != "shortcut.epub" {
			t.Errorf("listed %s, which is outside the root", p)
		}
	}
	if _, ok := got["shortcut.epub"]; !ok {
		t.Errorf("a link to a file inside the root isn't listed: %v", got)
	}

	if f, err := l.File(ctx, "Series/inside.epub"); err != nil || f.Title != "Inside" || f.Abs == "" {
		t.Errorf("File(inside) = %+v, %v", f, err)
	}
	for _, rel := range []string{
		"escape.epub",
		"elsewhere/outside.epub",
		"../" + filepath.Base(outsideDir) + "/outside.epub",
		"Series/../../outside.epub",
		"Series/../Series/inside.epub",
		outside,
		"missing.epub",
		"",
	} {
		if f, err := l.File(ctx, rel); err == nil {
			t.Errorf("File(%q) = %+v, want it refused", rel, f)
		}
	}
}
