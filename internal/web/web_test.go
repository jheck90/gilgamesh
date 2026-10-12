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
	// Sections 1 and 2 (front matter) were skipped at ingest, so the
	// chapters start at 3 and none of their text is stored.
	chapters := []store.Chapter{
		{Idx: 3, Title: "I The Sea-dog", FirstOrd: 0, LastOrd: 2},
		{Idx: 4, Title: "II The Hooded Man Is Mordecai", FirstOrd: 3, LastOrd: 5},
		{Idx: 5, Title: "The Fall of a Chieftain", FirstOrd: 6, LastOrd: 6},
	}
	sents := []store.Sentence{
		{Ord: 0, ChapterIdx: 3, Text: "Carl woke to the sound of gulls."},
		{Ord: 1, ChapterIdx: 3, Text: "It was Day 1."},
		{Ord: 2, ChapterIdx: 3, Text: "“The tide always turns,” his mother used to say."},
		{Ord: 3, ChapterIdx: 4, Text: "A hooded man watched from the dunes."},
		{Ord: 4, ChapterIdx: 4, Text: "“He’s Mordecai,” someone whispered."},
		{Ord: 5, ChapterIdx: 4, Text: "The tide always turns."},
		{Ord: 6, ChapterIdx: 5, Text: "The end came quietly."},
	}
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "The Crawl", SHA256: "x", SourcePath: "x"}, chapters, sents, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ApplyChunk(ctx, store.ChunkWrite{BookID: bid, SeriesID: sid, NextChunk: 1,
		New: []store.NewEntity{
			{Name: "Carl", Type: "character", FirstOrd: 0, Facts: []store.FactRec{{Text: "Carl woke up.", Ord: 0, Evidence: []int{0}}}},
			{Name: "Mordecai", Type: "character", FirstOrd: 4, Facts: []store.FactRec{{Text: "Mordecai wears a hood.", Ord: 4, Evidence: []int{3, 4}}}},
		},
		Markers: []store.MarkerRec{{Label: "Day 1", Ord: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 2, Title: "The Dungeon", SHA256: "y", SourcePath: "y"},
		[]store.Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: 0}},
		[]store.Sentence{{Ord: 0, ChapterIdx: 1, Text: "Donut barked at the mailbox all night."}}, 1, 100)
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
	if code != 200 || !strings.Contains(body, "Mordecai wears a hood.") || !strings.Contains(body, "A hooded man watched from the dunes.") {
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

// send makes a request as the named reader ("" sends no trusted header),
// posting form when it isn't nil, and returns the page it lands on.
func send(t *testing.T, srv *httptest.Server, reader, path string, form url.Values) (int, string) {
	t.Helper()
	method, body := "GET", io.Reader(nil)
	if form != nil {
		method, body = "POST", strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if reader != "" {
		req.Header.Set("Remote-User", reader)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSnapFieldOnBookPage(t *testing.T) {
	srv, _ := setup(t, "")
	hasField := func(body string) bool {
		return strings.Contains(body, `action="/b/1/snap"`) && strings.Contains(body, `name="words"`) &&
			strings.Contains(body, "4 or more words you read")
	}
	if _, body := send(t, srv, "", "/b/1", nil); !hasField(body) {
		t.Errorf("no position: no field to type the last 4 or more words:\n%s", body)
	}
	send(t, srv, "", "/b/1/pos", url.Values{"chapter": {"3"}})
	if _, body := send(t, srv, "", "/b/1", nil); !hasField(body) {
		t.Errorf("with a position: no field to type the last 4 or more words:\n%s", body)
	}
}

func TestSnapByTypedWords(t *testing.T) {
	// Mordecai is named in sentence 4, so his card shows whether a snap
	// stopped at the sentence before or got to the one naming him.
	cases := []struct {
		name, words, where string
		metMordecai        bool
	}{
		{"words found once", "a hooded man watched", "Partway through II The Hooded Man Is Mordecai", false},
		{"words across two sentences", "watched from the dunes. He's Mordecai", "Partway through II The Hooded Man Is Mordecai", true},
		{"case, quotes and spaces", `  "HE'S   mordecai,"  SOMEONE  whispered `, "Partway through II The Hooded Man Is Mordecai", true},
		// The refrain ends chapters I and II alike.
		{"earliest of two equal matches", "the tide always turns", "Finished I The Sea-dog", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			code, body := send(t, srv, "", "/b/1/snap", url.Values{"words": {c.words}})
			if code != 200 || !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("snap %q: %d, want %q\n%s", c.words, code, c.where, body)
			}
			if met := strings.Contains(body, `href="/b/1/e/2"`); met != c.metMordecai {
				t.Errorf("snap %q: Mordecai listed = %v, want %v\n%s", c.words, met, c.metMordecai, body)
			}
			if strings.Contains(body, "Chieftain") {
				t.Errorf("snap %q: a later chapter title leaked:\n%s", c.words, body)
			}
			if code, _ := send(t, srv, "", "/b/1/e/2", nil); !c.metMordecai && code != 404 {
				t.Errorf("snap %q: Mordecai's card returned %d before he's named", c.words, code)
			}
		})
	}
}

func TestSnapWithNoMatch(t *testing.T) {
	cases := []struct{ name, words string }{
		{"words not in the book", "the quick brown fox jumps over"},
		// Front matter skipped at ingest isn't stored, so it can't match.
		{"words from a skipped section", "praise for the crawl a gripping read"},
		{"a word or two", "the tide"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			send(t, srv, "", "/b/1/pos", url.Values{"chapter": {"3"}})
			code, body := send(t, srv, "", "/b/1/snap", url.Values{"words": {c.words}})
			if code != 200 || !strings.Contains(body, "Couldn't find this page") {
				t.Fatalf("snap %q: %d\n%s", c.words, code, body)
			}
			if !strings.Contains(body, `action="/b/1/snap"`) || !strings.Contains(body, `value="`+c.words+`"`) {
				t.Errorf("snap %q: no field to type the words again:\n%s", c.words, body)
			}
			if !strings.Contains(body, `action="/b/1/pos"`) || strings.Contains(body, "Chieftain") {
				t.Errorf("snap %q: no chapter picker, or a later title in it:\n%s", c.words, body)
			}
			if _, body := send(t, srv, "", "/b/1", nil); !strings.Contains(body, `<p class="where">Finished I The Sea-dog</p>`) {
				t.Errorf("snap %q moved the position:\n%s", c.words, body)
			}
		})
	}
}

func TestSnapSearchesOnlyThisBook(t *testing.T) {
	srv, _ := setup(t, "")
	words := url.Values{"words": {"donut barked at the mailbox"}} // from book 2
	if _, body := send(t, srv, "", "/b/1/snap", words); !strings.Contains(body, "Couldn't find this page") {
		t.Errorf("words from book 2 matched in book 1:\n%s", body)
	}
	if _, body := send(t, srv, "", "/b/2/snap", words); !strings.Contains(body, `<p class="where">Finished One</p>`) {
		t.Errorf("words from book 2 didn't match in book 2:\n%s", body)
	}
}

func TestSnapMovesOnlyThatReader(t *testing.T) {
	srv, _ := setup(t, "Remote-User")
	words := url.Values{"words": {"a hooded man watched"}}
	if code, _ := send(t, srv, "", "/b/1/snap", words); code != 401 {
		t.Errorf("snap without the header: got %d, want 401", code)
	}
	send(t, srv, "bob", "/b/1/pos", url.Values{"chapter": {"3"}})
	if _, body := send(t, srv, "alice", "/b/1/snap", words); !strings.Contains(body, `<p class="where">Partway through II The Hooded Man Is Mordecai</p>`) {
		t.Errorf("alice's snap:\n%s", body)
	}
	if _, body := send(t, srv, "bob", "/b/1", nil); !strings.Contains(body, `<p class="where">Finished I The Sea-dog</p>`) {
		t.Errorf("alice's snap moved bob:\n%s", body)
	}
}
