package estimate

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/jheck90/gilgamesh/internal/extract"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
)

var (
	haiku = Model{Provider: llm.ProviderAnthropic, Name: "claude-haiku-5-5"}
	opus  = Model{Provider: llm.ProviderAnthropic, Name: "claude-opus-5-5"}
	// 30,000 words is 40,000 tokens of text.
	book = Book{Words: 30000, Calls: 5}
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestIngestInputIsTextPlusPromptPerCall(t *testing.T) {
	e := Ingest(book, haiku, nil, Builtin())
	perCall := int64(extract.PromptTokens())
	want := Range{40000 + 5*(perCall+knownLo), 40000 + 5*(perCall+knownHi)}
	if e.Input != want {
		t.Errorf("input = %+v, want %+v", e.Input, want)
	}
	if fewer := Ingest(Book{Words: 15000, Calls: 3}, haiku, nil, Builtin()); fewer.Input.Hi >= e.Input.Hi {
		t.Errorf("skipping chapters didn't lower the estimate: %+v vs %+v", fewer.Input, e.Input)
	}
}

func TestIngestOutputFallsBackWithoutHistory(t *testing.T) {
	e := Ingest(book, haiku, nil, Builtin())
	if e.Runs != 0 {
		t.Errorf("runs = %d, want 0", e.Runs)
	}
	want := Range{scale(e.Input.Lo, ingestFallback.lo), scale(e.Input.Hi, ingestFallback.hi)}
	if e.Output != want {
		t.Errorf("output = %+v, want %+v", e.Output, want)
	}
	if !e.Priced || e.Plan {
		t.Fatalf("haiku should be priced: %+v", e)
	}
	lo := (float64(e.Input.Lo)*0.10 + float64(e.Output.Lo)*0.50) / 1e6
	hi := (float64(e.Input.Hi)*0.10 + float64(e.Output.Hi)*0.50) / 1e6
	if !near(e.Dollars.Lo, lo) || !near(e.Dollars.Hi, hi) {
		t.Errorf("dollars = %+v, want %v–%v", e.Dollars, lo, hi)
	}
}

func TestIngestOutputLearnsFromPastBooks(t *testing.T) {
	past := []store.BookUsage{{Input: 1000, Output: 100}, {Input: 2000, Output: 600}, {Input: 0, Output: 0}}
	e := Ingest(book, haiku, past, Builtin())
	if e.Runs != 2 {
		t.Errorf("runs = %d, want 2 (a book with no tokens says nothing)", e.Runs)
	}
	if want := (Range{scale(e.Input.Lo, 0.1), scale(e.Input.Hi, 0.3)}); e.Output != want {
		t.Errorf("output = %+v, want %+v", e.Output, want)
	}
}

// Effort changes how much a model thinks, which is billed as output, so
// runs at the same effort say the most. Rows from before effort was
// recorded have none.
func TestIngestOutputPrefersTheSameEffort(t *testing.T) {
	past := []store.BookUsage{{Effort: "low", Input: 1000, Output: 100}, {Effort: "high", Input: 1000, Output: 500}}
	low := haiku
	low.Effort = "low"
	if e := Ingest(book, low, past, Builtin()); e.Runs != 1 || e.Output != (Range{scale(e.Input.Lo, 0.1), scale(e.Input.Hi, 0.1)}) {
		t.Errorf("at low effort: %+v, want the low-effort run only", e)
	}
	low.Effort = "max"
	if e := Ingest(book, low, past, Builtin()); e.Runs != 2 || e.Output != (Range{scale(e.Input.Lo, 0.1), scale(e.Input.Hi, 0.5)}) {
		t.Errorf("at an effort with no runs: %+v, want every run", e)
	}
}

func TestNoKnownPriceShowsTokensOnly(t *testing.T) {
	e := Ingest(book, Model{Provider: llm.ProviderOpenAI, Name: "llama-4"}, nil, Builtin())
	if e.Priced || e.Input.Hi == 0 {
		t.Errorf("unknown model: %+v, want tokens without dollars", e)
	}
}

func TestClaudeCodeCountsAgainstThePlan(t *testing.T) {
	e := Ingest(book, Model{Provider: llm.ProviderClaudeCode, Name: "claude-haiku-5-5"}, nil, Builtin())
	if e.Priced || !e.Plan {
		t.Errorf("claude code: %+v, want the plan and no dollars", e)
	}
}

func TestEditScalesPastNotesToThisBook(t *testing.T) {
	// A 3,000-word book (4,000 tokens) whose edit sent 600 tokens of notes.
	past := []store.BookUsage{{Input: 600, Output: 60, Words: 3000}}
	e := Edit(book, opus, past, Builtin())
	if e.Runs != 1 || e.Input != (Range{6000, 6000}) || e.Output != (Range{600, 600}) {
		t.Errorf("edit = %+v, want 6000 in and 600 out from one past edit", e)
	}
	if !near(e.Dollars.Lo, (6000*4.0+600*20.0)/1e6) {
		t.Errorf("dollars = %+v", e.Dollars)
	}
	if fb := Edit(book, opus, nil, Builtin()); fb.Runs != 0 || fb.Input.Lo == 0 || fb.Input.Lo > fb.Input.Hi {
		t.Errorf("edit fallback = %+v", fb)
	}
}

func TestPricesFileOverridesBuiltin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, []byte(`{"claude-haiku-5-5": {"input": 1, "output": 2}, "llama-4": {"input": 0.5, "output": 0.5}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPrices(path)
	if err != nil {
		t.Fatal(err)
	}
	if p["claude-haiku-5-5"] != (Price{Input: 1, Output: 2}) || p["llama-4"].Input != 0.5 {
		t.Errorf("file prices not used: %+v", p)
	}
	if p["claude-opus-5-5"] != Builtin()["claude-opus-5-5"] {
		t.Errorf("a model the file doesn't name lost its built-in price")
	}
	if p, err := LoadPrices(""); err != nil || p["claude-haiku-5-5"] != Builtin()["claude-haiku-5-5"] {
		t.Errorf("no file: %+v, %v", p, err)
	}
	if err := os.WriteFile(path, []byte(`{"claude-haiku-5-5": 3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrices(path); err == nil {
		t.Error("a malformed prices file was accepted")
	}
}
