package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/jheck90/gilgamesh/internal/keys"
	"github.com/jheck90/gilgamesh/internal/llm"
	"github.com/jheck90/gilgamesh/internal/store"
)

func (a *app) key(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: gmesh key set|rm|status|request|list|approve|deny|revoke")
	}
	user, err := a.user(ctx)
	if err != nil {
		return err
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "set":
		return a.keySet(ctx, user, rest)
	case "rm":
		if err := a.st.DeleteAPIKey(ctx, user.ID); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "removed your key")
		return nil
	case "status":
		return a.keyStatus(ctx, user)
	case "request":
		fs := flag.NewFlagSet("key request", flag.ContinueOnError)
		note := fs.String("note", "", "a note for the admin")
		if _, err := parse(fs, rest); err != nil {
			return err
		}
		if err := a.st.RequestGrant(ctx, user.ID, *note); err != nil {
			return err
		}
		g, _, err := a.st.GetGrant(ctx, user.ID)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.out, "your request to use the admin's key is %s\n", g.Status)
		return nil
	case "list", "approve", "deny", "revoke":
		if !user.IsAdmin {
			return fmt.Errorf("only the admin can %s key requests", sub)
		}
		return a.keyAdmin(ctx, user, sub, rest)
	}
	return fmt.Errorf("unknown key command %q", sub)
}

func (a *app) keySet(ctx context.Context, user store.User, args []string) error {
	fs := flag.NewFlagSet("key set", flag.ContinueOnError)
	provider := fs.String("provider", llm.ProviderAnthropic, "anthropic or openai (any OpenAI-compatible API)")
	model := fs.String("model", "", "default model for this key")
	baseURL := fs.String("base-url", "", "API base URL, for OpenRouter, Ollama and the like")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if _, err := llm.New(llm.Config{Provider: *provider, Model: *model}); err != nil {
		return err
	}
	if *provider == llm.ProviderClaudeCode {
		return errors.New("a Claude Code key can't be stored per reader yet: the admin sets GMESH_PROVIDER=claude-code and CLAUDE_CODE_OAUTH_TOKEN (from `claude setup-token`) in the server's environment")
	}
	bx, err := box()
	if err != nil {
		return err
	}
	if bx == nil {
		return errors.New("set GMESH_SECRET first so the key can be stored encrypted (make one with `gmesh keygen`)")
	}
	key, err := readSecret("API key: ")
	if err != nil {
		return err
	}
	if key == "" && *baseURL == "" {
		return errors.New("no key given")
	}
	err = a.st.SetAPIKey(ctx, user.ID, store.APIKey{
		Provider: *provider, BaseURL: *baseURL, Model: *model,
		Sealed: bx.Seal([]byte(key), keys.Context(user.ID)),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "saved your %s key\n", *provider)
	return nil
}

// readSecret reads without echo from a terminal, or the first line of
// piped input.
func readSecret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (a *app) keyStatus(ctx context.Context, user store.User) error {
	role := ""
	if user.IsAdmin {
		role = " (admin)"
	}
	fmt.Fprintf(a.out, "user: %s%s\n", user.Name, role)
	k, ok, err := a.st.GetAPIKey(ctx, user.ID)
	if err != nil {
		return err
	}
	if ok {
		model := k.Model
		if model == "" {
			model = "default model"
		}
		fmt.Fprintf(a.out, "your key: %s, %s\n", k.Provider, model)
	} else {
		fmt.Fprintln(a.out, "your key: none")
	}
	if !user.IsAdmin {
		g, ok, err := a.st.GetGrant(ctx, user.ID)
		if err != nil {
			return err
		}
		switch {
		case !ok:
			fmt.Fprintln(a.out, "admin's key: not requested")
		case g.Status == store.GrantApproved && g.MonthlyCap > 0:
			used, err := a.st.BorrowedTokensThisMonth(ctx, user.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.out, "admin's key: approved, %d of %d tokens used this month\n", used, g.MonthlyCap)
		default:
			fmt.Fprintf(a.out, "admin's key: %s\n", g.Status)
		}
	}
	n, err := a.st.TokensThisMonth(ctx, user.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "tokens used this month: %d\n", n)
	return nil
}

func (a *app) keyAdmin(ctx context.Context, admin store.User, sub string, args []string) error {
	if sub == "list" {
		gs, err := a.st.Grants(ctx)
		if err != nil {
			return err
		}
		if len(gs) == 0 {
			fmt.Fprintln(a.out, "no requests")
		}
		for _, g := range gs {
			line := fmt.Sprintf("%-16s %s", g.UserName, g.Status)
			if g.MonthlyCap > 0 {
				line += fmt.Sprintf(", cap %d tokens/month", g.MonthlyCap)
			}
			if g.Note != "" {
				line += fmt.Sprintf(" · %q", g.Note)
			}
			fmt.Fprintln(a.out, line)
		}
		return nil
	}
	fs := flag.NewFlagSet("key "+sub, flag.ContinueOnError)
	capTokens := fs.Int64("cap", 0, "monthly token cap (0 for none)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: gmesh key %s USER", sub)
	}
	target, ok, err := a.st.UserByName(ctx, pos[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no user named %q", pos[0])
	}
	status := map[string]string{"approve": store.GrantApproved, "deny": store.GrantDenied, "revoke": store.GrantRevoked}[sub]
	if err := a.st.DecideGrant(ctx, target.ID, status, *capTokens, admin.ID); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s: %s\n", target.Name, status)
	return nil
}
