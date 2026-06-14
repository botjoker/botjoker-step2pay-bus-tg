package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
	"github.com/google/uuid"
)

func TestRemoteExecutor_Exec(t *testing.T) {
	pid := uuid.New()
	aid := uuid.New()

	var gotPath, gotAuth, gotProfile string
	var gotArgs map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotProfile = r.Header.Get("X-Profile-ID")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotArgs, _ = body["arguments"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"found": 3}})
	}))
	defer srv.Close()

	exec := NewRemoteExecutor(srv.URL, func() (string, error) { return "tok-xyz", nil })
	res, err := exec.Exec(context.Background(), runtime.ToolExecCtx{ProfileID: pid, AgentID: aid},
		"search_customers", map[string]any{"query": "иван"})
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	if gotPath != "/internal/agents/tools/exec/search_customers" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotProfile != pid.String() {
		t.Errorf("profile header = %q", gotProfile)
	}
	if gotArgs["query"] != "иван" {
		t.Errorf("args = %v", gotArgs)
	}
	if res["success"] != true {
		t.Errorf("result = %v", res)
	}
}

func TestRemoteExecutor_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden tool"})
	}))
	defer srv.Close()

	exec := NewRemoteExecutor(srv.URL, nil)
	_, err := exec.Exec(context.Background(), runtime.ToolExecCtx{}, "create_customer", nil)
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "forbidden tool") {
		t.Errorf("error = %v", err)
	}
}
