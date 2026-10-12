// Package estimate puts a rough price on adding a book before anything is
// spent: `gmesh ingest --dry-run` shows it, and so will the library page.
//
// Input tokens come from the book itself. Output tokens follow the
// output-to-input ratios a model showed on past books, from the usage
// table (at the same effort where there are any), or a built-in guess
// until there are any. Prices come from a
// built-in table that a prices file can override; nothing is fetched.
package estimate

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"

	"github.com/jheck90/gilgamesh/internal/extract"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
)

// Tokens is roughly how many tokens a run of English words takes.
func Tokens(words int) int64 { return int64(words) * 4 / 3 }

// Range is a low and high count of tokens.
type Range struct{ Lo, Hi int64 }

// Dollars is a low and high price.
type Dollars struct{ Lo, Hi float64 }

// Model is what a run would call, and at what effort ("" for none).
type Model struct{ Provider, Name, Effort string }

// Book is what a run would read: its words left after --skip, and the
// calls they split into.
type Book struct{ Words, Calls int }

type Estimate struct {
	Model         Model
	Input, Output Range
	// Runs is how many past runs the output ratio was learned from; 0
	// means it is the built-in guess.
	Runs    int
	Priced  bool // false when the model has no known price
	Dollars Dollars
	// Plan is set for a Claude Code key: the calls count against a Claude
	// subscription, so there are no dollars to show.
	Plan bool
}

// Each extraction call also lists the entities the reader already knows
// that the passage may mention: a handful early in a book, dozens later.
const knownLo, knownHi = 1000, 6000

type ratio struct{ lo, hi float64 }

// Guesses for a model with no history yet.
var (
	// Output per input token when taking notes on a book.
	ingestFallback = ratio{0.10, 0.25}
	// Tokens of notes an edit sends per token of the book's text.
	notesFallback = ratio{0.04, 0.12}
	// Output per input token of an edit.
	editFallback = ratio{0.05, 0.25}
)

// Ingest estimates adding b on m. past is what m spent on earlier books.
func Ingest(b Book, m Model, past []store.BookUsage, p Prices) Estimate {
	text := Tokens(b.Words)
	perCall := int64(extract.PromptTokens())
	in := Range{text + int64(b.Calls)*(perCall+knownLo), text + int64(b.Calls)*(perCall+knownHi)}
	r, runs := learn(sameEffort(past, m.Effort), ingestFallback, outputRatio)
	return priced(Estimate{Model: m, Input: in, Output: Range{scale(in.Lo, r.lo), scale(in.Hi, r.hi)}, Runs: runs}, p)
}

// Edit estimates a later `gmesh edit` of b on m. An edit sends the book's
// notes, which don't exist yet, so their size is scaled from past edits.
func Edit(b Book, m Model, past []store.BookUsage, p Prices) Estimate {
	past = sameEffort(past, m.Effort)
	notes, runs := learn(past, notesFallback, func(u store.BookUsage) (float64, bool) {
		return float64(u.Input) / float64(Tokens(u.Words)), u.Words > 0 && u.Input > 0
	})
	out, _ := learn(past, editFallback, outputRatio)
	text := Tokens(b.Words)
	in := Range{scale(text, notes.lo), scale(text, notes.hi)}
	return priced(Estimate{Model: m, Input: in, Output: Range{scale(in.Lo, out.lo), scale(in.Hi, out.hi)}, Runs: runs}, p)
}

// ForBook estimates adding b on ingest and then editing it on edit, from
// the history in st.
func ForBook(ctx context.Context, st *store.Store, b Book, ingest, edit Model, p Prices) (Estimate, Estimate, error) {
	ip, err := st.IngestUsage(ctx, ingest.Name)
	if err != nil {
		return Estimate{}, Estimate{}, err
	}
	ep, err := st.EditUsage(ctx, edit.Name)
	if err != nil {
		return Estimate{}, Estimate{}, err
	}
	return Ingest(b, ingest, ip, p), Edit(b, edit, ep, p), nil
}

// sameEffort keeps the runs at effort, which changes how much a model
// thinks and so how much output it's billed for. If there are none, every
// run is better than a guess.
func sameEffort(past []store.BookUsage, effort string) []store.BookUsage {
	var same []store.BookUsage
	for _, u := range past {
		if u.Effort == effort {
			same = append(same, u)
		}
	}
	if len(same) == 0 {
		return past
	}
	return same
}

func outputRatio(u store.BookUsage) (float64, bool) {
	return float64(u.Output) / float64(u.Input), u.Input > 0
}

// learn returns the lowest and highest of the ratios past runs show, and
// how many runs had one.
func learn(past []store.BookUsage, fallback ratio, of func(store.BookUsage) (float64, bool)) (ratio, int) {
	var r ratio
	n := 0
	for _, u := range past {
		v, ok := of(u)
		if !ok {
			continue
		}
		if n == 0 || v < r.lo {
			r.lo = v
		}
		if n == 0 || v > r.hi {
			r.hi = v
		}
		n++
	}
	if n == 0 {
		return fallback, 0
	}
	return r, n
}

func scale(n int64, r float64) int64 { return int64(float64(n)*r + 0.5) }

func priced(e Estimate, p Prices) Estimate {
	if e.Model.Provider == llm.ProviderClaudeCode {
		e.Plan = true
		return e
	}
	pr, ok := p[e.Model.Name]
	if !ok {
		return e
	}
	e.Priced = true
	e.Dollars = Dollars{
		Lo: (float64(e.Input.Lo)*pr.Input + float64(e.Output.Lo)*pr.Output) / 1e6,
		Hi: (float64(e.Input.Hi)*pr.Input + float64(e.Output.Hi)*pr.Output) / 1e6,
	}
	return e
}

// Price is in dollars per million tokens.
type Price struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
}

// Prices maps a model name to its price.
type Prices map[string]Price

// Builtin returns Anthropic's list prices when this release was made.
// Claude Haiku 5.5 costs more for prompts over 100K tokens, which no
// extraction call comes near.
func Builtin() Prices {
	return Prices{
		"claude-fable-5-1":  {10, 50},
		"claude-fable-5":    {10, 50},
		"claude-opus-5-5":   {4, 20},
		"claude-opus-5":     {5, 25},
		"claude-opus-4-8":   {5, 25},
		"claude-opus-4-7":   {5, 25},
		"claude-opus-4-6":   {5, 25},
		"claude-sonnet-5-5": {2, 10},
		"claude-sonnet-5":   {2, 10},
		"claude-sonnet-4-6": {3, 15},
		"claude-haiku-5-5":  {0.10, 0.50},
		"claude-haiku-4-5":  {1, 5},
	}
}

// LoadPrices returns the built-in prices, with any in the JSON file at
// path (if path isn't empty) taking their place, like
//
//	{"claude-haiku-5-5": {"input": 0.10, "output": 0.50}}
func LoadPrices(path string) (Prices, error) {
	p := Builtin()
	if path == "" {
		return p, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file Prices
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("prices file %s: %w", path, err)
	}
	maps.Copy(p, file)
	return p, nil
}
