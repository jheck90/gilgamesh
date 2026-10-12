package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
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

// Readers can't store a Claude Code key yet: only the admin's environment
// holds one (#24).
func TestKeySetRefusesClaudeCode(t *testing.T) {
	t.Setenv("GMESH_SECRET", "")
	var out bytes.Buffer
	err := run(context.Background(), []string{"-db", filepath.Join(t.TempDir(), "g.db"), "key", "set", "--provider", "claude-code"}, &out)
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("err = %v, want it to point at the admin's environment", err)
	}
}

// A Claude Code key reads on Haiku and tidies on Opus too, at the effort
// asked for.
func TestModelClientClaudeCodeDefault(t *testing.T) {
	t.Setenv("GMESH_SECRET", "")
	t.Setenv("GMESH_PROVIDER", "claude-code")
	t.Setenv("GMESH_MODEL", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-tok")
	ctx := context.Background()
	a := &app{dbPath: filepath.Join(t.TempDir(), "g.db"), userName: "me", out: &bytes.Buffer{}}
	defer a.close()
	st, _ := a.store()
	user, _ := a.user(ctx)

	client, res, err := a.modelClient(ctx, st, user, "", llm.DefaultIngestModel, "high")
	if err != nil {
		t.Fatal(err)
	}
	if client.Model() != "claude-haiku-5-5" || res.Config.Effort != "high" {
		t.Errorf("model = %q, effort = %q; want claude-haiku-5-5 at high", client.Model(), res.Config.Effort)
	}
}

// Approving a request for a Claude Code key would grant something that can
// never be used.
func TestKeyApproveRefusesClaudeCode(t *testing.T) {
	t.Setenv("GMESH_SECRET", "")
	t.Setenv("GMESH_PROVIDER", "claude-code")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-tok")
	db := filepath.Join(t.TempDir(), "g.db")
	gm := func(args ...string) error {
		return run(context.Background(), append([]string{"-db", db}, args...), &bytes.Buffer{})
	}
	if err := gm("key", "status"); err != nil { // the first user is the admin
		t.Fatal(err)
	}
	if err := gm("-user", "alice", "key", "request"); err != nil {
		t.Fatal(err)
	}
	if err := gm("key", "approve", "alice"); err == nil || !strings.Contains(err.Error(), "can't be borrowed") {
		t.Errorf("approve: err = %v, want a refusal", err)
	}
}
