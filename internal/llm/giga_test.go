package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGigaChat_Auth_And_Complete(t *testing.T) {
	var oauthHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		oauthHits++
		if r.Header.Get("Authorization") != "Basic test-b64" {
			t.Errorf("oauth auth header = %q", r.Header.Get("Authorization"))
		}
		_ = r.ParseForm()
		if r.Form.Get("scope") != "GIGACHAT_API_PERS" {
			t.Errorf("scope = %q", r.Form.Get("scope"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "tok-123",
			"expires_at":   time.Now().Add(30*time.Minute).UnixMilli(),
		})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			t.Errorf("completion auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(gigaResponse{
			Choices: []gigaChoice{{Message: gigaRespMsg{Role: "assistant", Content: "Здравствуйте"}}},
			Usage:   gigaUsage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GigaChat{
		authBase64: "test-b64",
		scope:      "GIGACHAT_API_PERS",
		authURL:    srv.URL + "/oauth",
		baseURL:    srv.URL,
		client:     srv.Client(),
	}

	for i := 0; i < 2; i++ { // второй вызов должен переиспользовать токен
		res, err := g.Complete(context.Background(), CompleteRequest{
			Model:    "GigaChat",
			Messages: []Message{{Role: RoleUser, Content: "привет"}},
		})
		if err != nil {
			t.Fatalf("Complete error: %v", err)
		}
		if res.Content != "Здравствуйте" {
			t.Errorf("content = %q", res.Content)
		}
		if res.TokensIn != 10 || res.TokensOut != 4 {
			t.Errorf("tokens = %d/%d", res.TokensIn, res.TokensOut)
		}
	}
	if oauthHits != 1 {
		t.Errorf("oauth hits = %d, want 1 (token must be cached)", oauthHits)
	}
}

func TestGigaChat_ToolCall(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_at": time.Now().Add(time.Hour).UnixMilli()})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(gigaResponse{
			Choices: []gigaChoice{{Message: gigaRespMsg{
				Role: "assistant",
				ToolCalls: []gigaToolCall{{
					ID: "call_1", Type: "function",
					Function: gigaToolFn{Name: "search_customer", Arguments: `{"phone":"+79991234567"}`},
				}},
			}}},
			Usage: gigaUsage{PromptTokens: 15, CompletionTokens: 6},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GigaChat{authURL: srv.URL + "/oauth", baseURL: srv.URL, client: srv.Client()}
	res, err := g.Complete(context.Background(), CompleteRequest{Model: "GigaChat-Pro"})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "search_customer" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments["phone"] != "+79991234567" {
		t.Errorf("args = %v", res.ToolCalls[0].Arguments)
	}
}

func TestGigaChat_Stream(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "expires_at": time.Now().Add(time.Hour).UnixMilli()})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"content":"При"}}]}`,
			`{"choices":[{"delta":{"content":"вет"}}]}`,
			`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GigaChat{authURL: srv.URL + "/oauth", baseURL: srv.URL, client: srv.Client()}
	ch, err := g.Stream(context.Background(), StreamRequest{Model: "GigaChat", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}

	var text strings.Builder
	var done bool
	var tokensOut int
	for ev := range ch {
		switch ev.Type {
		case EventText:
			text.WriteString(ev.Text)
		case EventDone:
			done = true
			tokensOut = ev.TokensOut
		case EventError:
			t.Fatalf("stream error event: %v", ev.Error)
		}
	}
	if text.String() != "Привет" {
		t.Errorf("streamed text = %q, want %q", text.String(), "Привет")
	}
	if !done {
		t.Error("no done event")
	}
	if tokensOut != 2 {
		t.Errorf("tokensOut = %d, want 2", tokensOut)
	}
}
