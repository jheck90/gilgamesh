// Package web serves the reading side of Gilgamesh: set where you are,
// browse who and what you've met, and open a card. Nothing here calls a
// model, and nothing past the reader's position is ever rendered.
package web

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	Store *store.Store
	// TrustHeader names a header set by an authenticating proxy (for
	// example Authelia's Remote-User). Leave empty for a single reader.
	// Only set it when the app can't be reached except through the proxy.
	TrustHeader string
	DefaultUser string
	// Photos reads the text off a snapped photo of a page.
	Photos PhotoReader

	// maxPhoto caps a photo's size in bytes, and photoTimeout how long
	// reading one may take. Zero means 20 MB and 30 seconds.
	maxPhoto     int64
	photoTimeout time.Duration
	tmpl         *template.Template
}

// PhotoReader reads the text off a photo of a page. The photo and its text
// are the reader's: neither is ever stored or logged.
type PhotoReader interface {
	ReadPhoto(ctx context.Context, photo []byte) (string, error)
}

func (s *Server) Handler() http.Handler {
	s.tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return one
			}
			return many
		},
	}).ParseFS(templateFS, "templates/*.html"))
	static, _ := fs.Sub(staticFS, "static")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		http.ServeFileFS(w, r, static, "sw.js")
	})
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /b/{book}", s.book)
	mux.HandleFunc("POST /b/{book}/pos", s.setPos)
	mux.HandleFunc("POST /b/{book}/next", s.next)
	mux.HandleFunc("POST /b/{book}/snap", s.snap)
	mux.HandleFunc("POST /b/{book}/line", s.line)
	mux.HandleFunc("POST /b/{book}/back", s.back)
	mux.HandleFunc("GET /b/{book}/e/{entity}", s.entity)
	mux.HandleFunc("GET /b/{book}/timeline", s.timeline)
	return headers(mux)
}

func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		h.ServeHTTP(w, r)
	})
}

type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string { return e.msg }

// notFound is the one answer for anything the reader can't see yet, so a
// page that exists later looks the same as one that never will.
var notFound = httpError{http.StatusNotFound, "Nothing here yet."}

func (s *Server) fail(w http.ResponseWriter, err error) {
	var he httpError
	if !errors.As(err, &he) {
		log.Printf("web: %v", err)
		he = httpError{http.StatusInternalServerError, "Something went wrong."}
	}
	w.WriteHeader(he.code)
	s.render(w, "error", map[string]any{"Title": "Gilgamesh", "Message": he.msg})
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("web: render %s: %v", name, err)
	}
}

func (s *Server) user(r *http.Request) (store.User, error) {
	name := s.DefaultUser
	if s.TrustHeader != "" {
		name = strings.TrimSpace(r.Header.Get(s.TrustHeader))
		if name == "" {
			return store.User{}, httpError{http.StatusUnauthorized, "Not signed in."}
		}
	}
	return s.Store.EnsureUser(r.Context(), name)
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, err := s.user(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	books, err := s.Store.Books(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	type row struct {
		Book  store.Book
		Where string
		Ready bool
	}
	var rows []row
	for _, b := range books {
		p, err := s.Store.Progress(ctx, b.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		rd, err := s.reading(ctx, user, b.ID)
		if err != nil {
			s.fail(w, err)
			return
		}
		rows = append(rows, row{Book: b, Where: rd.where(), Ready: p.Done()})
	}
	s.render(w, "home", map[string]any{"Title": "Gilgamesh", "Books": rows})
}

// reading is a reader's view of one book.
type reading struct {
	book     store.Book
	chapters []store.Chapter
	pos      *store.Position // nil until the reader says where they are
}

func (s *Server) reading(ctx context.Context, user store.User, bookID int64) (reading, error) {
	b, ok, err := s.Store.BookByID(ctx, bookID)
	if err != nil {
		return reading{}, err
	}
	if !ok {
		return reading{}, notFound
	}
	chs, err := s.Store.Chapters(ctx, b.ID)
	if err != nil {
		return reading{}, err
	}
	rd := reading{book: b, chapters: chs}
	ord, ok, err := s.Store.GetPosition(ctx, user.ID, b.ID)
	if err != nil {
		return reading{}, err
	}
	if ok {
		p := store.At(b, ord)
		rd.pos = &p
	}
	return rd, nil
}

func (s *Server) readingFor(r *http.Request) (store.User, reading, error) {
	user, err := s.user(r)
	if err != nil {
		return user, reading{}, err
	}
	id, err := strconv.ParseInt(r.PathValue("book"), 10, 64)
	if err != nil {
		return user, reading{}, notFound
	}
	rd, err := s.reading(r.Context(), user, id)
	return user, rd, err
}

// current is the index into chapters of the chapter holding the position,
// or -1 with no position.
func (rd reading) current() int {
	if rd.pos == nil {
		return -1
	}
	return rd.chapterOf(rd.pos.Ord)
}

// chapterOf is the index into chapters of the chapter holding sentence ord.
func (rd reading) chapterOf(ord int) int {
	i := sort.Search(len(rd.chapters), func(i int) bool { return rd.chapters[i].LastOrd >= ord })
	if i == len(rd.chapters) {
		i--
	}
	return i
}

// finished reports whether the position is at the end of chapter i.
func (rd reading) finished(i int) bool {
	return rd.pos != nil && i >= 0 && rd.pos.Ord >= rd.chapters[i].LastOrd
}

func (rd reading) where() string {
	i := rd.current()
	if i < 0 {
		return ""
	}
	if rd.finished(i) {
		return "Finished " + rd.label(i)
	}
	return "Partway through " + rd.label(i)
}

// reading is the chapter being read now: the one after the last finished.
func (rd reading) readingNow() int {
	i := rd.current()
	if i < 0 {
		return 0
	}
	if rd.finished(i) && i+1 < len(rd.chapters) {
		return i + 1
	}
	return i
}

// label names chapter i. Titles past the chapter being read can be
// spoilers ("The Fall of a Chieftain"), so those show only their number.
func (rd reading) label(i int) string {
	c := rd.chapters[i]
	if i <= rd.readingNow() || onlyNumber(c.Title) {
		return c.Title
	}
	if n := numberPart(c.Title); n != "" {
		return n + " …"
	}
	return "Section " + strconv.Itoa(c.Idx)
}

var (
	roman       = regexp.MustCompile(`^M{0,4}(CM|CD|D?C{0,3})(XC|XL|L?X{0,3})(IX|IV|V?I{0,3})$`)
	labelWords  = map[string]bool{"chapter": true, "ch": true, "part": true, "book": true, "section": true}
	numberWords = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields(`zero one two three four five six seven eight nine ten eleven twelve
		thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty thirty forty fifty sixty
		seventy eighty ninety hundred and`) {
		numberWords[w] = true
	}
}

// isNumber accepts 12, XII and spelled-out numbers like twenty-three.
func isNumber(s string) bool {
	s = strings.TrimRight(s, ".:")
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	if s != "" && s == strings.ToUpper(s) && roman.MatchString(s) {
		return true
	}
	for _, part := range strings.Split(strings.ToLower(s), "-") {
		if !numberWords[part] {
			return false
		}
	}
	return s != ""
}

// onlyNumber reports whether a title is nothing but a chapter number, like
// "Chapter Twelve" or "XII", and so safe to show early.
func onlyNumber(title string) bool {
	words := strings.Fields(title)
	if len(words) > 0 && labelWords[strings.ToLower(strings.TrimRight(words[0], ".:"))] {
		words = words[1:]
	}
	for _, w := range words {
		if !isNumber(w) {
			return false
		}
	}
	return len(words) > 0
}

// numberPart returns the leading "Chapter 12" or "XII" of a title, or ""
// if it doesn't start with a number.
func numberPart(title string) string {
	words := strings.Fields(title)
	prefix := ""
	if len(words) > 1 && labelWords[strings.ToLower(strings.TrimRight(words[0], ".:"))] {
		prefix, words = words[0]+" ", words[1:]
	}
	if len(words) == 0 || !isNumber(words[0]) {
		return ""
	}
	return prefix + strings.TrimRight(words[0], ".:")
}

type chapterOption struct {
	Idx      int
	Label    string
	Selected bool
}

// options lists the chapters for the picker, the one holding the position
// selected.
func (rd reading) options() []chapterOption {
	cur := rd.current()
	var opts []chapterOption
	for i, c := range rd.chapters {
		opts = append(opts, chapterOption{Idx: c.Idx, Label: rd.label(i), Selected: i == cur})
	}
	return opts
}

func (s *Server) book(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	data := map[string]any{
		"Title":    rd.book.Title,
		"Book":     rd.book,
		"Where":    rd.where(),
		"Chapters": rd.options(),
		"HasPos":   rd.pos != nil,
	}
	if rd.pos == nil {
		s.render(w, "book", data)
		return
	}
	next := rd.readingNow()
	if !(rd.finished(next) && next == len(rd.chapters)-1) {
		data["Next"] = rd.label(next)
	}

	es, err := s.Store.VisibleEntities(ctx, *rd.pos)
	if err != nil {
		s.fail(w, err)
		return
	}
	if pg, ok := snappedPage(r, rd); ok {
		named, err := s.namedOn(ctx, rd, es, pg)
		if err != nil {
			s.fail(w, err)
			return
		}
		data["Snapped"], data["Named"] = true, named
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	type group struct {
		Type     string
		Entities []store.EntitySummary
	}
	byType := map[string]*group{}
	var groups []*group
	total := 0
	for _, e := range es {
		if q != "" && !matches(e, q) {
			continue
		}
		g, ok := byType[e.Type]
		if !ok {
			g = &group{Type: e.Type}
			byType[e.Type] = g
			groups = append(groups, g)
		}
		g.Entities = append(g.Entities, e)
		total++
	}
	sort.Slice(groups, func(i, j int) bool { return len(groups[i].Entities) > len(groups[j].Entities) })
	for _, g := range groups {
		sort.SliceStable(g.Entities, func(i, j int) bool { return g.Entities[i].Facts > g.Entities[j].Facts })
	}
	data["Groups"] = groups
	data["Query"] = q
	data["Total"] = total
	s.render(w, "book", data)
}

// snappedPage is the page a request says the reader snapped, if it names a
// page of the book. Nothing is kept between a snap and what follows it, so
// the page travels with each request.
func snappedPage(r *http.Request, rd reading) (page, bool) {
	first, err1 := strconv.Atoi(r.FormValue("first"))
	last, err2 := strconv.Atoi(r.FormValue("last"))
	return page{first, last}, err1 == nil && err2 == nil && len(rd.chapters) > 0 &&
		0 <= first && first <= last && last <= rd.chapters[len(rd.chapters)-1].LastOrd
}

// namedOn lists the entities among es that page pg names up to the
// reader's position, in the order it names them. Only the names and
// aliases es has at the position count, so an alias revealed later on
// the page doesn't list anyone.
func (s *Server) namedOn(ctx context.Context, rd reading, es []store.EntitySummary, pg page) ([]store.EntitySummary, error) {
	sents, err := s.Store.SentencesBetween(ctx, rd.book.ID, pg.first, min(pg.last, rd.pos.Ord))
	if err != nil {
		return nil, err
	}
	var out []store.EntitySummary
	seen := map[int64]bool{}
	for _, se := range sents {
		for _, e := range es {
			if seen[e.ID] {
				continue
			}
			for _, n := range append([]string{e.Name}, e.Aliases...) {
				if text.ContainsName(se.Text, text.Key(n)) {
					seen[e.ID] = true
					out = append(out, e)
					break
				}
			}
		}
	}
	return out, nil
}

func matches(e store.EntitySummary, q string) bool {
	q = text.Fold(q)
	for _, n := range append([]string{e.Name}, e.Aliases...) {
		if strings.Contains(text.Fold(n), q) {
			return true
		}
	}
	return false
}

func (s *Server) setPos(w http.ResponseWriter, r *http.Request) {
	user, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	idx, err := strconv.Atoi(r.FormValue("chapter"))
	if err != nil {
		s.fail(w, httpError{http.StatusBadRequest, "Pick a chapter."})
		return
	}
	for _, c := range rd.chapters {
		if c.Idx == idx {
			if err := s.Store.SetPosition(r.Context(), user.ID, rd.book.ID, c.LastOrd); err != nil {
				s.fail(w, err)
				return
			}
			toBook(w, r, rd)
			return
		}
	}
	s.fail(w, httpError{http.StatusBadRequest, "No such chapter."})
}

// toBook sends the reader back to the book after moving their position.
func toBook(w http.ResponseWriter, r *http.Request, rd reading) {
	http.Redirect(w, r, "/b/"+strconv.FormatInt(rd.book.ID, 10), http.StatusSeeOther)
}

// next marks the chapter being read as finished.
func (s *Server) next(w http.ResponseWriter, r *http.Request) {
	user, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	c := rd.chapters[rd.readingNow()]
	if err := s.Store.SetPosition(r.Context(), user.ID, rd.book.ID, c.LastOrd); err != nil {
		s.fail(w, err)
		return
	}
	toBook(w, r, rd)
}

// snap moves the position to the page in front of the reader, found from
// a photo of it or from the last few words they read.
func (s *Server) snap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	sents, err := s.Store.Sentences(ctx, rd.book.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var pg page
	var ord int                   // the sentence on it the reader has read to
	var question []store.Sentence // the page, when the snap must ask how far down the reader has got
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		pg, ord, question, err = s.snapPhoto(w, r, rd.book.ID, sents)
	} else {
		pg, ord, err = snapWords(r, sents)
	}
	var again tryAgain
	if errors.As(err, &again) {
		title := "Couldn't find this page"
		if again.problem != "" {
			title = again.problem
		}
		w.WriteHeader(again.code)
		s.render(w, "unmatched", map[string]any{
			"Title":    title,
			"Problem":  again.problem,
			"Message":  again.msg,
			"Book":     rd.book,
			"Where":    rd.where(),
			"HasPos":   rd.pos != nil,
			"Chapters": rd.options(),
			"Words":    again.words,
		})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	if question != nil {
		// Only the page's own sentences: the reader has those in front of
		// them already.
		first, last := question[0].Ord, question[len(question)-1].Ord
		s.render(w, "line", map[string]any{
			"Title":  "How far down have you read?",
			"Book":   rd.book,
			"Lines":  question,
			"First":  first,
			"Last":   last,
			"Before": first - 1,
		})
		return
	}
	s.snapTo(w, r, user, rd, pg, ord)
}

// page is a snapped page, from the first sentence it shows any of to the
// last.
type page struct{ first, last int }

// snapTo moves the reader's position to sentence ord of page pg after a
// snap. Ahead of the saved position, or with none saved, it moves straight
// there, and to the same sentence it changes nothing. Behind it, it asks
// first: a snap of an earlier page, rereading or by accident, mustn't
// silently move the reader back.
func (s *Server) snapTo(w http.ResponseWriter, r *http.Request, user store.User, rd reading, pg page, ord int) {
	switch {
	case rd.pos != nil && ord == rd.pos.Ord:
	case rd.pos != nil && ord < rd.pos.Ord:
		s.render(w, "back", map[string]any{
			"Title":    "Move back?",
			"Book":     rd.book,
			"Question": rd.backQuestion(ord),
			"Ord":      ord,
			"First":    pg.first,
			"Last":     pg.last,
		})
		return
	default:
		if err := s.Store.SetPosition(r.Context(), user.ID, rd.book.ID, ord); err != nil {
			s.fail(w, err)
			return
		}
	}
	toPage(w, r, rd, pg)
}

// toPage sends the reader back to the book after a snap, to see who's named
// on the page they snapped.
func toPage(w http.ResponseWriter, r *http.Request, rd reading, pg page) {
	http.Redirect(w, r, fmt.Sprintf("/b/%d?first=%d&last=%d", rd.book.ID, pg.first, pg.last), http.StatusSeeOther)
}

// backQuestion asks whether to move back to sentence ord, naming both
// places by chapter: "You’re partway through Chapter 14. Move back to
// Chapter 12?" Both labels are the saved position's, so it shows no title
// the reader hasn't reached.
func (rd reading) backQuestion(ord int) string {
	i := rd.current()
	now := "You’re partway through " + rd.label(i) + "."
	if rd.finished(i) {
		now = "You’ve finished " + rd.label(i) + "."
	}
	j := rd.chapterOf(ord)
	to := rd.label(j)
	switch {
	case j == i:
		to = "earlier in " + to
	case ord >= rd.chapters[j].LastOrd:
		to = "the end of " + to
	}
	return now + " Move back to " + to + "?"
}

// back moves the position to sentence ord once the reader has said "Move
// back", then shows who's named on the page they snapped. Like the chapter
// picker it takes any sentence in the book: it only ever moves the reader's
// own position.
func (s *Server) back(w http.ResponseWriter, r *http.Request) {
	user, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ord, err := strconv.Atoi(r.FormValue("ord"))
	if err != nil || len(rd.chapters) == 0 || ord < -1 || ord > rd.chapters[len(rd.chapters)-1].LastOrd {
		s.fail(w, httpError{http.StatusBadRequest, "That sentence isn’t in the book."})
		return
	}
	if err := s.Store.SetPosition(r.Context(), user.ID, rd.book.ID, ord); err != nil {
		s.fail(w, err)
		return
	}
	if pg, ok := snappedPage(r, rd); ok {
		toPage(w, r, rd, pg)
		return
	}
	toBook(w, r, rd)
}

// line moves the position to the sentence the reader tapped on the line
// question, or to just before the page if they haven't started it. The
// question sends back the page it asked about, so nothing is kept between
// the two; an answer off that page, or a page off the book, is refused.
func (s *Server) line(w http.ResponseWriter, r *http.Request) {
	user, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	pg, ok := snappedPage(r, rd)
	ord, err := strconv.Atoi(r.FormValue("ord"))
	if !ok || err != nil || ord < pg.first-1 || ord > pg.last {
		s.fail(w, httpError{http.StatusBadRequest, "That sentence isn’t on the page."})
		return
	}
	s.snapTo(w, r, user, rd, pg, ord)
}

// tryAgain is a snap that didn't move the position. The reader sees why,
// with the ways to try again: another photo, typed words, or the chapter
// picker.
type tryAgain struct {
	code    int
	problem string // what went wrong with a photo; empty when the page just wasn't found
	msg     string
	words   string // typed words to fill back in
}

func (e tryAgain) Error() string { return e.problem + " " + e.msg }

// snapWords moves to the sentence the reader's typed words end in. The
// page is the sentences they match.
func snapWords(r *http.Request, sents []store.Sentence) (pg page, ord int, err error) {
	words := r.FormValue("words")
	found, ok := text.MatchPage(words, texts(sents))
	if !ok {
		return page{}, 0, tryAgain{code: http.StatusOK, words: words,
			msg: "Type 4 or more words just as they’re printed, or pick a chapter instead."}
	}
	pg = page{sents[found.First].Ord, sents[found.Last].Ord}
	return pg, pg.last, nil
}

// snapPhoto moves to the end of the page in a photo: its last sentence
// shown whole. If anything is revealed on the page, the reader may not have
// got that far down, so instead of an ord it returns the page's sentences
// for the line question. The photo is held in memory for this request only.
func (s *Server) snapPhoto(w http.ResponseWriter, r *http.Request, bookID int64, sents []store.Sentence) (pg page, ord int, question []store.Sentence, err error) {
	photo, err := readPhoto(w, r, cmp.Or(s.maxPhoto, 20<<20))
	if err != nil {
		return page{}, 0, nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), cmp.Or(s.photoTimeout, 30*time.Second))
	defer cancel()
	pageText, err := s.Photos.ReadPhoto(ctx, photo)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return page{}, 0, nil, tryAgain{code: http.StatusServiceUnavailable, problem: "Reading that photo took too long", msg: typeInstead}
	}
	if err != nil {
		log.Printf("web: read photo: %v", err)
		return page{}, 0, nil, tryAgain{code: http.StatusServiceUnavailable, problem: "Couldn’t read that photo",
			msg: "Photo snaps aren’t working here right now. " + typeInstead}
	}
	found, ok := text.MatchPage(pageText, texts(sents))
	if !ok {
		return page{}, 0, nil, tryAgain{code: http.StatusOK,
			msg: "Retake the photo with the whole page flat and in focus, type 4 or more words just as they’re printed, or pick a chapter instead."}
	}
	pg = page{sents[found.First].Ord, sents[found.Last].Ord}
	end := found.Last
	if found.Cut {
		end--
	}
	if end < found.First {
		return pg, pg.first - 1, nil, nil // no sentence shown whole
	}
	revealed, err := s.Store.RevealsBetween(r.Context(), bookID, pg.first, sents[end].Ord)
	if err != nil {
		return page{}, 0, nil, err
	}
	if revealed {
		return pg, 0, sents[found.First : end+1], nil
	}
	return pg, sents[end].Ord, nil, nil
}

const typeInstead = "Type the last 4 or more words you read instead, or pick a chapter."

// readPhoto reads the uploaded photo into memory, refusing one over max
// bytes or that isn't an image. Reading the parts directly, rather than
// parsing the form, keeps the photo off disk.
func readPhoto(w http.ResponseWriter, r *http.Request, max int64) ([]byte, error) {
	tooBig := tryAgain{code: http.StatusRequestEntityTooLarge, problem: "That photo is too big", msg: typeInstead}
	notPhoto := tryAgain{code: http.StatusUnsupportedMediaType, problem: "That isn’t a photo",
		msg: "Take a photo of the page you’re on, or type the last 4 or more words you read."}
	badUpload := httpError{http.StatusBadRequest, "Bad upload."}
	r.Body = http.MaxBytesReader(w, r.Body, max+64<<10) // room for the form around the photo
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, badUpload
	}
	var mbe *http.MaxBytesError
	for {
		p, err := mr.NextPart()
		if errors.As(err, &mbe) {
			return nil, tooBig
		}
		if errors.Is(err, io.EOF) {
			return nil, notPhoto
		}
		if err != nil {
			return nil, badUpload
		}
		if p.FormName() != "photo" {
			continue
		}
		photo, err := io.ReadAll(io.LimitReader(p, max+1))
		if errors.As(err, &mbe) || int64(len(photo)) > max {
			return nil, tooBig
		}
		if err != nil {
			return nil, badUpload
		}
		if !strings.HasPrefix(http.DetectContentType(photo), "image/") {
			return nil, notPhoto
		}
		return photo, nil
	}
}

func texts(sents []store.Sentence) []string {
	out := make([]string, len(sents))
	for i, se := range sents {
		out[i] = se.Text
	}
	return out
}

func (s *Server) entity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rd.pos == nil {
		s.fail(w, notFound)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("entity"), 10, 64)
	if err != nil {
		s.fail(w, notFound)
		return
	}
	es, err := s.Store.VisibleEntities(ctx, *rd.pos)
	if err != nil {
		s.fail(w, err)
		return
	}
	var e *store.EntitySummary
	for i := range es {
		if es[i].ID == id {
			e = &es[i]
		}
	}
	if e == nil {
		s.fail(w, notFound)
		return
	}
	facts, err := s.Store.VisibleFacts(ctx, e.ID, *rd.pos, 0)
	if err != nil {
		s.fail(w, err)
		return
	}

	type fact struct {
		Text    string
		Sources []string
	}
	type section struct {
		Heading string
		Facts   []fact
	}
	var sections []*section
	chapters := map[int64]reading{rd.book.ID: rd}
	for _, f := range facts {
		fr, ok := chapters[f.BookID]
		if !ok {
			chs, err := s.Store.Chapters(ctx, f.BookID)
			if err != nil {
				s.fail(w, err)
				return
			}
			fr = reading{chapters: chs}
			chapters[f.BookID] = fr
		}
		i := sort.Search(len(fr.chapters), func(i int) bool { return fr.chapters[i].LastOrd >= f.Ord })
		if i == len(fr.chapters) {
			i--
		}
		heading := fr.chapters[i].Title
		if f.BookID != rd.book.ID {
			heading = f.BookTitle + " · " + heading
		}
		if len(sections) == 0 || sections[len(sections)-1].Heading != heading {
			sections = append(sections, &section{Heading: heading})
		}
		ft := fact{Text: f.Text}
		for _, ord := range f.Evidence {
			se, err := s.Store.Sentence(ctx, f.BookID, ord)
			if err != nil {
				s.fail(w, err)
				return
			}
			ft.Sources = append(ft.Sources, se.Text)
		}
		sec := sections[len(sections)-1]
		sec.Facts = append(sec.Facts, ft)
	}
	s.render(w, "entity", map[string]any{
		"Title":    e.Name,
		"Book":     rd.book,
		"Entity":   e,
		"Where":    rd.where(),
		"Sections": sections,
	})
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if rd.pos == nil {
		s.fail(w, notFound)
		return
	}
	ms, err := s.Store.VisibleMarkers(ctx, *rd.pos)
	if err != nil {
		s.fail(w, err)
		return
	}
	type mark struct{ Label, Where string }
	var marks []mark
	for _, m := range ms {
		where := m.BookTitle
		if m.BookID == rd.book.ID {
			i := sort.Search(len(rd.chapters), func(i int) bool { return rd.chapters[i].LastOrd >= m.Ord })
			if i < len(rd.chapters) {
				where = rd.chapters[i].Title
			}
		}
		marks = append(marks, mark{Label: m.Label, Where: where})
	}
	s.render(w, "timeline", map[string]any{"Title": "Timeline", "Book": rd.book, "Where": rd.where(), "Marks": marks})
}
