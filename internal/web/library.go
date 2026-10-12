package web

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jheck90/gilgamesh/internal/adding"
	"github.com/jheck90/gilgamesh/internal/epub"
	"github.com/jheck90/gilgamesh/internal/estimate"
	"github.com/jheck90/gilgamesh/internal/extract"
	"github.com/jheck90/gilgamesh/internal/library"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

// listing is one file on the library page.
type listing struct {
	Path   string
	Title  string
	Author string
	Series string // with its number, like "Dungeon Crawler Carl #2"
	Pick   string // the preview's address, if it can be added
	Reason string // why it can't be, if it can't
	BookID int64  // the book it already is, if it's added
	Queued bool   // it's being added
	// Later is set when the only problem is a later book in its series,
	// which choosing another series or number on the preview can fix.
	Later bool
}

// seriesGroup is the files in one series, in order.
type seriesGroup struct {
	Series string // "" for books in no series
	Files  []listing
}

// added is what's already in the instance, to tell which library files
// can still be added.
type added struct {
	books   []store.Book
	bySHA   map[string]store.Book
	pending map[string]bool
}

func (s *Server) added(r *http.Request) (added, error) {
	books, err := s.Store.Books(r.Context())
	if err != nil {
		return added{}, err
	}
	adds, err := s.Store.Adds(r.Context())
	if err != nil {
		return added{}, err
	}
	a := added{books: books, bySHA: map[string]store.Book{}, pending: map[string]bool{}}
	for _, b := range books {
		a.bySHA[b.SHA256] = b
	}
	for _, q := range adds {
		if q.State != store.AddDone {
			a.pending[q.SHA256] = true
		}
	}
	return a, nil
}

// later returns a book already added after index in the series, if any:
// books go in in reading order.
func (a added) later(series string, index float64) (store.Book, bool) {
	for _, b := range a.books {
		if b.SeriesName == series && b.SeriesIndex > index {
			return b, true
		}
	}
	return store.Book{}, false
}

// seriesOf is the series and number a file would be added as by default.
func seriesOf(f library.File) (string, float64) {
	return adding.SeriesOf(&epub.Book{Title: f.Title, Series: f.Series, SeriesIndex: f.SeriesIndex}, "", 0)
}

// listing is how f shows on the library page, with why it can't be added
// if it can't.
func (a added) listing(f library.File) listing {
	sh := listing{Path: f.Path, Title: f.Title, Author: f.Author}
	if sh.Title == "" {
		sh.Title = f.Path[strings.LastIndex(f.Path, "/")+1:]
	}
	if f.Series != "" {
		name, index := seriesOf(f)
		sh.Series = name + " #" + fmtIndex(index)
	}
	switch {
	case f.Arriving:
		sh.Reason = "still arriving"
	case f.Unreadable != "":
		sh.Reason = f.Unreadable
	case a.bySHA[f.SHA256].ID != 0:
		sh.Reason, sh.BookID = "already added", a.bySHA[f.SHA256].ID
	case a.pending[f.SHA256]:
		sh.Reason, sh.Queued = "being added", true
	default:
		if _, ok := a.later(seriesOf(f)); ok {
			sh.Reason, sh.Later = "a later book in this series is already added", true
		}
	}
	return sh
}

func fmtIndex(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	if _, err := s.user(r); err != nil {
		s.fail(w, err)
		return
	}
	files, err := s.Library.Scan(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	a, err := s.added(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	type entry struct {
		series string
		index  float64
		sh     listing
	}
	var es []entry
	for _, f := range files {
		if q != "" && !holds(q, f.Title, f.Author, f.Series, f.Path) {
			continue
		}
		sh := a.listing(f)
		if sh.Reason == "" {
			sh.Pick = "/library/book?path=" + url.QueryEscape(f.Path)
		}
		es = append(es, entry{f.Series, f.SeriesIndex, sh})
	}
	// By series and number, with books in no series last.
	slices.SortFunc(es, func(x, y entry) int {
		if (x.series == "") != (y.series == "") {
			if x.series == "" {
				return 1
			}
			return -1
		}
		return cmp.Or(cmp.Compare(strings.ToLower(x.series), strings.ToLower(y.series)), cmp.Compare(x.index, y.index),
			cmp.Compare(strings.ToLower(x.sh.Title), strings.ToLower(y.sh.Title)), cmp.Compare(x.sh.Path, y.sh.Path))
	})
	var groups []seriesGroup
	for _, e := range es {
		if len(groups) == 0 || groups[len(groups)-1].Series != e.series {
			groups = append(groups, seriesGroup{Series: e.series})
		}
		groups[len(groups)-1].Files = append(groups[len(groups)-1].Files, e.sh)
	}
	s.render(w, "library", map[string]any{"Title": "Add a book", "Groups": groups, "Query": q, "Total": len(es)})
}

// holds reports whether any of fields holds q, ignoring case and accents.
func holds(q string, fields ...string) bool {
	q = text.Fold(q)
	for _, f := range fields {
		if strings.Contains(text.Fold(f), q) {
			return true
		}
	}
	return false
}

// The effort a book added from the library uses: `gmesh ingest`'s default.
// Only the CLI can change it, and the passage size.
const libraryEffort = "medium"

// section is one row of a preview's chapter list.
type section struct {
	N       int
	Title   string
	Words   int
	Skipped bool   // the reader left it out
	Short   string // it's left out anyway, being too short
}

// plan is what adding a library file would do, as the reader has set it.
type plan struct {
	File     library.File
	Title    string
	Author   string
	Sections []section
	Skip     string // the skipped sections, like "1-3,40"
	Series   string
	Index    float64
	Edit     bool
	Book     estimate.Book
	// Problem says why it can't be added as set, or is "".
	Problem string
}

// plan reads the library file the request names, with the chapters,
// series and number the reader chose; on a first look, the EPUB's.
func (s *Server) plan(r *http.Request, a added) (plan, error) {
	f, err := s.Library.File(r.Context(), r.FormValue("path"))
	if errors.Is(err, library.ErrNotInLibrary) {
		return plan{}, notFound
	}
	if err != nil {
		return plan{}, err
	}
	p := plan{File: f, Title: f.Title, Author: f.Author}
	if sh := a.listing(f); sh.Reason != "" && !sh.Later {
		p.Title, p.Problem = sh.Title, "This file can't be added: "+sh.Reason+"."
		return p, nil
	}
	eb, err := epub.Open(f.Abs)
	if err != nil {
		return plan{}, err
	}
	first := r.FormValue("set") == ""
	skip := map[int]bool{}
	for _, v := range r.Form["skip"] {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			skip[n] = true
		}
	}
	index, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("index")), 64)
	if err != nil || math.IsNaN(index) || math.IsInf(index, 0) || index < 0 {
		index = 0 // the EPUB's
	}
	p.Series, p.Index = adding.SeriesOf(eb, strings.TrimSpace(r.FormValue("series")), index)
	p.Edit = first || r.FormValue("edit") == "1"
	chapters, sentences, secs := adding.Prepare(eb, skip)
	var skipped []string
	for _, sec := range secs {
		row := section{N: sec.N, Title: sec.Title, Words: sec.Words, Skipped: skip[sec.N]}
		if sec.Skipped != "" && !row.Skipped {
			row.Short = sec.Skipped
		}
		if row.Skipped {
			skipped = append(skipped, strconv.Itoa(sec.N))
		}
		p.Sections = append(p.Sections, row)
	}
	p.Skip = strings.Join(skipped, ",")
	for _, st := range sentences {
		p.Book.Words += len(strings.Fields(st.Text))
	}
	p.Book.Calls = extract.CountChunks(chapters, sentences, extract.DefaultChunkWords)
	switch {
	case len(sentences) == 0:
		p.Problem = "Every chapter is left out."
	default:
		if b, ok := a.later(p.Series, p.Index); ok {
			p.Problem = fmt.Sprintf("%q already has a later book (%s, #%g). Books in a series go in in reading order.", p.Series, b.Title, b.SeriesIndex)
		}
	}
	return p, nil
}

// keyProblem says why a reader can't add books, and what fixes it, or is
// "".
func keyProblem(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if !strings.Contains(msg, "gmesh key") {
		msg += ". Add your own key with `gmesh key set`."
	}
	first, n := utf8.DecodeRuneInString(msg)
	return string(unicode.ToUpper(first)) + msg[n:]
}

// planFor is the reader and what adding the file they picked would do.
func (s *Server) planFor(r *http.Request) (store.User, plan, error) {
	user, err := s.user(r)
	if err != nil {
		return user, plan{}, err
	}
	a, err := s.added(r)
	if err != nil {
		return user, plan{}, err
	}
	p, err := s.plan(r, a)
	return user, p, err
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, p, err := s.planFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	data := map[string]any{"Title": p.Title, "Plan": p}
	if p.Sections == nil {
		s.render(w, "preview", data)
		return
	}
	res, keyErr := s.Adding.Resolve(ctx, user)
	cfg := res.Config
	if keyErr != nil {
		// Price it as most instances would run it.
		cfg = llm.Config{Provider: llm.ProviderAnthropic}
	}
	ingest, edit, err := adding.Estimate(ctx, s.Store, cfg, "", libraryEffort, p.Book, s.Prices)
	if err != nil {
		s.fail(w, err)
		return
	}
	data["Ingest"], data["EditPrice"], data["HasModel"] = ingest, edit, ingest.Model.Name != ""
	data["KeyProblem"], data["Assumed"] = keyProblem(keyErr), keyErr != nil
	data["Blocked"] = p.Problem != "" || keyErr != nil
	s.render(w, "preview", data)
}

// add queues the previewed file, as the reader set it. It's the only step
// on the library page that spends anything.
func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, p, err := s.planFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if p.File.SHA256 != r.FormValue("sha") {
		s.fail(w, httpError{http.StatusConflict, "The file has changed since you looked at it. Look at it again before adding it."})
		return
	}
	if p.Problem != "" {
		s.fail(w, httpError{http.StatusConflict, p.Problem})
		return
	}
	if _, err := s.Adding.Resolve(ctx, user); err != nil {
		s.fail(w, httpError{http.StatusForbidden, keyProblem(err)})
		return
	}
	_, err = s.Adding.Start(ctx, user, store.QueuedAdd{Path: p.File.Abs, SHA256: p.File.SHA256, Series: p.Series, SeriesIndex: p.Index,
		Skip: p.Skip, ChunkWords: extract.DefaultChunkWords, Effort: libraryEffort, ThenEdit: p.Edit})
	if err != nil {
		s.fail(w, httpError{http.StatusConflict, err.Error()})
		return
	}
	http.Redirect(w, r, "/adding", http.StatusSeeOther)
}
