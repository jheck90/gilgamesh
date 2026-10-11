package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openAIClient speaks the chat-completions API that OpenAI, OpenRouter,
// Ollama, llama.cpp and vLLM all serve.
type openAIClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	backoff time.Duration
}

func newOpenAI(c Config) *openAIClient {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &openAIClient{baseURL: base, apiKey: c.APIKey, model: c.Model, http: &http.Client{Timeout: 15 * time.Minute}, backoff: 2 * time.Second}
}

func (o *openAIClient) Provider() string { return ProviderOpenAI }
func (o *openAIClient) Model() string    { return o.model }

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func (o *openAIClient) JSON(ctx context.Context, req Request) ([]byte, Usage, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.model,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   req.SchemaName,
				"strict": true,
				"schema": req.Schema,
			},
		},
	})
	if err != nil {
		return nil, Usage{}, err
	}

	var resp chatResponse
	backoff := o.backoff
	for attempt := 1; ; attempt++ {
		status, raw, err := o.post(ctx, body)
		if err == nil && status == http.StatusOK {
			if err := json.Unmarshal(raw, &resp); err != nil {
				return nil, Usage{}, fmt.Errorf("openai: decoding reply: %w", err)
			}
			break
		}
		retryable := err != nil || status == http.StatusTooManyRequests || status >= 500
		if !retryable || attempt == 5 {
			if err != nil {
				return nil, Usage{}, err
			}
			return nil, Usage{}, fmt.Errorf("openai: HTTP %d: %s", status, bytes.TrimSpace(raw))
		}
		select {
		case <-ctx.Done():
			return nil, Usage{}, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	u := Usage{InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens}
	if len(resp.Choices) == 0 {
		return nil, u, errors.New("openai: reply had no choices")
	}
	c := resp.Choices[0]
	switch {
	case c.Message.Refusal != "":
		return nil, u, fmt.Errorf("openai: the model declined this passage: %s", c.Message.Refusal)
	case c.FinishReason == "length":
		return nil, u, errors.New("openai: reply was cut off; try a smaller --chunk-words")
	}
	return []byte(c.Message.Content), u, nil
}

func (o *openAIClient) post(ctx context.Context, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	res, err := o.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	return res.StatusCode, raw, err
}
