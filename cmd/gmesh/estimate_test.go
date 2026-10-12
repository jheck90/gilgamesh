package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jheck90/gilgamesh/internal/epub/epubtest"
	"github.com/jheck90/gilgamesh/internal/store"
)

// A dry run prices the run it plans, on the model that run would use.
func TestDryRunPrice(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "lamp.epub")
	long := strings.Repeat("The lamp burned low while the keeper counted the ships coming into the harbour. ", 40)
	if err := epubtest.Write(book, epubtest.Book{Title: "The Lamp", Author: "Someone", Chapters: []epubtest.Chapter{
		{Title: "One", Paras: []string{long}}, {Title: "Two", Paras: []string{long}}, {Title: "Three", Paras: []string{long}},
	}}); err != nil {
		t.Fatal(err)
	}
	prices := filepath.Join(dir, "prices.json")
	if err := os.WriteFile(prices, []byte(`{"claude-haiku-5-5": {"input": 1000, "output": 1000}, "llama-4": {"input": 2000, "output": 2000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"GMESH_SECRET", "GMESH_PROVIDER", "GMESH_API_KEY", "GMESH_BASE_URL", "GMESH_MODEL", "GMESH_PRICES", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv(k, "")
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	db := filepath.Join(dir, "g.db")
	dry := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{"-db", db, "-user", "reader", "ingest", book, "--dry-run"}, args...), &out); err != nil {
			t.Fatalf("dry run %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	section := func(out, from string) string {
		i := strings.Index(out, from)
		if i < 0 {
			t.Fatalf("no %q in:\n%s", from, out)
		}
		return out[i:]
	}

	out := dry()
	if !strings.Contains(out, "estimate on anthropic claude-haiku-5-5 at medium effort") || !strings.Contains(out, "under $0.01") ||
		!strings.Contains(out, "no books added on this model yet") {
		t.Errorf("default estimate:\n%s", out)
	}
	if e := section(out, "then `gmesh edit`"); !strings.Contains(e, "claude-opus-5-5") || !strings.Contains(e, "rough") || !strings.Contains(e, "$") {
		t.Errorf("edit estimate:\n%s", e)
	}
	if skipped := dry("--skip", "1-2"); section(skipped, "estimate on") == section(out, "estimate on") {
		t.Errorf("--skip didn't change the estimate:\n%s", skipped)
	}

	byFlag := dry("--prices", prices)
	t.Setenv("GMESH_PRICES", prices)
	if byEnv := dry(); !strings.Contains(byFlag, "about $") || section(byFlag, "estimate on") != section(byEnv, "estimate on") {
		t.Errorf("prices file:\nflag:\n%s\nenv:\n%s", byFlag, byEnv)
	}
	t.Setenv("GMESH_PRICES", "")

	if out := dry("--model", "claude-sonnet-5-5", "--effort", "high"); !strings.Contains(out, "estimate on anthropic claude-sonnet-5-5 at high effort") {
		t.Errorf("--model:\n%s", out)
	}

	t.Setenv("GMESH_PROVIDER", "openai")
	t.Setenv("GMESH_BASE_URL", "http://localhost:1")
	t.Setenv("GMESH_MODEL", "llama-4")
	if out := dry(); strings.Contains(out, "$") || !strings.Contains(out, "no known price for llama-4") {
		t.Errorf("unknown price:\n%s", out)
	}
	if out := dry("--prices", prices); !strings.Contains(section(out, "estimate on"), "about $") {
		t.Errorf("a price from the file for an openai model:\n%s", out)
	}

	t.Setenv("GMESH_PROVIDER", "claude-code")
	t.Setenv("GMESH_MODEL", "")
	if out := dry(); strings.Contains(out, "$") || !strings.Contains(out, "counts against your Claude plan") {
		t.Errorf("claude code key:\n%s", out)
	}

	// Pricing a book adds no reader: the first one added becomes admin.
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, ok, _ := st.UserByName(context.Background(), "reader"); ok {
		t.Error("a dry run added the reader")
	}
}
