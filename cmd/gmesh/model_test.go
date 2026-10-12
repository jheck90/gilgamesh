package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/jheck90/gilgamesh/internal/llm"
)

// Each command has its own default model on Anthropic, but a model asked
// for, or set on the key, still wins.
func TestModelClientDefault(t *testing.T) {
	t.Setenv("GMESH_SECRET", "")
	t.Setenv("GMESH_PROVIDER", "")
	t.Setenv("GMESH_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	ctx := context.Background()
	a := &app{dbPath: filepath.Join(t.TempDir(), "g.db"), userName: "me", out: &bytes.Buffer{}}
	defer a.close()
	st, err := a.store()
	if err != nil {
		t.Fatal(err)
	}
	user, err := a.user(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, envModel, asked, def, want string
	}{
		{"ingest default", "", "", llm.DefaultIngestModel, "claude-haiku-5-5"},
		{"edit default", "", "", llm.DefaultAnthropicModel, "claude-opus-5-5"},
		{"key's model", "claude-sonnet-5-5", "", llm.DefaultIngestModel, "claude-sonnet-5-5"},
		{"asked for", "claude-sonnet-5-5", "claude-opus-5-5", llm.DefaultIngestModel, "claude-opus-5-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GMESH_MODEL", tc.envModel)
			client, _, err := a.modelClient(ctx, st, user, tc.asked, tc.def, "medium")
			if err != nil {
				t.Fatal(err)
			}
			if got := client.Model(); got != tc.want {
				t.Errorf("model = %q, want %q", got, tc.want)
			}
		})
	}
}
