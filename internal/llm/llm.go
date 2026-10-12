// Package llm talks to whichever model provider the reader brought a key
// for. Every call asks for JSON matching a schema.
package llm

import (
	"context"
	"fmt"
)

type Request struct {
	System     string
	User       string
	SchemaName string
	Schema     map[string]any
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

type Client interface {
	Provider() string
	Model() string
	// JSON returns the model's reply, which should match req.Schema.
	JSON(ctx context.Context, req Request) ([]byte, Usage, error)
}

type Config struct {
	Provider string // "anthropic", "openai" (any OpenAI-compatible API) or "claude-code"
	APIKey   string
	BaseURL  string
	Model    string
	Effort   string // anthropic only: low, medium, high, xhigh, max
}

const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
	// ProviderClaudeCode runs calls through the `claude` CLI on a Claude
	// subscription. APIKey holds its OAuth token.
	ProviderClaudeCode = "claude-code"

	DefaultAnthropicModel = "claude-opus-5-5"
	// DefaultIngestModel reads the book. Haiku takes notes nearly as well
	// for a fraction of the cost, and `gmesh edit` tidies them on Opus.
	DefaultIngestModel = "claude-haiku-5-5"
)

func New(c Config) (Client, error) {
	switch c.Provider {
	case ProviderAnthropic:
		return newAnthropic(c), nil
	case ProviderOpenAI:
		if c.Model == "" {
			return nil, fmt.Errorf("llm: provider openai needs a model name")
		}
		return newOpenAI(c), nil
	case ProviderClaudeCode:
		return newClaudeCode(c), nil
	}
	return nil, fmt.Errorf("llm: unknown provider %q (want %s, %s or %s)", c.Provider, ProviderAnthropic, ProviderOpenAI, ProviderClaudeCode)
}
