package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

type anthropicClient struct {
	client anthropic.Client
	model  string
	effort string
}

func newAnthropic(c Config) *anthropicClient {
	opts := []option.RequestOption{option.WithAPIKey(c.APIKey), option.WithMaxRetries(4)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	model := c.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &anthropicClient{client: anthropic.NewClient(opts...), model: model, effort: c.Effort}
}

func (a *anthropicClient) Provider() string { return ProviderAnthropic }
func (a *anthropicClient) Model() string    { return a.model }

func (a *anthropicClient) JSON(ctx context.Context, req Request) ([]byte, Usage, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 64000,
		System: []anthropic.BetaTextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
		}},
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.User)),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffort(a.effort),
			Format: anthropic.BetaJSONOutputFormatParam{Schema: req.Schema},
		},
		// If a safety classifier declines a passage (fight scenes in a
		// dungeon book can trip one), let the API retry it on its
		// recommended fallback model instead of failing the chunk.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	}

	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, Usage{}, err
		}
	}
	if err := stream.Err(); err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			switch {
			case apiErr.StatusCode == 401:
				return nil, Usage{}, errors.New("anthropic: the API key was rejected")
			case strings.Contains(err.Error(), "credit balance is too low"):
				return nil, Usage{}, errors.New("anthropic: the account behind this key is out of credit (console.anthropic.com > Plans & Billing)")
			}
		}
		return nil, Usage{}, err
	}
	u := Usage{
		InputTokens:  msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens + msg.Usage.CacheCreationInputTokens,
		OutputTokens: msg.Usage.OutputTokens,
	}
	switch msg.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return nil, u, fmt.Errorf("anthropic: the model declined this passage (%s)", msg.StopDetails.Explanation)
	case anthropic.BetaStopReasonMaxTokens:
		return nil, u, errors.New("anthropic: reply hit max_tokens; try a smaller --chunk-words")
	}
	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return []byte(text.String()), u, nil
}
