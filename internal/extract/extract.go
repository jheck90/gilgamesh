// Package extract reads a stored book in order, one chunk at a time, and
// asks a model to take position-tagged notes on its entities.
//
// The model only ever sees the current chunk plus what the reader would
// already know at its start, and validate() drops anything the chunk
// doesn't support. Reading the notes back later needs no model at all.
package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
	"github.com/jheck90/gilgamesh/internal/text"
)

const (
	DefaultChunkWords = 6000
	maxCandidates     = 200
	recentEntities    = 12
	recentFacts       = 3
	retryEmptyWords   = 300 // a reply with no facts for a passage this long gets one retry
)

type Extractor struct {
	Store      *store.Store
	LLM        llm.Client
	UserID     int64 // who is adding the book
	KeyOwnerID int64 // whose key pays
	// Allow is called before each chunk; returning an error stops the run
	// (for example when a borrowed key's monthly allowance runs out).
	Allow   func(ctx context.Context) error
	Log     io.Writer
	Verbose bool
	// MaxCalls stops the run after this many chunks (0 for no limit); the
	// next run picks up from there.
	MaxCalls int
}

// ErrPaused is returned when a run stops at MaxCalls.
var ErrPaused = errors.New("extract: paused at --max-calls")

// Run extracts the remaining chunks of book, resuming where a previous run
// stopped.
func (x *Extractor) Run(ctx context.Context, b store.Book) error {
	prog, err := x.Store.Progress(ctx, b.ID)
	if err != nil {
		return err
	}
	chapters, err := x.Store.Chapters(ctx, b.ID)
	if err != nil {
		return err
	}
	sentences, err := x.Store.Sentences(ctx, b.ID)
	if err != nil {
		return err
	}
	chunks := buildChunks(chapters, sentences, prog.ChunkWords)
	if len(chunks) != prog.TotalChunks {
		return fmt.Errorf("extract: book splits into %d chunks now but %d were planned; remove it and ingest again", len(chunks), prog.TotalChunks)
	}

	for i := prog.NextChunk; i < len(chunks); i++ {
		if x.MaxCalls > 0 && i-prog.NextChunk >= x.MaxCalls {
			return ErrPaused
		}
		if x.Allow != nil {
			if err := x.Allow(ctx); err != nil {
				return err
			}
		}
		c := chunks[i]
		started := time.Now()
		k, cands, types, err := x.knownAt(ctx, b, c)
		if err != nil {
			return err
		}
		req := llm.Request{System: systemPrompt, User: renderUser(b, c, cands, types), SchemaName: "passage_notes", Schema: outputSchema}

		var out output
		var usage llm.Usage
		retriedGarbled, retriedEmpty := false, false
		for {
			raw, u, err := x.LLM.JSON(ctx, req)
			usage.InputTokens += u.InputTokens
			usage.OutputTokens += u.OutputTokens
			if err == nil {
				out = output{}
				if err = json.Unmarshal(raw, &out); err == nil {
					// Now and then a reply skips a whole passage, and asking
					// again almost always fills it in. A real chapter with
					// nothing new in it is rare enough that one retry is cheap.
					if out.facts() == 0 && !retriedEmpty && c.words() >= retryEmptyWords {
						retriedEmpty = true
						x.logf("  chunk %d: reply had no facts; retrying\n", i+1)
						continue
					}
					break
				}
				err = fmt.Errorf("reply wasn't valid JSON: %w", err)
			}
			// The clients already retry rate limits and server errors, so
			// only a garbled reply is worth asking for again.
			var jsonErr *json.SyntaxError
			if retriedGarbled || !errors.As(err, &jsonErr) {
				x.saveUsage(ctx, b, usage)
				return fmt.Errorf("extract: chunk %d/%d (%s): %w", i+1, len(chunks), c.Chapter.Title, err)
			}
			retriedGarbled = true
			x.logf("  chunk %d: %v; retrying\n", i+1, err)
		}

		w, warnings := validate(out, c, k)
		w.BookID, w.SeriesID, w.NextChunk = b.ID, b.SeriesID, i+1
		w.Usage = x.usageRec(usage)
		st, err := x.Store.ApplyChunk(ctx, w)
		if err != nil {
			return err
		}

		part := ""
		if c.Parts > 1 {
			part = fmt.Sprintf(" (%d/%d)", c.Part, c.Parts)
		}
		x.logf("[%d/%d] %s%s: %d new, %d facts, %d aliases, %d dropped · %s in / %s out · %s\n",
			i+1, len(chunks), c.Chapter.Title, part, st.NewEntities, st.Facts, st.Aliases, len(warnings),
			kTokens(usage.InputTokens), kTokens(usage.OutputTokens), time.Since(started).Round(time.Second))
		if x.Verbose {
			for _, wn := range warnings {
				x.logf("    - %s\n", wn)
			}
		}
	}
	return nil
}

// knownAt gathers what a reader knows at the start of chunk c: the
// candidates to offer the model (entities named in the chunk, in full or
// in part, the narrator, and whoever was active most recently) and a name
// index over every visible entity.
func (x *Extractor) knownAt(ctx context.Context, b store.Book, c chunk) (known, []candidate, []string, error) {
	pos := store.At(b, c.firstOrd()-1)
	all, err := x.Store.VisibleEntities(ctx, pos)
	if err != nil {
		return known{}, nil, nil, err
	}
	k := known{cands: map[int64]candidate{}, byName: map[string]int64{}}
	typeSet := map[string]bool{}
	for _, e := range all {
		typeSet[e.Type] = true
		k.byName[text.Key(e.Name)] = e.ID
		for _, a := range e.Aliases {
			k.byName[text.Key(a)] = e.ID
		}
	}

	var picked []store.EntitySummary
	pick := func(e store.EntitySummary) {
		if _, ok := k.cands[e.ID]; ok || len(picked) >= maxCandidates {
			return
		}
		k.cands[e.ID] = candidate{EntitySummary: e}
		picked = append(picked, e)
	}
	for _, e := range all {
		if e.Narrator || c.mentionsAny(append([]string{e.Name}, e.Aliases...)) {
			pick(e)
		}
	}
	// A passage often uses part of a name ("Guyen" for "Vrie Guyen"). If
	// that entity isn't offered, the model can only start a new one.
	passage := c.text()
	for _, e := range all {
		for _, w := range text.NameWords(append([]string{e.Name}, e.Aliases...)...) {
			if text.ContainsWord(passage, w) {
				pick(e)
				break
			}
		}
	}
	for _, e := range store.MostRecent(all, recentEntities) {
		pick(e)
	}

	cands := make([]candidate, 0, len(picked))
	for _, e := range picked {
		facts, err := x.Store.VisibleFacts(ctx, e.ID, pos, recentFacts)
		if err != nil {
			return known{}, nil, nil, err
		}
		cand := k.cands[e.ID]
		for _, f := range facts {
			cand.Recent = append(cand.Recent, f.Text)
		}
		k.cands[e.ID] = cand
		cands = append(cands, cand)
	}

	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, t)
	}
	sort.Strings(types)
	return k, cands, types, nil
}

func (x *Extractor) usageRec(u llm.Usage) *store.UsageRec {
	return &store.UsageRec{UserID: x.UserID, KeyOwnerID: x.KeyOwnerID, Provider: x.LLM.Provider(), Model: x.LLM.Model(),
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens}
}

// saveUsage records tokens spent on a chunk that failed, so caps stay
// honest. Progress is left where it was.
func (x *Extractor) saveUsage(ctx context.Context, b store.Book, u llm.Usage) {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return
	}
	p, err := x.Store.Progress(ctx, b.ID)
	if err != nil {
		return
	}
	x.Store.ApplyChunk(context.WithoutCancel(ctx), store.ChunkWrite{BookID: b.ID, SeriesID: b.SeriesID, NextChunk: p.NextChunk, Usage: x.usageRec(u)})
}

func (x *Extractor) logf(format string, args ...any) {
	if x.Log != nil {
		fmt.Fprintf(x.Log, format, args...)
	}
}

func kTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
