package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// HTTPBillingTracker реализует BillingTracker через backend internal-эндпоинты.
// hard-cap кешируется (TTL 30с), чтобы не дёргать backend на каждое сообщение.
type HTTPBillingTracker struct {
	backendURL  string
	internalJWT func() (string, error)
	client      *http.Client

	mu       sync.RWMutex
	capCache map[uuid.UUID]capCacheEntry
}

type capCacheEntry struct {
	hardCapHit bool
	expiresAt  time.Time
}

func nowFunc() time.Time { return time.Now() }

// NewBillingTracker создаёт HTTP-трекер.
func NewBillingTracker(backendURL string, jwt func() (string, error)) *HTTPBillingTracker {
	return &HTTPBillingTracker{
		backendURL:  backendURL,
		internalJWT: jwt,
		client:      &http.Client{Timeout: 5 * time.Second},
		capCache:    make(map[uuid.UUID]capCacheEntry),
	}
}

var _ BillingTracker = (*HTTPBillingTracker)(nil)

// IsHardCapHit возвращает кешированный статус; на промахе фетчит usage,
// при ошибке — fail-open (false), чтобы сбой биллинга не блокировал ответы.
func (h *HTTPBillingTracker) IsHardCapHit(ctx context.Context, profileID uuid.UUID) (bool, error) {
	h.mu.RLock()
	if e, ok := h.capCache[profileID]; ok && nowFunc().Before(e.expiresAt) {
		h.mu.RUnlock()
		return e.hardCapHit, nil
	}
	h.mu.RUnlock()

	hit, err := h.fetchHardCap(ctx, profileID)
	if err != nil {
		return false, nil // fail-open
	}
	h.setCache(profileID, hit, 30*time.Second)
	return hit, nil
}

// Track инкрементит usage; если backend вернул hard_cap_hit — обновляет кэш.
func (h *HTTPBillingTracker) Track(ctx context.Context, d BillingDelta) error {
	body, _ := json.Marshal(map[string]any{
		"messages_delta": d.Messages,
		"tokens_in":      d.TokensIn,
		"tokens_out":     d.TokensOut,
		"cost_usd":       d.CostUSD,
		"embeddings":     d.Embeddings,
		"proactive":      d.Proactive,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		h.backendURL+"/internal/agents/billing/track", bytes.NewReader(body))
	if err != nil {
		return err
	}
	if err := h.auth(req, d.ProfileID); err != nil {
		return err
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var result struct {
		HardCapHit bool `json:"hard_cap_hit"`
		SoftCapHit bool `json:"soft_cap_hit"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	if result.HardCapHit {
		h.setCache(d.ProfileID, true, 5*time.Minute) // дольше TTL при hit
	}
	return nil
}

func (h *HTTPBillingTracker) fetchHardCap(ctx context.Context, profileID uuid.UUID) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		h.backendURL+"/internal/agents/billing/usage", nil)
	if err != nil {
		return false, err
	}
	if err := h.auth(req, profileID); err != nil {
		return false, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var result struct {
		HardCapHit bool `json:"hard_cap_hit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}
	return result.HardCapHit, nil
}

func (h *HTTPBillingTracker) auth(req *http.Request, profileID uuid.UUID) error {
	if h.internalJWT != nil {
		token, err := h.internalJWT()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Profile-ID", profileID.String())
	req.Header.Set("Content-Type", "application/json")
	return nil
}

func (h *HTTPBillingTracker) setCache(profileID uuid.UUID, hit bool, ttl time.Duration) {
	h.mu.Lock()
	h.capCache[profileID] = capCacheEntry{hardCapHit: hit, expiresAt: nowFunc().Add(ttl)}
	h.mu.Unlock()
}
