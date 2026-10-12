package keys

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jheck90/gilgamesh/internal/secrets"
	"github.com/jheck90/gilgamesh/internal/store"
)

func TestResolve(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	box, _ := secrets.NewBox(secrets.GenerateKey())
	admin, _ := st.EnsureUser(ctx, "default")
	alice, _ := st.EnsureUser(ctx, "alice")
	env := Env(func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-env"
		}
		return ""
	})

	r, err := Resolve(ctx, st, box, admin, env)
	if err != nil || r.Config.APIKey != "sk-env" || r.Source != "environment" {
		t.Fatalf("admin with env key: %+v, %v", r, err)
	}
	if _, err := Resolve(ctx, st, box, alice, env); err != ErrNoKey {
		t.Fatalf("alice without key or grant: %v", err)
	}

	st.RequestGrant(ctx, alice.ID, "")
	if _, err := Resolve(ctx, st, box, alice, env); err == nil {
		t.Fatal("a pending request should not unlock the admin key")
	}

	st.DecideGrant(ctx, alice.ID, store.GrantApproved, 100, admin.ID)
	r, err = Resolve(ctx, st, box, alice, env)
	if err != nil || r.OwnerID != admin.ID || !r.Borrowed(alice.ID) || r.Remaining != 100 {
		t.Fatalf("approved grant: %+v, %v", r, err)
	}

	// Spend past the cap.
	b := store.Book{SeriesID: must(st.EnsureSeries(ctx, "s")), Title: "b", SHA256: "x", SourcePath: "x"}
	bid, _ := st.CreateBook(ctx, b, nil, nil, 1, 1)
	st.ApplyChunk(ctx, store.ChunkWrite{BookID: bid, SeriesID: b.SeriesID, NextChunk: 1,
		Usage: &store.UsageRec{UserID: alice.ID, KeyOwnerID: admin.ID, Provider: "anthropic", Model: "m", InputTokens: 90, OutputTokens: 20}})
	if _, err := Resolve(ctx, st, box, alice, env); err == nil {
		t.Fatal("cap exceeded but key still resolved")
	}

	// Her own key always wins.
	st.SetAPIKey(ctx, alice.ID, store.APIKey{Provider: "openai", Model: "m", Sealed: box.Seal([]byte("sk-alice"), Context(alice.ID))})
	r, err = Resolve(ctx, st, box, alice, env)
	if err != nil || r.Config.APIKey != "sk-alice" || r.Borrowed(alice.ID) {
		t.Fatalf("own key: %+v, %v", r, err)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// A Claude Code key is the admin's subscription: theirs to use, never lent.
func TestClaudeCodeKeyIsNotBorrowed(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, _ := st.EnsureUser(ctx, "default")
	alice, _ := st.EnsureUser(ctx, "alice")
	env := Env(func(k string) string {
		switch k {
		case "GMESH_PROVIDER":
			return "claude-code"
		case "CLAUDE_CODE_OAUTH_TOKEN":
			return "oauth-tok"
		case "ANTHROPIC_API_KEY", "GMESH_API_KEY":
			return "sk-env" // an API key must never be handed to the CLI as its token
		}
		return ""
	})

	r, err := Resolve(ctx, st, nil, admin, env)
	if err != nil || r.Config.Provider != "claude-code" || r.Config.APIKey != "oauth-tok" {
		t.Fatalf("admin with a Claude Code key: %+v, %v", r, err)
	}

	st.RequestGrant(ctx, alice.ID, "")
	st.DecideGrant(ctx, alice.ID, store.GrantApproved, 0, admin.ID)
	if r, err := Resolve(ctx, st, nil, alice, env); err == nil {
		t.Fatalf("alice borrowed the admin's Claude Code key: %+v", r)
	}
}
