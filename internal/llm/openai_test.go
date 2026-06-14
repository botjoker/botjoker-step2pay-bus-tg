package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newOAWithServer(t *testing.T, handler http.HandlerFunc) (*openAICompatible, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	o := newOpenAICompatible("openai", "sk-test", srv.URL, "gpt-4o-mini", withTools(true), withVision(true))
	o.client = srv.Client()
	return o, srv
}

func TestOpenAI_Complete(t *testing.T) {
	var gotAuth, gotModel string
	o, srv := newOAWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req oaReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		_ = json.NewEncoder(w).Encode(oaResponse{
			Choices: []oaChoice{{Message: oaRespMsg{Role: "assistant", Content: "Hello"}}},
			Usage:   &oaUsage{PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10},
		})
	})
	defer srv.Close()

	res, err := o.Complete(context.Background(), CompleteRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if res.Content != "Hello" {
		t.Errorf("content = %q", res.Content)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotModel != "gpt-4o-mini" { // дефолтная модель подставлена
		t.Errorf("model = %q", gotModel)
	}
	if res.CostUSD <= 0 {
		t.Errorf("cost not computed: %v", res.CostUSD)
	}
}

func TestOpenAI_ToolCall(t *testing.T) {
	o, srv := newOAWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(oaResponse{
			Choices: []oaChoice{{Message: oaRespMsg{
				Role: "assistant",
				ToolCalls: []oaToolCall{{
					ID: "call_1", Type: "function",
					Function: oaToolFn{Name: "book", Arguments: `{"date":"2026-07-01"}`},
				}},
			}}},
			Usage: &oaUsage{PromptTokens: 12, CompletionTokens: 5},
		})
	})
	defer srv.Close()

	res, err := o.Complete(context.Background(), CompleteRequest{Model: "gpt-4o"})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "book" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments["date"] != "2026-07-01" {
		t.Errorf("args = %v", res.ToolCalls[0].Arguments)
	}
}

func TestOpenAI_Stream(t *testing.T) {
	o, srv := newOAWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		chunks := []string{
			`{"choices":[{"delta":{"content":"He"}}]}`,
			`{"choices":[{"delta":{"content":"llo"}}]}`,
			`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer srv.Close()

	ch, err := o.Stream(context.Background(), StreamRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	var text strings.Builder
	var tokensOut int
	for ev := range ch {
		switch ev.Type {
		case EventText:
			text.WriteString(ev.Text)
		case EventDone:
			tokensOut = ev.TokensOut
		case EventError:
			t.Fatalf("error event: %v", ev.Error)
		}
	}
	if text.String() != "Hello" {
		t.Errorf("text = %q", text.String())
	}
	if tokensOut != 2 {
		t.Errorf("tokensOut = %d", tokensOut)
	}
}

func TestOpenAI_StreamToolCall(t *testing.T) {
	o, srv := newOAWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		chunks := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"name":"search","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"кофе\"}"}}]}}]}`,
			`{"choices":[{"delta":{}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	defer srv.Close()

	ch, _ := o.Stream(context.Background(), StreamRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	var calls []ToolCall
	for ev := range ch {
		if ev.Type == EventToolCall && ev.ToolCall != nil {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Arguments["q"] != "кофе" {
		t.Errorf("args = %v", calls[0].Arguments)
	}
}
