// Package keys decides whose API key pays for a reader's model calls.
//
// Readers bring their own key. A reader without one can ask to use the
// admin's key; the admin approves, optionally with a monthly token cap.
// Reading never calls a model, so this only matters for adding books.
package keys

import (
	"context"
	"errors"
	"fmt"

	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/secrets"
	"github.com/jheck90/gilgamesh/internal/store"
)

type Resolved struct {
	Config  llm.Config
	OwnerID int64  // whose key pays
	Source  string // for messages: "your key", "environment", "admin's key"
	// Remaining is the borrower's tokens left this month; -1 means no cap.
	Remaining int64
}

// Borrowed reports whether the key belongs to someone else.
func (r Resolved) Borrowed(userID int64) bool { return r.OwnerID != userID }

// Env reads the instance key from the environment. It belongs to the admin.
type Env func(string) string

func (env Env) config() (llm.Config, bool) {
	provider := env("GMESH_PROVIDER")
	if provider == "" {
		provider = llm.ProviderAnthropic
	}
	key := env("GMESH_API_KEY")
	if key == "" {
		switch provider {
		case llm.ProviderAnthropic:
			key = env("ANTHROPIC_API_KEY")
		case llm.ProviderOpenAI:
			key = env("OPENAI_API_KEY")
		}
	}
	base := env("GMESH_BASE_URL")
	// Local OpenAI-compatible servers often need no key at all.
	if key == "" && !(provider == llm.ProviderOpenAI && base != "") {
		return llm.Config{}, false
	}
	return llm.Config{Provider: provider, APIKey: key, BaseURL: base, Model: env("GMESH_MODEL")}, true
}

var ErrNoKey = errors.New("no API key: add your own with `gmesh key set`, or ask to use the admin's with `gmesh key request`")

// Resolve picks the key for user: their own, else (for the admin) the
// environment, else the admin's key if the user has an approved grant with
// allowance left.
func Resolve(ctx context.Context, st *store.Store, box *secrets.Box, user store.User, env Env) (Resolved, error) {
	if cfg, ok, err := stored(ctx, st, box, user.ID); err != nil || ok {
		return Resolved{Config: cfg, OwnerID: user.ID, Source: "your key", Remaining: -1}, err
	}
	if user.IsAdmin {
		if cfg, ok := env.config(); ok {
			return Resolved{Config: cfg, OwnerID: user.ID, Source: "environment", Remaining: -1}, nil
		}
		return Resolved{}, errors.New("no API key: set one with `gmesh key set` or export ANTHROPIC_API_KEY (or GMESH_PROVIDER=openai with OPENAI_API_KEY / GMESH_BASE_URL)")
	}

	g, ok, err := st.GetGrant(ctx, user.ID)
	if err != nil {
		return Resolved{}, err
	}
	if !ok {
		return Resolved{}, ErrNoKey
	}
	if g.Status != store.GrantApproved {
		return Resolved{}, fmt.Errorf("your request to use the admin's key is %s", g.Status)
	}
	remaining := int64(-1)
	if g.MonthlyCap > 0 {
		used, err := st.BorrowedTokensThisMonth(ctx, user.ID)
		if err != nil {
			return Resolved{}, err
		}
		if remaining = g.MonthlyCap - used; remaining <= 0 {
			return Resolved{}, fmt.Errorf("you've used this month's allowance on the admin's key (%d of %d tokens)", used, g.MonthlyCap)
		}
	}
	admin, ok, err := st.Admin(ctx)
	if err != nil || !ok {
		return Resolved{}, fmt.Errorf("no admin found: %v", err)
	}
	cfg, ok, err := stored(ctx, st, box, admin.ID)
	if err != nil {
		return Resolved{}, err
	}
	if !ok {
		if cfg, ok = env.config(); !ok {
			return Resolved{}, errors.New("your request is approved, but the admin hasn't set up a key yet")
		}
	}
	return Resolved{Config: cfg, OwnerID: admin.ID, Source: "admin's key", Remaining: remaining}, nil
}

func stored(ctx context.Context, st *store.Store, box *secrets.Box, userID int64) (llm.Config, bool, error) {
	k, ok, err := st.GetAPIKey(ctx, userID)
	if err != nil || !ok {
		return llm.Config{}, false, err
	}
	if box == nil {
		return llm.Config{}, false, errors.New("a stored API key needs GMESH_SECRET to open it")
	}
	plain, err := box.Open(k.Sealed, Context(userID))
	if err != nil {
		return llm.Config{}, false, err
	}
	return llm.Config{Provider: k.Provider, APIKey: string(plain), BaseURL: k.BaseURL, Model: k.Model}, true, nil
}

// Context is the sealing context for a user's key.
func Context(userID int64) string { return fmt.Sprintf("user:%d", userID) }
