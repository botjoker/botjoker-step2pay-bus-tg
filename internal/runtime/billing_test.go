package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestBilling_TrackAndCap(t *testing.T) {
	pid := uuid.New()
	var trackHits int32
	var gotProfile, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/agents/billing/track":
			atomic.AddInt32(&trackHits, 1)
			gotProfile = r.Header.Get("X-Profile-ID")
			gotAuth = r.Header.Get("Authorization")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"hard_cap_hit": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	bt := NewBillingTracker(srv.URL, func() (string, error) { return "tok", nil })
	if err := bt.Track(context.Background(), BillingDelta{ProfileID: pid, Messages: 1, TokensIn: 10, TokensOut: 5}); err != nil {
		t.Fatalf("Track error: %v", err)
	}
	if gotProfile != pid.String() {
		t.Errorf("X-Profile-ID = %q", gotProfile)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}

	// после Track с hard_cap_hit=true кэш должен вернуть true БЕЗ запроса usage
	hit, err := bt.IsHardCapHit(context.Background(), pid)
	if err != nil {
		t.Fatalf("IsHardCapHit error: %v", err)
	}
	if !hit {
		t.Error("expected hard cap hit from cache")
	}
}

func TestBilling_HardCapFromUsage(t *testing.T) {
	pid := uuid.New()
	var usageHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/agents/billing/usage" {
			atomic.AddInt32(&usageHits, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"hard_cap_hit": true})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	bt := NewBillingTracker(srv.URL, nil)
	hit, _ := bt.IsHardCapHit(context.Background(), pid)
	if !hit {
		t.Error("expected hard cap from usage endpoint")
	}
	// второй вызов — из кэша, usage не дёргается повторно
	_, _ = bt.IsHardCapHit(context.Background(), pid)
	if usageHits != 1 {
		t.Errorf("usage hits = %d, want 1 (cached)", usageHits)
	}
}

func TestBilling_FailOpen(t *testing.T) {
	// backend недоступен → IsHardCapHit должен вернуть false, не ошибку
	bt := NewBillingTracker("http://127.0.0.1:1", nil)
	hit, err := bt.IsHardCapHit(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("expected fail-open, got err: %v", err)
	}
	if hit {
		t.Error("fail-open should return false")
	}
}
