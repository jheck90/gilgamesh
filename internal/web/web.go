// Package web serves the reading side of Gilgamesh: set where you are,
// browse who and what you've met, and open a card. Nothing here calls a
// model, and nothing past the reader's position is ever rendered.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

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

	tmpl *template.Template
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
	i := sort.Search(len(rd.chapters), func(i int) bool { return rd.chapters[i].LastOrd >= rd.pos.Ord })
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

func (s *Server) book(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, rd, err := s.readingFor(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	cur := rd.current()
	var opts []chapterOption
	for i, c := range rd.chapters {
		opts = append(opts, chapterOption{Idx: c.Idx, Label: rd.label(i), Selected: i == cur})
	}
	data := map[string]any{
		"Title":    rd.book.Title,
		"Book":     rd.book,
		"Where":    rd.where(),
		"Chapters": opts,
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
			http.Redirect(w, r, "/b/"+strconv.FormatInt(rd.book.ID, 10), http.StatusSeeOther)
			return
		}
	}
	s.fail(w, httpError{http.StatusBadRequest, "No such chapter."})
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
	http.Redirect(w, r, "/b/"+strconv.FormatInt(rd.book.ID, 10), http.StatusSeeOther)
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
