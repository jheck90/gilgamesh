package llm

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeClaude puts a `claude` on PATH that records what it was given and
// prints reply, exiting with code. It returns the directory the recordings
// land in.
func fakeClaude(t *testing.T, reply string, code int) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
d="` + dir + `"
for a in "$@"; do printf '%s\n' "$a"; done > "$d/args"
cat > "$d/stdin"
pwd > "$d/pwd"
env > "$d/env"
cat <<'REPLY'
` + reply + `
REPLY
exit ` + strconv.Itoa(code) + `
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestClaudeCodeReturnsStructuredOutput(t *testing.T) {
	dir := fakeClaude(t, `{"type":"result","subtype":"success","is_error":false,"result":"{\"ok\":true}",
		"structured_output":{"ok":true},
		"usage":{"input_tokens":10,"cache_read_input_tokens":900,"cache_creation_input_tokens":90,"output_tokens":7}}`, 0)

	c, err := New(Config{Provider: ProviderClaudeCode, APIKey: "oauth-tok", Model: "claude-haiku-5-5", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	out, u, err := c.JSON(context.Background(), Request{System: "be terse", User: "the passage", SchemaName: "x",
		Schema: map[string]any{"type": "object"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("out = %s", out)
	}
	if u.InputTokens != 1000 || u.OutputTokens != 7 {
		t.Errorf("usage = %+v, want 1000 in (cache included) / 7 out", u)
	}
	if got := read(t, filepath.Join(dir, "stdin")); got != "the passage" {
		t.Errorf("stdin = %q", got)
	}
	args := read(t, filepath.Join(dir, "args"))
	for _, want := range []string{"-p\n", "--output-format\njson\n", "--json-schema\n{\"type\":\"object\"}\n",
		"--system-prompt\nbe terse\n", "--model\nclaude-haiku-5-5\n", "--effort\nlow\n",
		"--tools\n\n", "--restricted\n", "--strict-mcp-config\n", "--no-session-persistence\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}
}

// An API key in the environment would take precedence over the
// subscription's login and bill the API instead.
func TestClaudeCodeBillsTheSubscription(t *testing.T) {
	dir := fakeClaude(t, `{"is_error":false,"structured_output":{},"usage":{}}`, 0)
	t.Setenv("ANTHROPIC_API_KEY", "sk-api")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-auth")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "someone-elses")

	c, _ := New(Config{Provider: ProviderClaudeCode, APIKey: "oauth-tok"})
	if _, _, err := c.JSON(context.Background(), Request{Schema: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	env := read(t, filepath.Join(dir, "env"))
	for _, banned := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "someone-elses"} {
		if strings.Contains(env, banned) {
			t.Errorf("claude was run with %s", banned)
		}
	}
	if !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=oauth-tok\n") {
		t.Errorf("claude wasn't given the key's token")
	}
	wd, _ := os.Getwd()
	if pwd := strings.TrimSpace(read(t, filepath.Join(dir, "pwd"))); pwd == wd {
		t.Errorf("claude ran in gmesh's working directory %s, not an empty one", pwd)
	}
}

// A usage limit comes back as an error result; its message says when the
// limit resets, so it's what the reader needs to see.
func TestClaudeCodeReportsTheCLIsError(t *testing.T) {
	fakeClaude(t, `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets 4pm (America/Denver)","usage":{}}`, 1)

	c, _ := New(Config{Provider: ProviderClaudeCode})
	_, _, err := c.JSON(context.Background(), Request{Schema: map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "You've hit your limit · resets 4pm (America/Denver)") {
		t.Errorf("err = %v, want the CLI's message", err)
	}
}

func TestClaudeCodeNeedsAReply(t *testing.T) {
	fakeClaude(t, `{"type":"result","subtype":"error_max_structured_output_retries","is_error":false,"usage":{}}`, 0)

	c, _ := New(Config{Provider: ProviderClaudeCode})
	if out, _, err := c.JSON(context.Background(), Request{Schema: map[string]any{}}); err == nil {
		t.Errorf("no structured output, but got %s and no error", out)
	}
}
