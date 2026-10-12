package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jheck90/gilgamesh/internal/epub"
	"github.com/jheck90/gilgamesh/internal/estimate"
	"github.com/jheck90/gilgamesh/internal/extract"
	"github.com/jheck90/gilgamesh/internal/keys"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

// Sections shorter than this are title pages and separators, not chapters.
const minSectionWords = 20

func (a *app) ingest(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ingest", flag.ContinueOnError)
	series := fs.String("series", "", "series name (default: from the EPUB, else the book title)")
	index := fs.Float64("index", 0, "number in the series (default: from the EPUB, else 1)")
	chunkWords := fs.Int("chunk-words", extract.DefaultChunkWords, "longest passage sent to the model at once, in words")
	model := fs.String("model", "", "model to use instead of the key's default")
	effort := fs.String("effort", "medium", "Anthropic effort: low, medium, high, xhigh or max")
	dry := fs.Bool("dry-run", false, "parse the book and show the plan without calling a model")
	verbose := fs.Bool("v", false, "list every note the spoiler checks dropped")
	maxCalls := fs.Int("max-calls", 0, "stop after this many model calls; run again to continue")
	skipList := fs.String("skip", "", "sections to leave out, as numbered by --dry-run, like 1-3,40 (front matter, a preview of the next book)")
	prices := fs.String("prices", envOr("GMESH_PRICES", ""), "JSON file of model prices that --dry-run uses instead of the built-in ones")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gmesh ingest FILE.epub [--series NAME] [--index N] [--dry-run]")
	}
	path := pos[0]

	eb, err := epub.Open(path)
	if err != nil {
		return err
	}
	skip, err := parseSkip(*skipList)
	if err != nil {
		return err
	}
	chapters, sentences, plan := prepare(eb, skip)
	if len(sentences) == 0 {
		return errors.New("no readable text found; DRM-protected books aren't supported")
	}
	seriesName, seriesIndex := seriesOf(eb, *series, *index)
	nChunks := extract.CountChunks(chapters, sentences, *chunkWords)
	words := 0
	for _, s := range sentences {
		words += len(strings.Fields(s.Text))
	}
	fmt.Fprintf(a.out, "%s by %s\n", eb.Title, eb.Author)
	summary := func() {
		fmt.Fprintf(a.out, "series: %s #%g\n", seriesName, seriesIndex)
		fmt.Fprintf(a.out, "%d chapters, %d sentences, %d words (about %dk tokens of text)\n",
			len(chapters), len(sentences), words, estimate.Tokens(words)/1000)
		fmt.Fprintf(a.out, "extraction: %d model calls of up to %d words each\n", nChunks, *chunkWords)
	}
	if *dry {
		summary()
		for _, sec := range plan {
			note := fmt.Sprintf("%d words", sec.words)
			if sec.skipped != "" {
				note += ", skipped: " + sec.skipped
			}
			fmt.Fprintf(a.out, "  %3d  %s (%s)\n", sec.n, sec.title, note)
		}
		fmt.Fprintln(a.out, "check the start and end for front matter or a preview of the next book, and leave them out with --skip")
		return a.showPrice(ctx, estimate.Book{Words: words, Calls: nChunks}, *model, *effort, *prices)
	}

	st, err := a.store()
	if err != nil {
		return err
	}
	user, err := a.user(ctx)
	if err != nil {
		return err
	}
	sha, err := fileSHA(path)
	if err != nil {
		return err
	}
	book, exists, err := st.BookBySHA(ctx, sha)
	if err != nil {
		return err
	}
	if !exists {
		summary()
		sid, err := st.EnsureSeries(ctx, seriesName)
		if err != nil {
			return err
		}
		later, err := st.LaterInSeries(ctx, sid, seriesIndex)
		if err != nil {
			return err
		}
		if len(later) > 0 {
			return fmt.Errorf("%q already has a later book (%s, #%g). Add books in reading order so notes build on each other; remove later ones with `gmesh rm` first",
				seriesName, later[0].Title, later[0].SeriesIndex)
		}
		abs, _ := filepath.Abs(path)
		id, err := st.CreateBook(ctx, store.Book{SeriesID: sid, SeriesIndex: seriesIndex, Title: eb.Title, Author: eb.Author, SourcePath: abs, SHA256: sha},
			chapters, sentences, nChunks, *chunkWords)
		if err != nil {
			return err
		}
		if book, _, err = st.BookByID(ctx, id); err != nil {
			return err
		}
	} else {
		p, err := st.Progress(ctx, book.ID)
		if err != nil {
			return err
		}
		if p.Done() {
			fmt.Fprintf(a.out, "already done (book id %d)\n", book.ID)
			return nil
		}
		// The stored plan wins over this run's flags, so say what it is.
		fmt.Fprintf(a.out, "resuming at call %d of %d (the --skip and --chunk-words from the first run still apply)\n", p.NextChunk+1, p.TotalChunks)
	}

	client, res, err := a.modelClient(ctx, st, user, *model, llm.DefaultIngestModel, *effort)
	if err != nil {
		return err
	}

	x := &extract.Extractor{Store: st, LLM: client, UserID: user.ID, KeyOwnerID: res.OwnerID, Effort: res.Config.Effort, Log: a.out, Verbose: *verbose, MaxCalls: *maxCalls}
	if res.Borrowed(user.ID) {
		// Recheck before every call: the admin may revoke, and the cap
		// may run out partway through a book.
		x.Allow = func(ctx context.Context) error {
			bx, err := box()
			if err != nil {
				return err
			}
			_, err = keys.Resolve(ctx, st, bx, user, keys.Env(os.Getenv))
			return err
		}
	}
	if err := x.Run(ctx, book); errors.Is(err, extract.ErrPaused) {
		fmt.Fprintf(a.out, "paused after %d calls; run the same command again to continue\n", *maxCalls)
		return nil
	} else if err != nil {
		fmt.Fprintln(a.out, "stopped; run the same command again to pick up where it left off")
		return err
	}
	es, err := st.VisibleEntities(ctx, store.At(book, len(sentences)))
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "done: %d entities so far in %s (book id %d)\n", len(es), seriesName, book.ID)
	return nil
}

// modelClient resolves which key user's model calls run on and opens a
// client for it.
func (a *app) modelClient(ctx context.Context, st *store.Store, user store.User, model, anthropicDefault, effort string) (llm.Client, keys.Resolved, error) {
	res, err := resolveKey(ctx, st, user)
	if err != nil {
		return nil, res, err
	}
	res.Config = runConfig(res.Config, model, anthropicDefault, effort)
	client, err := llm.New(res.Config)
	if err != nil {
		return nil, res, err
	}
	fmt.Fprintf(a.out, "using %s: %s %s\n", res.Source, client.Provider(), client.Model())
	return client, res, nil
}

func resolveKey(ctx context.Context, st *store.Store, user store.User) (keys.Resolved, error) {
	bx, err := box()
	if err != nil {
		return keys.Resolved{}, err
	}
	return keys.Resolve(ctx, st, bx, user, keys.Env(os.Getenv))
}

// runConfig sets the model a run uses on a key: the one asked for, else the
// key's own, else (on Anthropic or Claude Code) the command's default.
func runConfig(c llm.Config, model, anthropicDefault, effort string) llm.Config {
	if model != "" {
		c.Model = model
	}
	if p := c.Provider; p == llm.ProviderAnthropic || p == llm.ProviderClaudeCode {
		if c.Model == "" {
			c.Model = anthropicDefault
		}
		c.Effort = effort
	}
	return c
}

// showPrice estimates adding b, and editing it afterwards, on the models
// those runs would use. Nothing here adds a reader or calls a model.
func (a *app) showPrice(ctx context.Context, b estimate.Book, model, effort, pricesFile string) error {
	prices, err := estimate.LoadPrices(pricesFile)
	if err != nil {
		return err
	}
	st, err := a.store()
	if err != nil {
		return err
	}
	user, ok, err := st.UserByName(ctx, a.userName)
	if err != nil {
		return err
	}
	if !ok {
		// Adding the book would add this reader, as admin if they're first.
		_, hasAdmin, err := st.Admin(ctx)
		if err != nil {
			return err
		}
		user = store.User{Name: a.userName, IsAdmin: !hasAdmin}
	}
	cfg := llm.Config{Provider: llm.ProviderAnthropic}
	if res, err := resolveKey(ctx, st, user); err != nil {
		fmt.Fprintf(a.out, "no key to price with (%v); assuming an Anthropic key\n", err)
	} else {
		cfg = res.Config
	}
	modelOf := func(c llm.Config) estimate.Model {
		return estimate.Model{Provider: c.Provider, Name: c.Model, Effort: c.Effort}
	}
	ingest, edit, err := estimate.ForBook(ctx, st, b,
		modelOf(runConfig(cfg, model, llm.DefaultIngestModel, effort)), modelOf(runConfig(cfg, "", llm.DefaultAnthropicModel, "medium")), prices)
	if err != nil {
		return err
	}
	if ingest.Model.Name == "" {
		fmt.Fprintln(a.out, "no model set on this key, so no estimate (add --model)")
		return nil
	}
	basis := "output guessed: no books added on this model yet"
	if ingest.Runs > 0 {
		basis = fmt.Sprintf("output ratio from %d %s added on this model", ingest.Runs, plural(ingest.Runs, "book", "books"))
	}
	fmt.Fprintf(a.out, "estimate on %s (%s):\n  %s\n", modelName(ingest.Model), basis, priceLine(ingest))
	basis = "notes guessed: no edits on this model yet"
	if edit.Runs > 0 {
		basis = fmt.Sprintf("notes scaled from %d past %s on this model", edit.Runs, plural(edit.Runs, "edit", "edits"))
	}
	fmt.Fprintf(a.out, "then `gmesh edit` on %s, a rough estimate (%s):\n  %s\n", modelName(edit.Model), basis, priceLine(edit))
	return nil
}

func modelName(m estimate.Model) string {
	s := m.Provider + " " + m.Name
	if m.Effort != "" {
		s += " at " + m.Effort + " effort"
	}
	return s
}

func priceLine(e estimate.Estimate) string {
	s := fmt.Sprintf("input %s tokens, output %s tokens, ", tokenRange(e.Input), tokenRange(e.Output))
	switch {
	case e.Plan:
		return s + "counts against your Claude plan"
	case !e.Priced:
		return s + fmt.Sprintf("no known price for %s (add one with --prices)", e.Model.Name)
	case e.Dollars.Hi < 0.01:
		return s + "under $0.01"
	}
	return s + fmt.Sprintf("about $%.2f–$%.2f", e.Dollars.Lo, e.Dollars.Hi)
}

func tokenRange(r estimate.Range) string {
	if lo, hi := tokenCount(r.Lo), tokenCount(r.Hi); lo != hi {
		return lo + "–" + hi
	}
	return tokenCount(r.Lo)
}

func tokenCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return strconv.FormatInt(n, 10)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

type planned struct {
	n       int
	title   string
	words   int
	skipped string
}

// prepare numbers every sentence in reading order and turns sections into
// chapters. A chapter keeps its section number, so the numbers --dry-run
// shows stay the chapter numbers even when some sections are skipped.
func prepare(b *epub.Book, skip map[int]bool) ([]store.Chapter, []store.Sentence, []planned) {
	var chapters []store.Chapter
	var sentences []store.Sentence
	var plan []planned
	para := 0
	for i, sec := range b.Sections {
		n := i + 1
		var sents []store.Sentence
		words := 0
		for _, p := range sec.Paragraphs {
			for _, s := range text.Split(p) {
				sents = append(sents, store.Sentence{Ord: len(sentences) + len(sents), ChapterIdx: n, Para: para, Text: s})
				words += len(strings.Fields(s))
			}
			para++
		}
		title := sec.Title
		if title == "" {
			title = fmt.Sprintf("Section %d", n)
		}
		p := planned{n: n, title: title, words: words}
		switch {
		case skip[n]:
			p.skipped = "--skip"
		case words < minSectionWords:
			p.skipped = "too short"
		}
		plan = append(plan, p)
		if p.skipped != "" {
			continue
		}
		chapters = append(chapters, store.Chapter{Idx: n, Title: title, FirstOrd: sents[0].Ord, LastOrd: sents[len(sents)-1].Ord})
		sentences = append(sentences, sents...)
	}
	return chapters, sentences, plan
}

// parseSkip reads a list like "1-3,40".
func parseSkip(s string) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := a, error(nil)
		if isRange {
			b, err2 = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err1 != nil || err2 != nil || a < 1 || b < a {
			return nil, fmt.Errorf("can't read --skip %q", part)
		}
		for i := a; i <= b; i++ {
			out[i] = true
		}
	}
	return out, nil
}

func seriesOf(b *epub.Book, name string, index float64) (string, float64) {
	if name == "" {
		name = b.Series
	}
	if name == "" {
		name = b.Title
	}
	if index == 0 {
		index = b.SeriesIndex
	}
	if index == 0 {
		index = 1
	}
	return name, index
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *app) rm(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "really remove it")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: gmesh rm BOOK --yes")
	}
	b, err := a.book(ctx, pos[0])
	if err != nil {
		return err
	}
	if !*yes {
		return fmt.Errorf("this deletes %q, its notes, and everything first met in it (even notes from later books about those). Add --yes to go ahead", b.Title)
	}
	if err := a.st.DeleteBook(ctx, b.ID); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "removed %s\n", b.Title)
	return nil
}
