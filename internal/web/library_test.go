package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jheck90/gilgamesh/internal/adding"
	"github.com/jheck90/gilgamesh/internal/epub/epubtest"
	"github.com/jheck90/gilgamesh/internal/estimate"
	"github.com/jheck90/gilgamesh/internal/keys"
	"github.com/jheck90/gilgamesh/internal/library"
	"github.com/jheck90/gilgamesh/internal/store"
)

// settled is long enough ago that a library file isn't still arriving.
var settled = time.Now().Add(-time.Hour)

type libraryFixture struct {
	st     *store.Store
	q      *adding.Queue
	lib    *library.Library
	saga   int64 // The Crawl, Saga #1, from setup
	send   func(reader, path string, form url.Values) (int, string)
	shelve func(rel string, b epubtest.Book) string
}

// withLibrary serves the books setup adds, a library to add more from, and
// a queue that nothing runs. The admin ("default", the first reader) has an
// Anthropic key from the environment.
func withLibrary(t *testing.T) *libraryFixture {
	t.Helper()
	s := &libraryFixture{}
	srv, bid := setup(t, "Remote-User", func(sv *Server) {
		s.st = sv.Store
		s.q = &adding.Queue{Store: sv.Store, Env: keys.Env(func(k string) string {
			return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k]
		})}
		root := filepath.Join(t.TempDir(), "books")
		os.Mkdir(root, 0o755)
		s.lib = &library.Library{Root: root, Store: sv.Store}
		sv.Adding, sv.Library, sv.Prices = s.q, s.lib, estimate.Builtin()
	})
	s.saga = bid
	s.send = func(reader, path string, form url.Values) (int, string) { return send(t, srv, reader, path, form) }
	s.shelve = func(rel string, b epubtest.Book) string {
		t.Helper()
		path := filepath.Join(s.lib.Root, rel)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if b.Chapters == nil {
			for i := 1; i <= 3; i++ {
				b.Chapters = append(b.Chapters, epubtest.Chapter{Title: fmt.Sprintf("Chapter %d", i), Paras: []string{
					fmt.Sprintf("In chapter %d of %s the crawlers walked a long way down the stairs and argued about where the boots had gone.", i, b.Title)}})
			}
		}
		if err := epubtest.Write(path, b); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(path, settled, settled)
		return path
	}
	s.send("default", "/", nil) // the first reader is the admin
	return s
}

// before reports whether a comes before b in page.
func before(page, a, b string) bool {
	i, j := strings.Index(page, a), strings.Index(page, b)
	return i >= 0 && j >= 0 && i < j
}

func pick(rel string) string { return "/library/book?path=" + url.QueryEscape(rel) }

func TestLibraryListing(t *testing.T) {
	s := withLibrary(t)
	ctx := context.Background()
	s.shelve("Carl/2.epub", epubtest.Book{Title: "Carl's Doomsday Scenario", Author: "Matt Dinniman", Series: "Dungeon Crawler Carl", SeriesIndex: "2"})
	s.shelve("Carl/1.epub", epubtest.Book{Title: "Dungeon Crawler Carl", Author: "Matt Dinniman", Series: "Dungeon Crawler Carl", SeriesIndex: "1"})
	s.shelve("a-alone.epub", epubtest.Book{Title: "Alone", Author: "Someone"})
	added := s.shelve("added.epub", epubtest.Book{Title: "Added Already", Author: "Someone"})
	s.shelve("saga.epub", epubtest.Book{Title: "Between", Series: "Saga", SeriesIndex: "1.5"})
	os.WriteFile(filepath.Join(s.lib.Root, "broken.epub"), []byte("not a zip"), 0o644)
	os.Chtimes(filepath.Join(s.lib.Root, "broken.epub"), settled, settled)
	fresh := s.shelve("fresh.epub", epubtest.Book{Title: "Fresh"})
	os.Chtimes(fresh, time.Now(), time.Now())

	sha, _ := adding.FileSHA(added)
	sid, _ := s.st.EnsureSeries(ctx, "Added Already")
	addedID, err := s.st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: 1, Title: "Added Already", SHA256: sha, SourcePath: "/elsewhere/added.epub"},
		[]store.Chapter{{Idx: 1, Title: "One", FirstOrd: 0, LastOrd: 0}}, []store.Sentence{{Ord: 0, ChapterIdx: 1, Text: "Hello there."}}, 1, 100)
	if err != nil {
		t.Fatal(err)
	}

	if _, body := s.send("alice", "/", nil); !strings.Contains(body, `href="/library"`) {
		t.Errorf("home has no link to the library:\n%s", body)
	}
	_, page := s.send("alice", "/library", nil)
	for _, want := range []string{"Matt Dinniman", "Carl/1.epub", pick("Carl/1.epub"), pick("Carl/2.epub"), pick("a-alone.epub")} {
		if !strings.Contains(page, want) {
			t.Errorf("listing lacks %q:\n%s", want, page)
		}
	}
	if !before(page, "Dungeon Crawler Carl #1", "Dungeon Crawler Carl #2") || !before(page, "Carl&#39;s Doomsday Scenario", "Alone") {
		t.Errorf("not grouped by series and number with lone books last:\n%s", page)
	}
	for rel, reason := range map[string]string{
		"added.epub":  "already added",
		"saga.epub":   "a later book in this series is already added",
		"broken.epub": "can&#39;t read this file",
		"fresh.epub":  "still arriving",
	} {
		if strings.Contains(page, pick(rel)) {
			t.Errorf("%s can be picked", rel)
		}
		i := strings.Index(page, rel)
		if i < 0 || !strings.Contains(page[max(0, i-600):], reason) {
			t.Errorf("%s isn't listed as %q:\n%s", rel, reason, page)
		}
	}
	if !strings.Contains(page, fmt.Sprintf(`href="/b/%d"`, addedID)) {
		t.Errorf("an added file doesn't link to its book:\n%s", page)
	}

	for q, want := range map[string]string{"doomsday": "Carl/2.epub", "DINNIMAN": "Carl/1.epub", "saga": "saga.epub", "broken": "broken.epub"} {
		_, page := s.send("alice", "/library?q="+q, nil)
		if !strings.Contains(page, want) || strings.Contains(page, "a-alone.epub") {
			t.Errorf("searching %q:\n%s", q, page)
		}
	}
}

// long is a chapter of about n words.
func long(title string, n int) epubtest.Chapter {
	var paras []string
	for i := 0; i < n/20; i++ {
		paras = append(paras, fmt.Sprintf("Paragraph %d of %s, where the crawlers walked down the stairs and argued about the boots again.", i, title))
	}
	return epubtest.Chapter{Title: title, Paras: paras}
}

var inputTokens = regexp.MustCompile(`input (\S+) tokens`)

func TestLibraryPreview(t *testing.T) {
	s := withLibrary(t)
	s.shelve("Carl/1.epub", epubtest.Book{Title: "Dungeon Crawler Carl", Author: "Matt Dinniman", Series: "Dungeon Crawler Carl", SeriesIndex: "1",
		Chapters: []epubtest.Chapter{long("Chapter One", 3000), long("Chapter Two", 9000), long("Chapter Three", 3000)}})

	_, page := s.send("default", pick("Carl/1.epub"), nil)
	for _, want := range []string{
		"Dungeon Crawler Carl", "Matt Dinniman", "Chapter Two", "words",
		`name="skip" value="2"`, `name="series" value="Dungeon Crawler Carl"`, `name="index" value="1"`,
		"claude-haiku-5-5", "about $", `name="edit" value="1" checked`, "claude-opus-5-5",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("preview lacks %q:\n%s", want, page)
		}
	}
	if !confirmable(page) {
		t.Errorf("the admin, with a key, can't confirm:\n%s", page)
	}
	all := inputTokens.FindStringSubmatch(page)

	_, page = s.send("default", pick("Carl/1.epub")+"&set=1&skip=2&series=Dungeon+Crawler+Carl&index=1", nil)
	if !strings.Contains(page, `name="skip" value="2" checked`) || strings.Contains(page, `name="edit" value="1" checked`) {
		t.Errorf("the form doesn't keep what was asked:\n%s", page)
	}
	if got := inputTokens.FindStringSubmatch(page); all == nil || got == nil || got[1] == all[1] {
		t.Errorf("skipping a chapter didn't change the input tokens (%v, then %v)", all, got)
	}

	_, page = s.send("default", pick("Carl/1.epub")+"&set=1&series=Saga&index=0.5", nil)
	if !strings.Contains(page, "already has a later book") || confirmable(page) {
		t.Errorf("a clash with the reading order should be shown, and stop it being added:\n%s", page)
	}

	for _, rel := range []string{"../books/Carl/1.epub", "/etc/passwd", "Carl/../../x.epub", "missing.epub"} {
		if code, _ := s.send("default", pick(rel), nil); code != http.StatusNotFound {
			t.Errorf("previewing %q: %d, want 404", rel, code)
		}
	}
}

// confirmable reports whether a preview's Confirm button can be pressed.
func confirmable(page string) bool {
	m := regexp.MustCompile(`<button[^>]*formaction="/library/add"[^>]*>`).FindString(page)
	return m != "" && !strings.Contains(m, "disabled")
}

func TestConfirmQueuesTheBook(t *testing.T) {
	s := withLibrary(t)
	ctx := context.Background()
	path := s.shelve("Carl/1.epub", epubtest.Book{Title: "Dungeon Crawler Carl", Series: "Dungeon Crawler Carl", SeriesIndex: "1",
		Chapters: []epubtest.Chapter{long("Chapter One", 300), long("Chapter Two", 300), long("Chapter Three", 300)}})
	sha, _ := adding.FileSHA(path)
	form := url.Values{"path": {"Carl/1.epub"}, "sha": {sha}, "set": {"1"}, "skip": {"1", "3"}, "series": {"DCC"}, "index": {"1"}}

	stale := url.Values{}
	for k, v := range form {
		stale[k] = v
	}
	stale.Set("sha", strings.Repeat("0", 64))
	if code, body := s.send("default", "/library/add", stale); code != http.StatusConflict || !strings.Contains(body, "changed") {
		t.Errorf("confirming a file that changed since the preview: %d\n%s", code, body)
	}

	_, body := s.send("default", "/library/add", form)
	if !strings.Contains(body, "Books being added") || !strings.Contains(body, "Waiting") {
		t.Errorf("after confirming, want the queue:\n%s", body)
	}
	adds, _ := s.st.Adds(ctx)
	if len(adds) != 1 {
		t.Fatalf("queue = %+v, want the one book", adds)
	}
	want := store.QueuedAdd{Path: path, SHA256: sha, Series: "DCC", SeriesIndex: 1, Skip: "1,3", ThenEdit: false, Effort: "medium"}
	if a := adds[0]; a.Path != want.Path || a.SHA256 != want.SHA256 || a.Series != want.Series || a.SeriesIndex != want.SeriesIndex ||
		a.Skip != want.Skip || a.ThenEdit != want.ThenEdit || a.Effort != want.Effort || a.ChunkWords == 0 || a.UserName != "default" {
		t.Errorf("queued %+v, want %+v", a, want)
	}

	_, page := s.send("default", "/library", nil)
	if strings.Contains(page, pick("Carl/1.epub")) || !strings.Contains(page, "being added") {
		t.Errorf("a queued file can still be picked:\n%s", page)
	}
}

func TestReadersWithoutAWorkingKeyCanLookButNotAdd(t *testing.T) {
	s := withLibrary(t)
	ctx := context.Background()
	path := s.shelve("book.epub", epubtest.Book{Title: "A Book"})
	sha, _ := adding.FileSHA(path)
	form := url.Values{"path": {"book.epub"}, "sha": {sha}, "set": {"1"}}
	s.send("alice", "/", nil)
	alice, _, _ := s.st.UserByName(ctx, "alice")
	admin, _, _ := s.st.UserByName(ctx, "default")

	check := func(when string, says ...string) {
		t.Helper()
		if _, body := s.send("alice", "/library", nil); !strings.Contains(body, pick("book.epub")) {
			t.Errorf("%s: alice can't browse the library:\n%s", when, body)
		}
		_, page := s.send("alice", pick("book.epub"), nil)
		if confirmable(page) || !strings.Contains(page, "claude-haiku-5-5") {
			t.Errorf("%s: want a price but no way to confirm:\n%s", when, page)
		}
		for _, want := range says {
			if !strings.Contains(page, want) {
				t.Errorf("%s: preview doesn't say %q:\n%s", when, want, page)
			}
		}
		if code, _ := s.send("alice", "/library/add", form); code != http.StatusForbidden {
			t.Errorf("%s: confirming anyway: %d, want 403", when, code)
		}
	}
	check("no key", "No key", "gmesh key set", "gmesh key request")
	s.st.RequestGrant(ctx, alice.ID, "")
	check("request pending", "waiting for the admin", "gmesh key set")
	s.st.DecideGrant(ctx, alice.ID, store.GrantApproved, 100, admin.ID)
	b, _, _ := s.st.BookByID(ctx, s.saga)
	s.st.ApplyChunk(ctx, store.ChunkWrite{BookID: b.ID, SeriesID: b.SeriesID, NextChunk: 1,
		Usage: &store.UsageRec{UserID: alice.ID, KeyOwnerID: admin.ID, Provider: "anthropic", Model: "m", InputTokens: 90, OutputTokens: 20}})
	check("allowance used up", "this month&#39;s allowance", "gmesh key set")

	if adds, _ := s.st.Adds(ctx); len(adds) != 0 {
		t.Errorf("queued %+v", adds)
	}
}
