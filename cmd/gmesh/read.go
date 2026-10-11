package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"

	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

func (a *app) books(ctx context.Context) error {
	st, err := a.store()
	if err != nil {
		return err
	}
	books, err := st.Books(ctx)
	if err != nil {
		return err
	}
	if len(books) == 0 {
		fmt.Fprintln(a.out, "no books yet; add one with `gmesh ingest FILE.epub`")
		return nil
	}
	for _, b := range books {
		p, err := st.Progress(ctx, b.ID)
		if err != nil {
			return err
		}
		status := "ready"
		if !p.Done() {
			status = fmt.Sprintf("notes %d/%d", p.NextChunk, p.TotalChunks)
		}
		fmt.Fprintf(a.out, "%3d  %s #%g  %s  (%s)\n", b.ID, b.SeriesName, b.SeriesIndex, b.Title, status)
	}
	return nil
}

// reading is the shared setup for read commands: the book, its chapters
// and the position to look from.
type reading struct {
	book     store.Book
	chapters []store.Chapter
	pos      store.Position
}

func (r reading) chapterOf(ord int) store.Chapter {
	i := sort.Search(len(r.chapters), func(i int) bool { return r.chapters[i].LastOrd >= ord })
	if i == len(r.chapters) {
		i--
	}
	return r.chapters[i]
}

// chapter finds chapter n by its number, which may have gaps where
// sections were skipped.
func (r reading) chapter(n int) (store.Chapter, error) {
	for _, c := range r.chapters {
		if c.Idx == n {
			return c, nil
		}
	}
	return store.Chapter{}, fmt.Errorf("%s has no chapter %d; see `gmesh chapters %d`", r.book.Title, n, r.book.ID)
}

func (a *app) chapterList(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: gmesh chapters BOOK")
	}
	b, err := a.book(ctx, args[0])
	if err != nil {
		return err
	}
	chapters, err := a.st.Chapters(ctx, b.ID)
	if err != nil {
		return err
	}
	for _, c := range chapters {
		fmt.Fprintf(a.out, "%4d  %s\n", c.Idx, c.Title)
	}
	return nil
}

func (r reading) asOf() string {
	c := r.chapterOf(r.pos.Ord)
	if r.pos.Ord == c.LastOrd {
		return fmt.Sprintf("as of the end of chapter %d, %q", c.Idx, c.Title)
	}
	return fmt.Sprintf("as of partway through chapter %d, %q", c.Idx, c.Title)
}

// readFlags adds --chapter to fs and returns a func that resolves the
// position: that chapter's end if given, else the saved position. There is
// deliberately no default to the end of the book.
func (a *app) readFlags(fs *flag.FlagSet) func(context.Context, string) (reading, error) {
	chapter := fs.Int("chapter", 0, "look as of the end of this chapter instead of your saved position")
	return func(ctx context.Context, ref string) (reading, error) {
		b, err := a.book(ctx, ref)
		if err != nil {
			return reading{}, err
		}
		chapters, err := a.st.Chapters(ctx, b.ID)
		if err != nil {
			return reading{}, err
		}
		r := reading{book: b, chapters: chapters}
		if *chapter != 0 {
			c, err := r.chapter(*chapter)
			if err != nil {
				return reading{}, err
			}
			r.pos = store.At(b, c.LastOrd)
			return r, nil
		}
		user, err := a.user(ctx)
		if err != nil {
			return reading{}, err
		}
		ord, ok, err := a.st.GetPosition(ctx, user.ID, b.ID)
		if err != nil {
			return reading{}, err
		}
		if !ok {
			return reading{}, fmt.Errorf("you haven't said where you are in %s yet: run `gmesh pos %d --chapter N`, or pass --chapter N", b.Title, b.ID)
		}
		r.pos = store.At(b, ord)
		return r, nil
	}
}

func (a *app) pos(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pos", flag.ContinueOnError)
	chapter := fs.Int("chapter", 0, "set your position to the end of this chapter")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gmesh pos BOOK [--chapter N]")
	}
	b, err := a.book(ctx, pos[0])
	if err != nil {
		return err
	}
	user, err := a.user(ctx)
	if err != nil {
		return err
	}
	chapters, err := a.st.Chapters(ctx, b.ID)
	if err != nil {
		return err
	}
	r := reading{book: b, chapters: chapters}
	if *chapter == 0 {
		ord, ok, err := a.st.GetPosition(ctx, user.ID, b.ID)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(a.out, "no position set for %s\n", b.Title)
			return nil
		}
		r.pos = store.At(b, ord)
		fmt.Fprintf(a.out, "%s: %s\n", b.Title, strings.TrimPrefix(r.asOf(), "as of "))
		return nil
	}
	c, err := r.chapter(*chapter)
	if err != nil {
		return err
	}
	ord := c.LastOrd
	if err := a.st.SetPosition(ctx, user.ID, b.ID, ord); err != nil {
		return err
	}
	r.pos = store.At(b, ord)
	fmt.Fprintf(a.out, "%s: now at %s\n", b.Title, strings.TrimPrefix(r.asOf(), "as of "))
	return nil
}

func (a *app) entities(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("entities", flag.ContinueOnError)
	resolve := a.readFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gmesh entities BOOK [--chapter N]")
	}
	r, err := resolve(ctx, pos[0])
	if err != nil {
		return err
	}
	es, err := a.st.VisibleEntities(ctx, r.pos)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s, %s: %d entries\n", r.book.Title, r.asOf(), len(es))
	byType := map[string][]store.EntitySummary{}
	var types []string
	for _, e := range es {
		if _, ok := byType[e.Type]; !ok {
			types = append(types, e.Type)
		}
		byType[e.Type] = append(byType[e.Type], e)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Fprintf(a.out, "\n%s\n", t)
		for _, e := range byType[t] {
			line := "  " + e.Name
			if len(e.Aliases) > 0 {
				line += " (" + strings.Join(e.Aliases, ", ") + ")"
			}
			fmt.Fprintf(a.out, "%s · %d\n", line, e.Facts)
		}
	}
	return nil
}

func (a *app) card(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("card", flag.ContinueOnError)
	resolve := a.readFlags(fs)
	sources := fs.Bool("sources", false, "show the sentences behind each fact")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return errors.New("usage: gmesh card BOOK NAME [--chapter N] [--sources]")
	}
	r, err := resolve(ctx, pos[0])
	if err != nil {
		return err
	}
	name := strings.Join(pos[1:], " ")
	es, err := a.st.VisibleEntities(ctx, r.pos)
	if err != nil {
		return err
	}
	e, err := pick(es, name)
	if err != nil {
		// Same message whether or not the name turns up later.
		return fmt.Errorf("%s, %s", err, r.asOf())
	}

	fmt.Fprintf(a.out, "%s · %s\n", e.Name, e.Type)
	if len(e.Aliases) > 0 {
		fmt.Fprintf(a.out, "also: %s\n", strings.Join(e.Aliases, ", "))
	}
	fmt.Fprintf(a.out, "%s\n", r.asOf())

	facts, err := a.st.VisibleFacts(ctx, e.ID, r.pos, 0)
	if err != nil {
		return err
	}
	chapterCache := map[int64]reading{r.book.ID: r}
	heading := ""
	for _, f := range facts {
		fr, ok := chapterCache[f.BookID]
		if !ok {
			chs, err := a.st.Chapters(ctx, f.BookID)
			if err != nil {
				return err
			}
			fr = reading{chapters: chs}
			chapterCache[f.BookID] = fr
		}
		c := fr.chapterOf(f.Ord)
		h := fmt.Sprintf("%s · ch %d, %s", f.BookTitle, c.Idx, c.Title)
		if h != heading {
			fmt.Fprintf(a.out, "\n  %s\n", h)
			heading = h
		}
		fmt.Fprintf(a.out, "    • %s\n", f.Text)
		if *sources {
			for _, ord := range f.Evidence {
				s, err := a.st.Sentence(ctx, f.BookID, ord)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "        › %s\n", s.Text)
			}
		}
	}
	return nil
}

// pick finds an entity by exact name or alias, then by partial match.
func pick(es []store.EntitySummary, name string) (store.EntitySummary, error) {
	want := text.Fold(name)
	var partial []store.EntitySummary
	for _, e := range es {
		names := append([]string{e.Name}, e.Aliases...)
		for _, n := range names {
			if text.Fold(n) == want {
				return e, nil
			}
		}
		for _, n := range names {
			if strings.Contains(text.Fold(n), want) {
				partial = append(partial, e)
				break
			}
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		return store.EntitySummary{}, fmt.Errorf("nothing called %q yet", name)
	}
	var names []string
	for _, e := range partial {
		names = append(names, e.Name)
	}
	return store.EntitySummary{}, fmt.Errorf("%q could be: %s", name, strings.Join(names, ", "))
}

func (a *app) timeline(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	resolve := a.readFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gmesh timeline BOOK [--chapter N]")
	}
	r, err := resolve(ctx, pos[0])
	if err != nil {
		return err
	}
	ms, err := a.st.VisibleMarkers(ctx, r.pos)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s, %s\n", r.book.Title, r.asOf())
	if len(ms) == 0 {
		fmt.Fprintln(a.out, "no in-world dates or days mentioned yet")
	}
	cache := map[int64]reading{r.book.ID: r}
	for _, m := range ms {
		mr, ok := cache[m.BookID]
		if !ok {
			chs, err := a.st.Chapters(ctx, m.BookID)
			if err != nil {
				return err
			}
			mr = reading{chapters: chs}
			cache[m.BookID] = mr
		}
		c := mr.chapterOf(m.Ord)
		fmt.Fprintf(a.out, "  %-24s %s · ch %d\n", m.Label, m.BookTitle, c.Idx)
	}
	return nil
}
