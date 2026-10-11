package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAIRetriesAndParses(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("unexpected request %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if body["response_format"].(map[string]any)["type"] != "json_schema" {
			t.Errorf("no json_schema response_format in %s", raw)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":12,"completion_tokens":3}}`))
	}))
	defer srv.Close()

	c, err := New(Config{Provider: ProviderOpenAI, APIKey: "k", BaseURL: srv.URL + "/v1/", Model: "local"})
	if err != nil {
		t.Fatal(err)
	}
	c.(*openAIClient).backoff = time.Millisecond
	out, u, err := c.JSON(context.Background(), Request{System: "s", User: "u", SchemaName: "x", Schema: map[string]any{"type": "object"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"ok":true}` || u.InputTokens != 12 || u.OutputTokens != 3 || calls != 2 {
		t.Errorf("out=%s usage=%+v calls=%d", out, u, calls)
	}
}
