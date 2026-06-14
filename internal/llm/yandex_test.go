package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newYandexWithServer подменяет endpoint на тестовый сервер.
func newYandexWithServer(t *testing.T, handler http.HandlerFunc) (*YandexGPT, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	y := &YandexGPT{
		apiKey:   "test-key",
		folderID: "test-folder",
		baseURL:  srv.URL,
		client:   srv.Client(),
	}
	return y, srv
}

func TestYandexGPT_Complete_Mock(t *testing.T) {
	resp := yandexResponse{}
	resp.Result.Alternatives = []yandexAlternative{{
		Message: yandexRespMsg{Role: "assistant", Text: "Привет!"},
		Status:  "ALTERNATIVE_STATUS_FINAL",
	}}
	resp.Result.Usage = yandexUsage{InputTextTokens: "12", CompletionTokens: "5", TotalTokens: "17"}

	var gotAuth, gotModelURI string
	y, srv := newYandexWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req yandexReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModelURI = req.ModelURI
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	res, err := y.Complete(context.Background(), CompleteRequest{
		Model:    "yandexgpt-lite",
		Messages: []Message{{Role: RoleUser, Content: "привет"}},
	})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if res.Content != "Привет!" {
		t.Errorf("content = %q, want %q", res.Content, "Привет!")
	}
	if res.TokensIn != 12 || res.TokensOut != 5 {
		t.Errorf("tokens = %d/%d, want 12/5", res.TokensIn, res.TokensOut)
	}
	if gotAuth != "Api-Key test-key" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotModelURI != "gpt://test-folder/yandexgpt-lite/latest" {
		t.Errorf("modelUri = %q", gotModelURI)
	}
}

func TestYandexGPT_Complete_ToolCall(t *testing.T) {
	resp := yandexResponse{}
	resp.Result.Alternatives = []yandexAlternative{{
		Message: yandexRespMsg{
			Role: "assistant",
			ToolCallList: &yandexToolCallList{ToolCalls: []yandexToolCall{{
				FunctionCall: yandexFunctionCall{
					Name:      "get_weather",
					Arguments: map[string]any{"city": "Москва"},
				},
			}}},
		},
		Status: "ALTERNATIVE_STATUS_FINAL",
	}}
	resp.Result.Usage = yandexUsage{InputTextTokens: "20", CompletionTokens: "8"}

	y, srv := newYandexWithServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(resp)
	})
	defer srv.Close()

	res, err := y.Complete(context.Background(), CompleteRequest{
		Model:    "yandexgpt",
		Messages: []Message{{Role: RoleUser, Content: "погода в москве?"}},
	})
	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(res.ToolCalls))
	}
	if res.ToolCalls[0].Name != "get_weather" {
		t.Errorf("tool name = %q", res.ToolCalls[0].Name)
	}
	if res.ToolCalls[0].Arguments["city"] != "Москва" {
		t.Errorf("tool args = %v", res.ToolCalls[0].Arguments)
	}
}

func TestYandexModelKey(t *testing.T) {
	cases := map[string]string{
		"yandexgpt-lite":                       "yandexgpt-lite",
		"yandexgpt":                            "yandexgpt",
		"gpt://folder/yandexgpt-lite/latest":   "yandexgpt-lite",
		"gpt://folder/yandexgpt/rc":            "yandexgpt",
		"some-llama-model":                     "llama",
	}
	for in, want := range cases {
		if got := yandexModelKey(in); got != want {
			t.Errorf("yandexModelKey(%q) = %q, want %q", in, got, want)
		}
	}
}
