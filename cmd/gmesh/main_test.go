package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jheck90/gilgamesh/internal/epub/epubtest"
)

// TestEndToEnd drives the real CLI against a fake OpenAI-compatible server:
// ingest a two-chapter book, then read it back from different positions.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "crawl.epub")
	err := epubtest.Write(book, epubtest.Book{
		Title: "The Crawl", Author: "Someone", Series: "Crawl Saga", SeriesIndex: "1",
		Chapters: []epubtest.Chapter{
			{Title: "Chapter One", Paras: []string{
				"Carl woke up in the dungeon with a terrible headache and no idea where his boots had gone.",
				"It was Day 1 of the crawl, and the announcer would not stop talking about sponsors.",
			}},
			{Title: "Chapter Two", Paras: []string{
				"The hooded man watched Carl from across the saferoom while sipping something that smelled awful. Carl waved at him anyway.",
				"Everyone called the hooded man Mordecai, and he was apparently their guide.",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	editCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &req)
		prompt := req.Messages[1].Content
		var reply string
		switch {
		case strings.Contains(prompt, "ENTITIES (in order of first appearance"):
			editCalls++
			if !strings.Contains(prompt, "F3 ch2: Carl waved at the hooded man.") {
				t.Errorf("edit prompt:\n%s", prompt)
			}
			reply = `{"category_map": [], "merges": [], "drop_entities": [], "drop_aliases": [],
				"drop_facts": ["F3"], "drop_markers": []}`
		case strings.Contains(prompt, "[2] Carl woke up"):
			reply = `{"entities": [{"entity_id": "", "name": "Carl", "type": "character", "is_narrator": false, "new_aliases": [],
				"facts": [{"text": "Carl woke up in the dungeon without his boots.", "sentences": [2]}]}],
				"time_markers": [{"label": "Day 1", "sentence": 3}]}`
		case strings.Contains(prompt, "E1 [character] Carl"):
			reply = `{"entities": [
				{"entity_id": "E1", "name": "Carl", "type": "character", "is_narrator": false, "new_aliases": [],
				 "facts": [{"text": "Carl waved at the hooded man.", "sentences": [3]}]},
				{"entity_id": "", "name": "Mordecai", "type": "character", "is_narrator": false,
				 "new_aliases": [{"alias": "the hooded man", "sentence": 2}],
				 "facts": [{"text": "Mordecai is the crawlers' guide.", "sentences": [4]}]}], "time_markers": []}`
		default:
			t.Errorf("unexpected prompt:\n%s", prompt)
		}
		body, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": reply}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 5},
		})
		w.Write(body)
	}))
	defer srv.Close()

	t.Setenv("GMESH_PROVIDER", "openai")
	t.Setenv("GMESH_BASE_URL", srv.URL)
	t.Setenv("GMESH_MODEL", "fake")
	t.Setenv("GMESH_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	db := filepath.Join(dir, "g.db")

	gm := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(context.Background(), append([]string{"-db", db}, args...), &out)
		return out.String(), err
	}
	mustRun := func(args ...string) string {
		t.Helper()
		out, err := gm(args...)
		if err != nil {
			t.Fatalf("gmesh %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}

	out := mustRun("ingest", book, "--dry-run")
	if !strings.Contains(out, "series: Crawl Saga #1") || !strings.Contains(out, "2 chapters") {
		t.Errorf("dry run output:\n%s", out)
	}
	mustRun("ingest", book)
	if out := mustRun("ingest", book); !strings.Contains(out, "already done") {
		t.Errorf("second ingest should be a no-op:\n%s", out)
	}

	if _, err := gm("card", "crawl", "Carl"); err == nil || !strings.Contains(err.Error(), "haven't said where you are") {
		t.Errorf("card with no position should refuse, got %v", err)
	}

	mustRun("pos", "crawl", "--chapter", "1")
	if _, err := gm("card", "crawl", "Mordecai"); err == nil || !strings.Contains(err.Error(), `nothing called "Mordecai" yet`) {
		t.Errorf("Mordecai shouldn't exist in chapter one, got %v", err)
	}
	if _, err := gm("card", "crawl", "hooded"); err == nil {
		t.Error("the hooded man alias leaked into chapter one")
	}
	out = mustRun("card", "crawl", "carl", "--sources")
	if !strings.Contains(out, "without his boots") || strings.Contains(out, "waved") || !strings.Contains(out, "› Carl woke up") {
		t.Errorf("Carl at chapter one:\n%s", out)
	}

	mustRun("pos", "crawl", "--chapter", "2")
	out = mustRun("card", "crawl", "the hooded man")
	if !strings.Contains(out, "Mordecai · character") || !strings.Contains(out, "also: the hooded man") || !strings.Contains(out, "guide") {
		t.Errorf("Mordecai at chapter two:\n%s", out)
	}
	out = mustRun("entities", "crawl")
	if !strings.Contains(out, "2 entries") {
		t.Errorf("entities:\n%s", out)
	}
	if out := mustRun("timeline", "crawl"); !strings.Contains(out, "Day 1") {
		t.Errorf("timeline:\n%s", out)
	}

	// The editor: review first, then apply the saved reply without asking
	// the model again.
	plan := filepath.Join(dir, "plan.json")
	out = mustRun("edit", "crawl", "--dry-run", "--save", plan)
	if !strings.Contains(out, "Facts dropped, of 3 (1):") || !strings.Contains(out, "Carl, ch2: Carl waved") || !strings.Contains(out, "nothing changed") {
		t.Errorf("edit dry run:\n%s", out)
	}
	if out := mustRun("card", "crawl", "Carl"); !strings.Contains(out, "waved") {
		t.Errorf("a dry run changed Carl:\n%s", out)
	}
	out = mustRun("edit", "crawl", "--from", plan)
	if !strings.Contains(out, "1 facts and 0 time markers dropped") || !strings.Contains(out, ".before-edit-1-") {
		t.Errorf("edit:\n%s", out)
	}
	if out := mustRun("card", "crawl", "Carl"); strings.Contains(out, "waved") || !strings.Contains(out, "without his boots") {
		t.Errorf("Carl after the edit:\n%s", out)
	}
	if editCalls != 1 {
		t.Errorf("the model was asked to edit %d times, want 1", editCalls)
	}
	if _, err := gm("edit", "crawl", "--from", plan); err == nil || !strings.Contains(err.Error(), "already edited") {
		t.Errorf("a second edit should need --again, got %v", err)
	}
	mustRun("edit", "crawl", "--from", plan, "--again")

	// A second reader starts with no position, and can't spend a key
	// without asking.
	if _, err := gm("-user", "alice", "card", "crawl", "Carl"); err == nil {
		t.Error("alice inherited the default user's position")
	}
	out = mustRun("-user", "alice", "key", "request", "--note", "book club")
	if !strings.Contains(out, "requested") {
		t.Errorf("request: %s", out)
	}
	if _, err := gm("-user", "alice", "key", "approve", "alice"); err == nil {
		t.Error("alice approved her own request")
	}
	out = mustRun("key", "approve", "alice", "--cap", "1000")
	if !strings.Contains(out, "alice: approved") {
		t.Errorf("approve: %s", out)
	}
	out = mustRun("-user", "alice", "key", "status")
	if !strings.Contains(out, fmt.Sprintf("0 of %d tokens", 1000)) {
		t.Errorf("status: %s", out)
	}
}

func TestParseSkip(t *testing.T) {
	got, err := parseSkip("1-3, 40")
	if err != nil || len(got) != 4 || !got[1] || !got[3] || !got[40] || got[4] {
		t.Errorf("parseSkip = %v, %v", got, err)
	}
	for _, bad := range []string{"x", "3-1", "0", "2-"} {
		if _, err := parseSkip(bad); err == nil {
			t.Errorf("parseSkip(%q) should fail", bad)
		}
	}
}
