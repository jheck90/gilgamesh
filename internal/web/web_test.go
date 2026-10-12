package web

import (
	"bytes"
	"context"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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

func setup(t *testing.T, trust string, opts ...func(*Server)) (*httptest.Server, int64) {
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
	return serve(t, st, trust, opts...), bid
}

// serve starts the web app over st, with fake photos.
func serve(t *testing.T, st *store.Store, trust string, opts ...func(*Server)) *httptest.Server {
	t.Helper()
	s := &Server{Store: st, TrustHeader: trust, DefaultUser: "default", Photos: fakePhotos{}}
	for _, o := range opts {
		o(s)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// pngHeader starts every fake photo, so the server takes it for an image.
const pngHeader = "\x89PNG\r\n\x1a\n"

// photo is a fake photo of a page showing text.
func photo(text string) []byte { return []byte(pngHeader + text) }

// fakePhotos stands in for Tesseract: it reads back the text a fake photo
// shows.
type fakePhotos struct{}

func (fakePhotos) ReadPhoto(ctx context.Context, photo []byte) (string, error) {
	return strings.TrimPrefix(string(photo), pngHeader), nil
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
	for _, pos := range []string{"no position", "with a position"} {
		if pos == "with a position" {
			send(t, srv, "", "/b/1/pos", url.Values{"chapter": {"3"}})
		}
		_, body := send(t, srv, "", "/b/1", nil)
		if !hasField(body) {
			t.Errorf("%s: no field to type the last 4 or more words:\n%s", pos, body)
		}
		if !hasCamera(body) {
			t.Errorf("%s: no photo input that opens the rear camera:\n%s", pos, body)
		}
	}
}

// hasCamera reports whether a page has a photo input that posts a snap
// and opens the rear camera on a phone.
func hasCamera(body string) bool {
	return strings.Contains(body, `enctype="multipart/form-data"`) &&
		regexp.MustCompile(`<input[^>]* name="photo" type="file" accept="image/\*" capture="environment"`).MatchString(body)
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

// snapPhoto uploads a photo to path the way the book page's photo input
// does, as the default reader, and returns the page it lands on.
func snapPhoto(t *testing.T, srv *httptest.Server, path string, photo []byte) (int, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("photo", "page.jpg")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(photo)
	mw.Close()
	res, err := srv.Client().Post(srv.URL+path, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func TestSnapByPhoto(t *testing.T) {
	// Sentences 2 and 3 reveal nothing. Carl is met in sentence 0, Day 1
	// is marked in 1, and Mordecai is met in 4.
	cases := []struct {
		name, page, where    string
		metCarl, metMordecai bool
	}{
		{"nothing revealed: to the end of the page",
			"“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.",
			"Partway through II The Hooded Man Is Mordecai", true, false},
		{"a sentence cut off at the bottom belongs to the next page",
			"“The tide always turns,” his mother used to say.\nA hooded man watched",
			"Finished I The Sea-dog", true, false},
		{"a reveal on the page: to the sentence before the page",
			"A hooded man watched from the dunes. “He’s Mordecai,” someone whispered.",
			"Finished I The Sea-dog", true, false},
		{"a reveal on the book's first page: to before the book starts",
			"Carl woke to the sound of gulls. It was Day 1.",
			"Partway through I The Sea-dog", false, false},
		{"a reveal only in a cut-off sentence: to the end of the page",
			"“The tide always turns,” his mother used to say. A hooded man watched from the dunes.\n“He’s Mordecai,”",
			"Partway through II The Hooded Man Is Mordecai", true, false},
		{"a running head and page number",
			"THE CRAWL                17\n“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.",
			"Partway through II The Hooded Man Is Mordecai", true, false},
		{"a word hyphenated across a line end",
			"“The tide always turns,” his mother used to say.\nA hooded man watched from the du-\nnes.",
			"Partway through II The Hooded Man Is Mordecai", true, false},
		{"OCR typos, down to the page's last words",
			"“Tbe tide always turns,’ his rnother used to say.\nA hooded man watched frorn the dunes.",
			"Partway through II The Hooded Man Is Mordecai", true, false},
		{"two facing pages",
			"16 THE CRAWL\n“The tide always turns,” his\nmother used to say.\n\nII 17\nA hooded man watched from the dunes.",
			"Partway through II The Hooded Man Is Mordecai", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			code, body := snapPhoto(t, srv, "/b/1/snap", photo(c.page))
			if code != 200 || !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("%d, want %q\n%s", code, c.where, body)
			}
			if met := strings.Contains(body, `href="/b/1/e/1"`); met != c.metCarl {
				t.Errorf("Carl listed = %v, want %v\n%s", met, c.metCarl, body)
			}
			if met := strings.Contains(body, `href="/b/1/e/2"`); met != c.metMordecai {
				t.Errorf("Mordecai listed = %v, want %v\n%s", met, c.metMordecai, body)
			}
			if strings.Contains(body, "Chieftain") {
				t.Errorf("a later chapter title leaked:\n%s", body)
			}
		})
	}
}

func TestPhotoSnapStopsBeforeEachKindOfReveal(t *testing.T) {
	// The page snapped is chapter 2, sentences 1 and 2. Ada is met in
	// sentence 0, and each case reveals one thing in sentence 2.
	ada := func(rs ...store.AliasRec) store.NewEntity {
		return store.NewEntity{Name: "Ada", Type: "character", FirstOrd: 0, Aliases: rs}
	}
	cases := []struct {
		name  string
		chunk store.ChunkWrite
		title bool // show Ada by her alias
		where string
	}{
		{"nothing revealed", store.ChunkWrite{New: []store.NewEntity{ada()}}, false, "Finished Chapter 2"},
		{"a fact", store.ChunkWrite{New: []store.NewEntity{{Name: "Ada", Type: "character", FirstOrd: 0,
			Facts: []store.FactRec{{Text: "Ada hears the ferry.", Ord: 2, Evidence: []int{2}}}}}}, false, "Finished Chapter 1"},
		{"an alias", store.ChunkWrite{New: []store.NewEntity{ada(store.AliasRec{Alias: "the keeper", Ord: 2})}}, false, "Finished Chapter 1"},
		{"a title alias", store.ChunkWrite{New: []store.NewEntity{ada(store.AliasRec{Alias: "Keeper Ada", Ord: 2})}}, true, "Finished Chapter 1"},
		{"a new entity", store.ChunkWrite{New: []store.NewEntity{ada(), {Name: "the ferry", Type: "thing", FirstOrd: 2}}}, false, "Finished Chapter 1"},
		{"a time marker", store.ChunkWrite{New: []store.NewEntity{ada()}, Markers: []store.MarkerRec{{Label: "Night 1", Ord: 2}}}, false, "Finished Chapter 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			sid, _ := st.EnsureSeries(ctx, "Lighthouse")
			bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "The Lamp", SHA256: "l", SourcePath: "l"},
				[]store.Chapter{
					{Idx: 1, Title: "Chapter 1", FirstOrd: 0, LastOrd: 0},
					{Idx: 2, Title: "Chapter 2", FirstOrd: 1, LastOrd: 2},
					{Idx: 3, Title: "Chapter 3", FirstOrd: 3, LastOrd: 3},
				},
				[]store.Sentence{
					{Ord: 0, ChapterIdx: 1, Text: "Ada climbed the lighthouse stairs at dusk."},
					{Ord: 1, ChapterIdx: 2, Text: "The lamp had to be lit before the ships came in."},
					{Ord: 2, ChapterIdx: 2, Text: "Below the cliff the ferry sounded its horn twice."},
					{Ord: 3, ChapterIdx: 3, Text: "Nobody answered the horn that night."},
				}, 1, 100)
			if err != nil {
				t.Fatal(err)
			}
			c.chunk.BookID, c.chunk.SeriesID, c.chunk.NextChunk = bid, sid, 1
			if _, err := st.ApplyChunk(ctx, c.chunk); err != nil {
				t.Fatal(err)
			}
			if c.title {
				b, _, _ := st.BookByID(ctx, bid)
				notes, err := st.BookNotes(ctx, b)
				if err != nil {
					t.Fatal(err)
				}
				e := notes.Entities[0]
				if _, err := st.ApplyEdit(ctx, store.Edit{BookID: bid, SeriesID: sid,
					Titles: []store.Title{{Entity: e.ID, Alias: e.Aliases[0].ID}}}); err != nil {
					t.Fatal(err)
				}
			}
			srv := serve(t, st, "")
			_, body := snapPhoto(t, srv, "/b/1/snap", photo(
				"The lamp had to be lit before the ships came in.\nBelow the cliff the ferry sounded its horn twice."))
			if !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("want %q\n%s", c.where, body)
			}
		})
	}
}

// slowPhotos takes longer to read a photo than the server waits.
type slowPhotos struct{}

func (slowPhotos) ReadPhoto(ctx context.Context, photo []byte) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// brokenPhotos fails like a server without the tesseract program.
type brokenPhotos struct{}

func (brokenPhotos) ReadPhoto(ctx context.Context, photo []byte) (string, error) {
	return "", &exec.Error{Name: "tesseract", Err: exec.ErrNotFound}
}

func TestPhotoSnapThatFails(t *testing.T) {
	page := photo("“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.")
	cases := []struct {
		name    string
		server  func(*Server)
		photo   []byte
		code    int
		problem string
	}{
		{"unreadable text", nil, photo("~ |l1 ,, .\n\nrn ii Tr"), 200, "Couldn't find this page"},
		{"a file that isn't an image", nil, []byte("Dear diary, today the tide turned."), 415, "isn’t a photo"},
		{"a photo over the size cap", func(s *Server) { s.MaxPhoto = 64 }, append(page, bytes.Repeat([]byte(" "), 64)...), 413, "too big"},
		{"a photo that takes too long to read", func(s *Server) { s.Photos, s.PhotoTimeout = slowPhotos{}, time.Millisecond }, page, 503, "took too long"},
		{"no tesseract program", func(s *Server) { s.Photos = brokenPhotos{} }, page, 503, "Couldn’t read that photo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var opts []func(*Server)
			if c.server != nil {
				opts = append(opts, c.server)
			}
			srv, _ := setup(t, "", opts...)
			send(t, srv, "", "/b/1/pos", url.Values{"chapter": {"3"}})
			code, body := snapPhoto(t, srv, "/b/1/snap", c.photo)
			if code != c.code || !strings.Contains(body, c.problem) {
				t.Errorf("got %d, want %d and %q:\n%s", code, c.code, c.problem, body)
			}
			if !strings.Contains(body, `action="/b/1/snap"`) || !strings.Contains(body, `name="words"`) {
				t.Errorf("no field to type the last words read instead:\n%s", body)
			}
			if !hasCamera(body) || !strings.Contains(body, `action="/b/1/pos"`) {
				t.Errorf("no way to retake the photo or pick a chapter:\n%s", body)
			}
			if _, body := send(t, srv, "", "/b/1", nil); !strings.Contains(body, `<p class="where">Finished I The Sea-dog</p>`) {
				t.Errorf("the position moved:\n%s", body)
			}
			// Typed words still work.
			if _, body := send(t, srv, "", "/b/1/snap", url.Values{"words": {"a hooded man watched"}}); !strings.Contains(body, `<p class="where">Partway through II The Hooded Man Is Mordecai</p>`) {
				t.Errorf("typed words after a failed photo:\n%s", body)
			}
		})
	}
}

func TestPhotoSnapsLogNoText(t *testing.T) {
	var logged bytes.Buffer
	out := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(out) })

	srv, _ := setup(t, "")
	broken, _ := setup(t, "", func(s *Server) { s.Photos = brokenPhotos{} })
	snapPhoto(t, srv, "/b/1/snap", photo("“The tide always turns,” his mother used to say."))
	snapPhoto(t, srv, "/b/1/snap", photo("A postcard from Zanzibar, wish you were here."))
	snapPhoto(t, broken, "/b/1/snap", photo("A postcard from Zanzibar, wish you were here."))
	if !strings.Contains(logged.String(), "tesseract") {
		t.Errorf("a broken photo reader isn't logged for the operator:\n%s", &logged)
	}
	for _, w := range []string{"tide", "mother", "postcard", "Zanzibar"} {
		if strings.Contains(logged.String(), w) {
			t.Errorf("text from a photo was logged:\n%s", &logged)
		}
	}
}
