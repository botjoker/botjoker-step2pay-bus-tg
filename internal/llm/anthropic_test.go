package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAntWithServer(t *testing.T, handler http.HandlerFunc) (*Anthropic, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	a := &Anthropic{
		apiKey:       "ak-test",
		baseURL:      srv.URL,
		defaultModel: "claude-3-5-sonnet-latest",
		client:       srv.Client(),
	}
	return a, srv
}

func TestAnthropic_Complete_SystemExtraction(t *testing.T) {
	var gotSystem, gotVersion, gotKey string
	var gotRoles []string
	a, srv := newAntWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.Header.Get("anthropic-version")
		gotKey = r.Header.Get("x-api-key")
		var req antReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotSystem = req.System
		for _, m := range req.Messages {
			gotRoles = append(gotRoles, m.Role)
		}
		_ = json.NewEncoder(w).Encode(antResponse{
			Content: []antRespBlock{{Type: "text", Text: "Готово"}},
			Usage:   antUsage{InputTokens: 11, OutputTokens: 3},
		})
	})
	defer srv.Close()

	res, err := a.Complete(context.Background(), CompleteRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "Ты юрист."},
			{Role: RoleUser, Content: "Привет"},
		},
	})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if res.Content != "Готово" {
		t.Errorf("content = %q", res.Content)
	}
	if gotSystem != "Ты юрист." {
		t.Errorf("system = %q (должен быть вынесен из messages)", gotSystem)
	}
	if len(gotRoles) != 1 || gotRoles[0] != "user" {
		t.Errorf("roles = %v (system не должен попасть в messages)", gotRoles)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("version header = %q", gotVersion)
	}
	if gotKey != "ak-test" {
		t.Errorf("x-api-key = %q", gotKey)
	}
}

func TestAnthropic_Complete_ToolUse(t *testing.T) {
	a, srv := newAntWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(antResponse{
			StopReason: "tool_use",
			Content: []antRespBlock{
				{Type: "text", Text: "Секунду"},
				{Type: "tool_use", ID: "toolu_1", Name: "get_weather", Input: map[string]any{"city": "Сочи"}},
			},
			Usage: antUsage{InputTokens: 20, OutputTokens: 8},
		})
	})
	defer srv.Close()

	res, err := a.Complete(context.Background(), CompleteRequest{Model: "claude-3-5-sonnet-latest"})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if res.Content != "Секунду" {
		t.Errorf("content = %q", res.Content)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Arguments["city"] != "Сочи" {
		t.Errorf("args = %v", res.ToolCalls[0].Arguments)
	}
}

func TestAnthropic_Stream(t *testing.T) {
	a, srv := newAntWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"При"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"вет"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		}
		for _, e := range events {
			_, _ = w.Write([]byte("event: x\ndata: " + e + "\n\n"))
		}
	})
	defer srv.Close()

	ch, err := a.Stream(context.Background(), StreamRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	var text strings.Builder
	var tokensIn, tokensOut int
	for ev := range ch {
		switch ev.Type {
		case EventText:
			text.WriteString(ev.Text)
		case EventDone:
			tokensIn = ev.TokensIn
			tokensOut = ev.TokensOut
		case EventError:
			t.Fatalf("error event: %v", ev.Error)
		}
	}
	if text.String() != "Привет" {
		t.Errorf("text = %q", text.String())
	}
	if tokensIn != 9 || tokensOut != 2 {
		t.Errorf("tokens = %d/%d, want 9/2", tokensIn, tokensOut)
	}
}

func TestAnthropic_StreamToolUse(t *testing.T) {
	a, srv := newAntWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		events := []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_7","name":"search"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"кофе\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","usage":{"output_tokens":6}}`,
		}
		for _, e := range events {
			_, _ = w.Write([]byte("data: " + e + "\n\n"))
		}
	})
	defer srv.Close()

	ch, _ := a.Stream(context.Background(), StreamRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	var calls []ToolCall
	for ev := range ch {
		if ev.Type == EventToolCall && ev.ToolCall != nil {
			calls = append(calls, *ev.ToolCall)
		}
	}
	if len(calls) != 1 || calls[0].Name != "search" {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].ID != "toolu_7" {
		t.Errorf("id = %q", calls[0].ID)
	}
	if calls[0].Arguments["q"] != "кофе" {
		t.Errorf("args = %v", calls[0].Arguments)
	}
}
