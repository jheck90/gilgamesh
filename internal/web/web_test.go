package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jheck90/gilgamesh/internal/store"
)

func TestChapterLabels(t *testing.T) {
	cases := []struct {
		title, part string
		only        bool
	}{
		{"Chapter 12", "Chapter 12", true},
		{"Chapter Twenty-Three", "Chapter Twenty-Three", true},
		{"XII", "XII", true},
		{"XXXIII The Fall of a Chieftain", "XXXIII", false},
		{"12. The Reveal", "12", false},
		{"The Fall of a Chieftain", "", false},
		{"Mild Mannered", "", false},
		{"Chapter", "", false},
	}
	for _, c := range cases {
		if got := numberPart(c.title); got != c.part {
			t.Errorf("numberPart(%q) = %q, want %q", c.title, got, c.part)
		}
		if got := onlyNumber(c.title); got != c.only {
			t.Errorf("onlyNumber(%q) = %v, want %v", c.title, got, c.only)
		}
	}
}

func setup(t *testing.T, trust string) (*httptest.Server, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid, _ := st.EnsureSeries(ctx, "Saga")
	chapters := []store.Chapter{
		{Idx: 3, Title: "I The Sea-dog", FirstOrd: 0, LastOrd: 1},
		{Idx: 4, Title: "II The Hooded Man Is Mordecai", FirstOrd: 2, LastOrd: 3},
		{Idx: 5, Title: "The Fall of a Chieftain", FirstOrd: 4, LastOrd: 4},
	}
	sents := []store.Sentence{
		{Ord: 0, ChapterIdx: 3, Text: "Carl woke."}, {Ord: 1, ChapterIdx: 3, Text: "It was Day 1."},
		{Ord: 2, ChapterIdx: 4, Text: "A hooded man watched."}, {Ord: 3, ChapterIdx: 4, Text: "He was Mordecai."},
		{Ord: 4, ChapterIdx: 5, Text: "The end."},
	}
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "The Crawl", SHA256: "x", SourcePath: "x"}, chapters, sents, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ApplyChunk(ctx, store.ChunkWrite{BookID: bid, SeriesID: sid, NextChunk: 1,
		New: []store.NewEntity{
			{Name: "Carl", Type: "character", FirstOrd: 0, Facts: []store.FactRec{{Text: "Carl woke up.", Ord: 0, Evidence: []int{0}}}},
			{Name: "Mordecai", Type: "character", FirstOrd: 3, Facts: []store.FactRec{{Text: "Mordecai wears a hood.", Ord: 3, Evidence: []int{2, 3}}}},
		},
		Markers: []store.MarkerRec{{Label: "Day 1", Ord: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer((&Server{Store: st, TrustHeader: trust, DefaultUser: "default"}).Handler())
	t.Cleanup(srv.Close)
	return srv, bid
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestReading(t *testing.T) {
	srv, _ := setup(t, "")
	c := srv.Client()

	code, body := get(t, c, srv.URL+"/b/1")
	if code != 200 || !strings.Contains(body, "Where are you?") {
		t.Fatalf("no position: %d\n%s", code, body)
	}
	if strings.Contains(body, "Mordecai") || strings.Contains(body, "Chieftain") {
		t.Errorf("future chapter titles leaked into the picker:\n%s", body)
	}
	if !strings.Contains(body, "II …") || !strings.Contains(body, "Section 5") {
		t.Errorf("future chapters should show numbers only:\n%s", body)
	}
	if code, _ := get(t, c, srv.URL+"/b/1/e/1"); code != 404 {
		t.Errorf("a card before setting a position returned %d", code)
	}

	res, err := c.PostForm(srv.URL+"/b/1/pos", url.Values{"chapter": {"3"}})
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("set position: %v %v", err, res.StatusCode)
	}
	_, body = get(t, c, srv.URL+"/b/1")
	if !strings.Contains(body, `href="/b/1/e/1"`) || strings.Contains(body, `href="/b/1/e/2"`) {
		t.Errorf("after chapter 3 the list should have Carl only:\n%s", body)
	}
	if !strings.Contains(body, "I finished II The Hooded Man Is Mordecai") {
		t.Errorf("the chapter being read should show its title:\n%s", body)
	}
	_, hidden := get(t, c, srv.URL+"/b/1/e/2")
	_, missing := get(t, c, srv.URL+"/b/1/e/999")
	if hidden != missing || !strings.Contains(hidden, "Nothing here yet.") {
		t.Errorf("a not-yet-met entity must look exactly like a missing one:\n%s\n---\n%s", hidden, missing)
	}
	_, body = get(t, c, srv.URL+"/b/1/timeline")
	if !strings.Contains(body, "Day 1") {
		t.Errorf("timeline:\n%s", body)
	}

	if _, err := c.PostForm(srv.URL+"/b/1/next", nil); err != nil {
		t.Fatal(err)
	}
	code, body = get(t, c, srv.URL+"/b/1/e/2")
	if code != 200 || !strings.Contains(body, "Mordecai wears a hood.") || !strings.Contains(body, "A hooded man watched.") {
		t.Errorf("Mordecai after chapter 4: %d\n%s", code, body)
	}
	_, body = get(t, c, srv.URL+"/b/1?q=mord")
	if !strings.Contains(body, `href="/b/1/e/2"`) || strings.Contains(body, `href="/b/1/e/1"`) {
		t.Errorf("search:\n%s", body)
	}
}

func TestTrustHeader(t *testing.T) {
	srv, _ := setup(t, "Remote-User")
	if code, _ := get(t, srv.Client(), srv.URL+"/"); code != 401 {
		t.Errorf("no header: got %d, want 401", code)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("Remote-User", "alice")
	res, err := srv.Client().Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Errorf("with header: %v %v", err, res.StatusCode)
	}
}
