package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// claudeCodeClient runs each call through the `claude` CLI in print mode, so
// it's paid for by the Claude subscription the CLI is logged into rather
// than an API key. The model gets no tools, settings or MCP servers: all it
// can do is return JSON.
type claudeCodeClient struct {
	token  string // CLAUDE_CODE_OAUTH_TOKEN; empty uses the CLI's own login
	model  string
	effort string
}

func newClaudeCode(c Config) *claudeCodeClient {
	model := c.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &claudeCodeClient{token: c.APIKey, model: model, effort: c.Effort}
}

func (c *claudeCodeClient) Provider() string { return ProviderClaudeCode }
func (c *claudeCodeClient) Model() string    { return c.model }

// env is gmesh's environment minus any Anthropic API credentials, which the
// CLI would otherwise prefer to the subscription, plus the key's token.
func (c *claudeCodeClient) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN":
			continue
		}
		env = append(env, kv)
	}
	if c.token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+c.token)
	}
	return env
}

func (c *claudeCodeClient) JSON(ctx context.Context, req Request) ([]byte, Usage, error) {
	schema, err := json.Marshal(req.Schema)
	if err != nil {
		return nil, Usage{}, err
	}
	args := []string{"-p", "--output-format", "json", "--json-schema", string(schema),
		"--system-prompt", req.System, "--model", c.model,
		"--tools", "", "--restricted", "--strict-mcp-config", "--no-session-persistence"}
	if c.effort != "" {
		args = append(args, "--effort", c.effort)
	}
	// An empty directory, so no project's CLAUDE.md or settings come along.
	dir, err := os.MkdirTemp("", "gmesh-claude-")
	if err != nil {
		return nil, Usage{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	cmd.Env = c.env()
	cmd.Stdin = strings.NewReader(req.User)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()

	// A failed call (a usage limit, say) can still print a result saying why.
	var res struct {
		IsError bool            `json:"is_error"`
		Subtype string          `json:"subtype"`
		Result  string          `json:"result"`
		Output  json.RawMessage `json:"structured_output"`
		Usage   struct {
			Input         int64 `json:"input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			Output        int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		if runErr != nil {
			return nil, Usage{}, fmt.Errorf("claude: %v: %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, Usage{}, fmt.Errorf("claude: unreadable reply: %v", err)
	}
	if res.IsError || runErr != nil {
		msg := res.Result
		if msg == "" {
			msg = fmt.Sprintf("%v %s", runErr, strings.TrimSpace(stderr.String()))
		}
		return nil, Usage{}, fmt.Errorf("claude: %s", msg)
	}
	if len(res.Output) == 0 || string(res.Output) == "null" {
		return nil, Usage{}, fmt.Errorf("claude: no JSON in the reply (%s)", res.Subtype)
	}
	u := Usage{InputTokens: res.Usage.Input + res.Usage.CacheRead + res.Usage.CacheCreation, OutputTokens: res.Usage.Output}
	return res.Output, u, nil
}
