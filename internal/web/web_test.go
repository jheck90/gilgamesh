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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jheck90/gilgamesh/internal/store"
	"golang.org/x/net/html"
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

// crawl is the text of the first book setup builds.
var crawl = []store.Sentence{
	{Ord: 0, ChapterIdx: 3, Text: "Carl woke to the sound of gulls."},
	{Ord: 1, ChapterIdx: 3, Text: "It was Day 1."},
	{Ord: 2, ChapterIdx: 3, Text: "“The tide always turns,” his mother used to say."},
	{Ord: 3, ChapterIdx: 4, Text: "A hooded man watched from the dunes."},
	{Ord: 4, ChapterIdx: 4, Text: "“He’s Mordecai,” someone whispered."},
	{Ord: 5, ChapterIdx: 4, Text: "The tide always turns."},
	{Ord: 6, ChapterIdx: 5, Text: "The end came quietly."},
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
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "The Crawl", SHA256: "x", SourcePath: "x"}, chapters, crawl, 1, 100)
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
	// stopped at the sentence before or got to the one naming him. Typed
	// words never ask how far down the page the reader has got, even past a
	// reveal: the reader has already said.
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
	// Sentences 2 and 3 reveal nothing, so none of these pages asks how far
	// down the reader has got. Carl is met in sentence 0 and Mordecai in 4.
	cases := []struct{ name, page, where string }{
		{"nothing revealed: to the end of the page",
			"“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.",
			"Partway through II The Hooded Man Is Mordecai"},
		{"a sentence cut off at the bottom belongs to the next page",
			"“The tide always turns,” his mother used to say.\nA hooded man watched",
			"Finished I The Sea-dog"},
		{"a reveal only in a cut-off sentence: to the end of the page",
			"“The tide always turns,” his mother used to say. A hooded man watched from the dunes.\n“He’s Mordecai,”",
			"Partway through II The Hooded Man Is Mordecai"},
		{"a running head and page number",
			"THE CRAWL                17\nturns,” his mother used to say.\nA hooded man watched from the dunes.",
			"Partway through II The Hooded Man Is Mordecai"},
		{"a word hyphenated across a line end",
			"“The tide always turns,” his mother used to say.\nA hooded man watched from the du-\nnes.",
			"Partway through II The Hooded Man Is Mordecai"},
		{"misread words, down to the page's last words",
			"“Tbe tide always turns,’ his rnother used to say.\nA hooded man watched frorn the dunes.",
			"Partway through II The Hooded Man Is Mordecai"},
		{"two facing pages",
			"16 THE CRAWL\nturns,” his\nmother used to say.\n\nII 17\nA hooded man watched from the dunes.",
			"Partway through II The Hooded Man Is Mordecai"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			code, body := snapPhoto(t, srv, "/b/1/snap", photo(c.page))
			if code != 200 || !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("%d, want %q\n%s", code, c.where, body)
			}
			if !strings.Contains(body, `href="/b/1/e/1"`) || strings.Contains(body, `href="/b/1/e/2"`) {
				t.Errorf("want Carl listed and Mordecai not:\n%s", body)
			}
			if strings.Contains(body, "Chieftain") {
				t.Errorf("a later chapter title leaked:\n%s", body)
			}
		})
	}
}

// notStarted answers the line question for a reader who hasn't read any of
// the page yet.
const notStarted = "I haven’t started this page"

func TestPhotoOfAPageWithARevealAsksForTheLine(t *testing.T) {
	// Carl is met in sentence 0, Day 1 is marked in 1, and Mordecai is met
	// in 4.
	cases := []struct {
		name, page  string
		first, last int // the sentences asked about
	}{
		{"a reveal on the page",
			"A hooded man watched from the dunes. “He’s Mordecai,” someone whispered.", 3, 4},
		{"a reveal on the book's first page",
			"Carl woke to the sound of gulls. It was Day 1.", 0, 1},
		{"a sentence cut off at the bottom isn't asked about",
			"A hooded man watched from the dunes. “He’s Mordecai,” someone whispered.\nThe tide always", 3, 4},
		// Words before the match may be the misread end of the sentence
		// before, so the page may hold that sentence's reveal. A running
		// head can't be told from them.
		{"misread words at the top: the sentence before may be on the page",
			"Ii vvas Dav l. “The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.", 1, 3},
		{"a running head over a page that starts a sentence: so may the one before",
			"THE CRAWL 17\n“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.", 1, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			code, body := snapPhoto(t, srv, "/b/1/snap", photo(c.page))
			var want []string
			for _, se := range crawl[c.first : c.last+1] {
				want = append(want, se.Text)
			}
			want = append(want, notStarted)
			if got := labels(buttons(t, body)); code != 200 || !slices.Equal(got, want) {
				t.Errorf("%d, asked\n%q\nwant\n%q\n%s", code, got, want, body)
			}
			for i, se := range crawl {
				if (i < c.first || i > c.last) && strings.Contains(body, se.Text) {
					t.Errorf("sentence %d isn't on the page but was shown:\n%s", i, body)
				}
			}
			if strings.Contains(body, "/b/1/e/") || strings.Contains(body, "Chieftain") {
				t.Errorf("the question showed a card or a later chapter title:\n%s", body)
			}
			if _, body := send(t, srv, "", "/b/1", nil); strings.Contains(body, `class="where"`) {
				t.Errorf("asking moved the position:\n%s", body)
			}
		})
	}
}

func TestAnsweringTheLineQuestion(t *testing.T) {
	// Carl is met in sentence 0, Day 1 is marked in 1, and Mordecai is met
	// in 4.
	mordecai := "A hooded man watched from the dunes. “He’s Mordecai,” someone whispered."
	cases := []struct {
		name, page, answer, where string
		metCarl, metMordecai      bool
	}{
		{"a sentence before the reveal: the reveal stays hidden", mordecai,
			"A hooded man watched from the dunes.", "Partway through II The Hooded Man Is Mordecai", true, false},
		{"the sentence with the reveal", mordecai,
			"“He’s Mordecai,” someone whispered.", "Partway through II The Hooded Man Is Mordecai", true, true},
		{"not started: to the sentence before the page", mordecai,
			notStarted, "Finished I The Sea-dog", true, false},
		{"not started on the book's first page: to before the book starts", "Carl woke to the sound of gulls. It was Day 1.",
			notStarted, "Partway through I The Sea-dog", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			_, question := snapPhoto(t, srv, "/b/1/snap", photo(c.page))
			code, body := tap(t, srv, question, c.answer)
			if code != 200 || !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("%d, want %q\n%s", code, c.where, body)
			}
			if met := strings.Contains(body, `href="/b/1/e/1"`); met != c.metCarl {
				t.Errorf("Carl listed = %v, want %v\n%s", met, c.metCarl, body)
			}
			if met := strings.Contains(body, `href="/b/1/e/2"`); met != c.metMordecai {
				t.Errorf("Mordecai listed = %v, want %v\n%s", met, c.metMordecai, body)
			}
			if code, _ := send(t, srv, "", "/b/1/e/2", nil); !c.metMordecai && code != 404 {
				t.Errorf("Mordecai's card returned %d before he's named", code)
			}
		})
	}
}

func TestLineAnswerOffThePageIsRejected(t *testing.T) {
	srv, _ := setup(t, "")
	send(t, srv, "", "/b/1/pos", url.Values{"chapter": {"3"}})
	// The page is sentences 3 and 4, so the answers are 2 (not started) to 4.
	_, question := snapPhoto(t, srv, "/b/1/snap", photo("A hooded man watched from the dunes. “He’s Mordecai,” someone whispered."))
	b := buttons(t, question)[0]
	cases := []struct {
		name string
		set  url.Values
	}{
		{"a sentence after the page", url.Values{"ord": {"5"}}},
		{"a sentence before the page", url.Values{"ord": {"1"}}},
		{"no sentence", url.Values{"ord": {""}}},
		{"no page", url.Values{"first": {""}, "last": {""}}},
		{"a page that ends before it starts", url.Values{"first": {"4"}, "last": {"3"}, "ord": {"3"}}},
		{"a page past the end of the book", url.Values{"first": {"3"}, "last": {"99"}, "ord": {"50"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			form := url.Values{}
			for k, v := range b.form {
				form[k] = v
			}
			for k, v := range c.set {
				form[k] = v
			}
			if code, body := send(t, srv, "", b.action, form); code != 400 {
				t.Errorf("got %d, want 400\n%s", code, body)
			}
			if _, body := send(t, srv, "", "/b/1", nil); !strings.Contains(body, `<p class="where">Finished I The Sea-dog</p>`) {
				t.Errorf("the position moved:\n%s", body)
			}
		})
	}
}

// button is a submit button on a page, with the form it sends.
type button struct {
	label, action string
	form          url.Values
}

// buttons lists a page's submit buttons in order.
func buttons(t *testing.T, page string) []button {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	var out []button
	var form *html.Node
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		switch n.Data {
		case "form":
			form = n
		case "button":
			if form == nil || attrOf(n, "type") == "button" {
				continue
			}
			b := button{label: strings.TrimSpace(textOf(n)), action: attrOf(form, "action"), form: url.Values{}}
			for in := range form.Descendants() {
				if in.Type == html.ElementNode && in.Data == "input" && attrOf(in, "type") != "file" && attrOf(in, "name") != "" {
					b.form.Set(attrOf(in, "name"), attrOf(in, "value"))
				}
			}
			if name := attrOf(n, "name"); name != "" {
				b.form.Set(name, attrOf(n, "value"))
			}
			out = append(out, b)
		}
	}
	return out
}

// attrOf is an element's attribute key, or "" without one.
func attrOf(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// textOf is all the text inside n.
func textOf(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := range n.ChildNodes() {
		b.WriteString(textOf(c))
	}
	return b.String()
}

// labels is what each button says.
func labels(bs []button) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.label)
	}
	return out
}

// tap taps the button labelled label on page, as the default reader, and
// returns the page it lands on.
func tap(t *testing.T, srv *httptest.Server, page, label string) (int, string) {
	t.Helper()
	for _, b := range buttons(t, page) {
		if b.label == label {
			return send(t, srv, "", b.action, b.form)
		}
	}
	t.Fatalf("no button %q on the page:\n%s", label, page)
	return 0, ""
}

func TestEachKindOfRevealAsksForTheLine(t *testing.T) {
	// The page snapped is chapter 2, sentences 1 and 2. Ada is met in
	// sentence 0, and each case puts one reveal in sentence 2, which the
	// reader sees at path once they've read that far.
	ada := func(rs ...store.AliasRec) store.NewEntity {
		return store.NewEntity{Name: "Ada", Type: "character", FirstOrd: 0, Aliases: rs}
	}
	cases := []struct {
		name         string
		chunk        store.ChunkWrite
		title        bool // show Ada by her alias
		path, reveal string
	}{
		{"a fact", store.ChunkWrite{New: []store.NewEntity{{Name: "Ada", Type: "character", FirstOrd: 0,
			Facts: []store.FactRec{{Text: "Ada hears the ferry.", Ord: 2, Evidence: []int{2}}}}}}, false, "/b/1/e/1", "Ada hears the ferry."},
		{"an alias", store.ChunkWrite{New: []store.NewEntity{ada(store.AliasRec{Alias: "the keeper", Ord: 2})}}, false, "/b/1", "the keeper"},
		{"a title alias", store.ChunkWrite{New: []store.NewEntity{ada(store.AliasRec{Alias: "Keeper Ada", Ord: 2})}}, true, "/b/1", "Keeper Ada"},
		{"a new entity", store.ChunkWrite{New: []store.NewEntity{ada(), {Name: "the ferry", Type: "term", FirstOrd: 2}}}, false, "/b/1", "the ferry"},
		{"a time marker", store.ChunkWrite{New: []store.NewEntity{ada()}, Markers: []store.MarkerRec{{Label: "Night 1", Ord: 2}}}, false, "/b/1/timeline", "Night 1"},
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
			lit, horn := "The lamp had to be lit before the ships came in.", "Below the cliff the ferry sounded its horn twice."
			page := photo(lit + "\n" + horn)
			_, body := snapPhoto(t, srv, "/b/1/snap", page)
			if got, want := labels(buttons(t, body)), []string{lit, horn, notStarted}; !slices.Equal(got, want) {
				t.Fatalf("asked %q, want %q\n%s", got, want, body)
			}
			if _, body := tap(t, srv, body, lit); !strings.Contains(body, `<p class="where">Partway through Chapter 2</p>`) {
				t.Errorf("tapped the first sentence:\n%s", body)
			}
			if _, body := send(t, srv, "", c.path, nil); strings.Contains(body, c.reveal) {
				t.Errorf("%q shows before its sentence is read:\n%s", c.reveal, body)
			}
			_, body = snapPhoto(t, srv, "/b/1/snap", page)
			tap(t, srv, body, horn)
			if _, body := send(t, srv, "", c.path, nil); !strings.Contains(body, c.reveal) {
				t.Errorf("%q doesn't show once its sentence is read:\n%s", c.reveal, body)
			}
		})
	}
}

func TestSnapBehindThePositionAsksFirst(t *testing.T) {
	// Each snap lands behind where the reader has got to, so it asks before
	// moving them back, naming both places by chapter.
	cases := []struct {
		name, saved string // typed words that set the position first
		snap        snapping
		question    string
		where       string // once the reader taps "Move back"
	}{
		{"typed words in an earlier chapter", "he's mordecai someone whispered",
			typing("carl woke to the sound"),
			"You’re partway through II The Hooded Man Is Mordecai. Move back to I The Sea-dog?",
			"Partway through I The Sea-dog"},
		{"a sentence tapped on the line question", "he's mordecai someone whispered",
			tapping("Carl woke to the sound of gulls. It was Day 1.", "Carl woke to the sound of gulls."),
			"You’re partway through II The Hooded Man Is Mordecai. Move back to I The Sea-dog?",
			"Partway through I The Sea-dog"},
		{"a photo of a page that ends a chapter", "he's mordecai someone whispered",
			photographing("“The tide always turns,” his mother used to say.\nA hooded man watched"),
			"You’re partway through II The Hooded Man Is Mordecai. Move back to the end of I The Sea-dog?",
			"Finished I The Sea-dog"},
		{"typed words earlier in the same chapter", "someone whispered. The tide always turns",
			typing("a hooded man watched"),
			"You’ve finished II The Hooded Man Is Mordecai. Move back to earlier in II The Hooded Man Is Mordecai?",
			"Partway through II The Hooded Man Is Mordecai"},
		{"not started on the book's first page", "he's mordecai someone whispered",
			tapping("Carl woke to the sound of gulls. It was Day 1.", notStarted),
			"You’re partway through II The Hooded Man Is Mordecai. Move back to I The Sea-dog?",
			"Partway through I The Sea-dog"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			_, body := send(t, srv, "", "/b/1/snap", url.Values{"words": {c.saved}})
			was := regexp.MustCompile(`<p class="where">[^<]*</p>`).FindString(body)
			question := c.snap(t, srv)
			if !strings.Contains(question, c.question) {
				t.Errorf("want the question %q:\n%s", c.question, question)
			}
			if strings.Contains(question, "/b/1/e/") || strings.Contains(question, "Chieftain") {
				t.Errorf("the question showed a card or a later chapter title:\n%s", question)
			}
			if _, body := send(t, srv, "", "/b/1", nil); !strings.Contains(body, was) {
				t.Errorf("asking moved the position from %s:\n%s", was, body)
			}
			if _, body := follow(t, srv, question, "Keep my place"); !strings.Contains(body, was) {
				t.Errorf("keeping my place moved the position from %s:\n%s", was, body)
			}
			if _, body := tap(t, srv, question, "Move back"); !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("moving back: want %q\n%s", c.where, body)
			}
		})
	}
}

func TestMoveBackOffTheBookIsRejected(t *testing.T) {
	srv, _ := setup(t, "")
	send(t, srv, "", "/b/1/pos", url.Values{"chapter": {"4"}})
	_, question := send(t, srv, "", "/b/1/snap", url.Values{"words": {"carl woke to the sound"}})
	bs := buttons(t, question)
	i := slices.IndexFunc(bs, func(b button) bool { return b.label == "Move back" })
	if i < 0 {
		t.Fatalf("no Move back button:\n%s", question)
	}
	// The book is sentences 0 to 6, and -1 is before it starts.
	for _, ord := range []string{"", "one", "-2", "7"} {
		t.Run(ord, func(t *testing.T) {
			if code, body := send(t, srv, "", bs[i].action, url.Values{"ord": {ord}}); code != 400 {
				t.Errorf("got %d, want 400\n%s", code, body)
			}
			if _, body := send(t, srv, "", "/b/1", nil); !strings.Contains(body, `<p class="where">Finished II The Hooded Man Is Mordecai</p>`) {
				t.Errorf("the position moved:\n%s", body)
			}
		})
	}
}

func TestSnapAheadOrToTheSamePlaceDoesntAsk(t *testing.T) {
	// Snaps with no position saved are in the tests above.
	mordecai := "A hooded man watched from the dunes. “He’s Mordecai,” someone whispered."
	cases := []struct {
		name, saved string // typed words that set the position first
		snap        snapping
		where       string
	}{
		{"typed words ahead", "carl woke to the sound",
			typing("a hooded man watched"), "Partway through II The Hooded Man Is Mordecai"},
		{"a photo ahead", "carl woke to the sound",
			photographing("“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes."),
			"Partway through II The Hooded Man Is Mordecai"},
		{"a sentence tapped ahead", "carl woke to the sound",
			tapping(mordecai, "“He’s Mordecai,” someone whispered."), "Partway through II The Hooded Man Is Mordecai"},
		{"typed words where the reader already is", "a hooded man watched",
			typing("a hooded man watched"), "Partway through II The Hooded Man Is Mordecai"},
		{"a sentence tapped where the reader already is", "a hooded man watched",
			tapping(mordecai, "A hooded man watched from the dunes."), "Partway through II The Hooded Man Is Mordecai"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setup(t, "")
			send(t, srv, "", "/b/1/snap", url.Values{"words": {c.saved}})
			body := c.snap(t, srv)
			if strings.Contains(body, "Move back") || !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("want %q without asking:\n%s", c.where, body)
			}
		})
	}
}

// snapping is one way of snapping a page of the first book, as the default
// reader. It returns the page the snap lands on.
type snapping func(t *testing.T, srv *httptest.Server) string

// typing snaps by typing words.
func typing(words string) snapping {
	return func(t *testing.T, srv *httptest.Server) string {
		_, body := send(t, srv, "", "/b/1/snap", url.Values{"words": {words}})
		return body
	}
}

// photographing snaps a photo of a page showing text.
func photographing(text string) snapping {
	return func(t *testing.T, srv *httptest.Server) string {
		_, body := snapPhoto(t, srv, "/b/1/snap", photo(text))
		return body
	}
}

// tapping snaps a photo of a page showing text, then taps label on the
// line question it asks.
func tapping(text, label string) snapping {
	return func(t *testing.T, srv *httptest.Server) string {
		_, body := tap(t, srv, photographing(text)(t, srv), label)
		return body
	}
}

// follow follows the link labelled label on page, as the default reader,
// and returns the page it lands on.
func follow(t *testing.T, srv *httptest.Server, page, label string) (int, string) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "a" && strings.TrimSpace(textOf(n)) == label {
			return send(t, srv, "", attrOf(n, "href"), nil)
		}
	}
	t.Fatalf("no link %q on the page:\n%s", label, page)
	return 0, ""
}

// lighthouse starts the web app over a book whose pages name people in
// different ways: Ada by name, and Tom by name and as "the stranger".
func lighthouse(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid, _ := st.EnsureSeries(ctx, "Lighthouse")
	bid, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "The Lamp", SHA256: "l", SourcePath: "l"},
		[]store.Chapter{
			{Idx: 1, Title: "Chapter 1", FirstOrd: 0, LastOrd: 1},
			{Idx: 2, Title: "Chapter 2", FirstOrd: 2, LastOrd: 7},
			{Idx: 3, Title: "The Stranger Was Tom", FirstOrd: 8, LastOrd: 8},
		},
		[]store.Sentence{
			{Ord: 0, ChapterIdx: 1, Text: "Ada climbed the lighthouse stairs at dusk."},
			{Ord: 1, ChapterIdx: 1, Text: "Tom rowed out to the rocks with the post."},
			{Ord: 2, ChapterIdx: 2, Text: "The wind dropped and the sea went flat and grey."},
			{Ord: 3, ChapterIdx: 2, Text: "Ada said the lamp had to be lit before the ships came in."},
			{Ord: 4, ChapterIdx: 2, Text: "From the gallery she watched the ferry come round the point."},
			{Ord: 5, ChapterIdx: 2, Text: "A stranger in grey stepped off onto the quay."},
			{Ord: 6, ChapterIdx: 2, Text: "Ada knew him at once: it was Tom, home from the sea."},
			{Ord: 7, ChapterIdx: 2, Text: "After that night the village called her Keeper Ada."},
			{Ord: 8, ChapterIdx: 3, Text: "Nobody answered the horn that night."},
		}, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.ApplyChunk(ctx, store.ChunkWrite{BookID: bid, SeriesID: sid, NextChunk: 1,
		New: []store.NewEntity{
			{Name: "Ada", Type: "character", FirstOrd: 0,
				Aliases: []store.AliasRec{{Alias: "Keeper Ada", Ord: 7}},
				Facts: []store.FactRec{
					{Text: "Ada climbs to the lamp at dusk.", Ord: 0, Evidence: []int{0}},
					{Text: "Ada watches for the ferry.", Ord: 4, Evidence: []int{4}},
					{Text: "The village calls Ada its keeper.", Ord: 7, Evidence: []int{7}},
				}},
			{Name: "Tom", Type: "character", FirstOrd: 1,
				Aliases: []store.AliasRec{{Alias: "the stranger", Ord: 6}},
				Facts:   []store.FactRec{{Text: "Tom rows the post out to the rocks.", Ord: 1, Evidence: []int{1}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.BookByID(ctx, bid)
	notes, err := st.BookNotes(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	ada := notes.Entities[0]
	if _, err := st.ApplyEdit(ctx, store.Edit{BookID: bid, SeriesID: sid,
		Titles: []store.Title{{Entity: ada.ID, Alias: ada.Aliases[0].ID}}}); err != nil {
		t.Fatal(err)
	}
	return serve(t, st, "")
}

// lighthouseFacts are the facts on each card in the lighthouse book, and
// the sentence that reveals each.
var lighthouseFacts = map[string]map[string]int{
	"/b/1/e/1": {"Ada climbs to the lamp at dusk.": 0, "Ada watches for the ferry.": 4, "The village calls Ada its keeper.": 7},
	"/b/1/e/2": {"Tom rows the post out to the rocks.": 1},
}

// listed is a card listed on the page a snap lands on: its title and the
// link to it.
type listed struct{ title, href string }

// onThisPage is the "On this page" section of a page: the cards it lists
// in order and everything it says. ok is false without one.
func onThisPage(t *testing.T, page string) (cards []listed, says string, ok bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode || n.Data != "section" || attrOf(n, "class") != "page" {
			continue
		}
		for a := range n.Descendants() {
			if a.Type != html.ElementNode || a.Data != "a" {
				continue
			}
			c := listed{href: attrOf(a, "href")}
			for s := range a.Descendants() {
				if s.Type == html.ElementNode && attrOf(s, "class") == "name" {
					c.title = strings.TrimSpace(textOf(s))
				}
			}
			cards = append(cards, c)
		}
		return cards, strings.Join(strings.Fields(textOf(n)), " "), true
	}
	return nil, "", false
}

func TestCardsAfterASnap(t *testing.T) {
	// Ada is met in sentence 0 and Tom in 1. Tom is "the stranger" from 6
	// on, and Ada's card is titled "Keeper Ada" from 7.
	ada, tom := listed{"Ada", "/b/1/e/1"}, listed{"Tom", "/b/1/e/2"}
	// The page holds a fact in 4 and an alias in 6, so a photo of it asks
	// how far down the reader has got.
	asks := "Ada said the lamp had to be lit before the ships came in.\nFrom the gallery she watched the ferry come round the point.\n" +
		"A stranger in grey stepped off onto the quay.\nAda knew him at once: it was Tom, home from the sea."
	cases := []struct {
		name  string
		snap  snapping
		where string
		pos   int
		cards []listed
	}{
		{"typed words: who they name", typing("ada said the lamp had to be lit"),
			"Partway through Chapter 2", 3, []listed{ada}},
		{"a photo with nothing revealed lands on the same view",
			photographing("The wind dropped and the sea went flat and grey.\nAda said the lamp had to be lit before the ships came in."),
			"Partway through Chapter 2", 3, []listed{ada}},
		{"named only after the sentence tapped: not listed",
			tapping(asks, "From the gallery she watched the ferry come round the point."),
			"Partway through Chapter 2", 4, []listed{ada}},
		{"named by an alias revealed later on the page: not listed",
			tapping(asks, "A stranger in grey stepped off onto the quay."),
			"Partway through Chapter 2", 5, []listed{ada}},
		{"named by an alias once it's revealed: listed where the page first names them",
			typing("a stranger in grey stepped off onto the quay. ada knew him at once"),
			"Partway through Chapter 2", 6, []listed{tom, ada}},
		{"a card titled by an alias once it's revealed", typing("the village called her keeper ada"),
			"Finished Chapter 2", 7, []listed{{"Keeper Ada", "/b/1/e/1"}}},
		{"a snap to where the reader already is",
			func(t *testing.T, srv *httptest.Server) string {
				typing("ada said the lamp had to be lit")(t, srv)
				return typing("ada said the lamp had to be lit")(t, srv)
			},
			"Partway through Chapter 2", 3, []listed{ada}},
		{"a page naming no one", typing("the sea went flat and grey"),
			"Partway through Chapter 2", 2, nil},
		{"not started on the page: no one yet", tapping(asks, notStarted),
			"Partway through Chapter 2", 2, nil},
		{"a photo showing no sentence whole: no one yet", photographing("The wind dropped and the sea went"),
			"Finished Chapter 1", 1, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := lighthouse(t)
			body := c.snap(t, srv)
			if !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("want %q:\n%s", c.where, body)
			}
			cards, says, ok := onThisPage(t, body)
			if !ok || !slices.Equal(cards, c.cards) {
				t.Errorf("listed %v, want %v\n%s", cards, c.cards, body)
			}
			if c.cards == nil && !strings.Contains(says, "No one and nothing named on this page so far has a card.") {
				t.Errorf("a page naming no one should say so:\n%s", says)
			}
			if c.pos < 7 && (strings.Contains(body, "Keeper Ada") || strings.Contains(body, "Stranger Was Tom")) {
				t.Errorf("a title revealed past the position leaked:\n%s", body)
			}
			for _, card := range cards {
				code, body := send(t, srv, "", card.href, nil)
				if code != 200 || !strings.Contains(body, "<h1>"+card.title+"</h1>") {
					t.Errorf("%s's card: %d\n%s", card.title, code, body)
				}
				for fact, ord := range lighthouseFacts[card.href] {
					if shown := strings.Contains(body, fact); shown != (ord <= c.pos) {
						t.Errorf("%s's card at sentence %d shows %q = %v; it's revealed at %d", card.title, c.pos, fact, shown, ord)
					}
				}
			}
		})
	}
}

func TestCardsAfterASnapBehindThePosition(t *testing.T) {
	// The reader has finished chapter 2, where Ada's card takes the title
	// "Keeper Ada" in its last sentence, then snaps a page earlier in it.
	cases := []struct {
		answer       func(t *testing.T, srv *httptest.Server, page, label string) (int, string)
		label, where string
		cards        []listed
	}{
		{tap, "Move back", "Partway through Chapter 2", []listed{{"Ada", "/b/1/e/1"}}},
		{follow, "Keep my place", "Finished Chapter 2", []listed{{"Keeper Ada", "/b/1/e/1"}}},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			srv := lighthouse(t)
			send(t, srv, "", "/b/1/snap", url.Values{"words": {"the village called her keeper ada"}})
			question := typing("ada said the lamp had to be lit")(t, srv)
			if _, _, ok := onThisPage(t, question); ok || strings.Contains(question, "/b/1/e/") {
				t.Errorf("the question listed cards before the reader answered:\n%s", question)
			}
			_, body := c.answer(t, srv, question, c.label)
			if !strings.Contains(body, `<p class="where">`+c.where+`</p>`) {
				t.Errorf("want %q:\n%s", c.where, body)
			}
			if cards, _, _ := onThisPage(t, body); !slices.Equal(cards, c.cards) {
				t.Errorf("listed %v, want %v: Ada by her title where the reader now is\n%s", cards, c.cards, body)
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
		{"a photo over the size cap", func(s *Server) { s.maxPhoto = 64 }, append(page, bytes.Repeat([]byte(" "), 64)...), 413, "too big"},
		{"a photo that takes too long to read", func(s *Server) { s.Photos, s.photoTimeout = slowPhotos{}, time.Millisecond }, page, 503, "took too long"},
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

// Each photo snap logs its score, so an operator can see how close real
// photos come to the threshold: a page is found when at least 4 of the
// words read, and at least half of them, match the book in order.
func TestPhotoSnapsLogTheirScore(t *testing.T) {
	cases := []struct{ name, page, want string }{
		{"every word matched",
			"“The tide always turns,” his mother used to say.\nA hooded man watched from the dunes.",
			"photo snap: 16 of 16 words read matched, found the page"},
		{"a running head and page number don't match",
			"THE CRAWL                17\nturns,” his mother used to say.\nA hooded man watched from the dunes.",
			"photo snap: 13 of 16 words read matched, found the page"},
		{"under half matched",
			"A hooded man watched. Postcards from Zanzibar, wish you were here, wish you were there.",
			"photo snap: 4 of 15 words read matched, couldn't find the page"},
		{"nothing matched",
			"A postcard from Zanzibar, wish you were here.",
			"photo snap: 0 of 8 words read matched, couldn't find the page"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logged bytes.Buffer
			out := log.Writer()
			log.SetOutput(&logged)
			t.Cleanup(func() { log.SetOutput(out) })

			srv, _ := setup(t, "")
			snapPhoto(t, srv, "/b/1/snap", photo(c.page))
			if !strings.Contains(logged.String(), c.want) {
				t.Errorf("want %q logged:\n%s", c.want, &logged)
			}
		})
	}
}
