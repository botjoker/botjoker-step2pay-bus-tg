package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/crypto/chacha20poly1305"
)

// POST /internal/llm/complete (CONTRACTS §3.2) — синхронный вызов LLM по
// per-tenant credential. Переиспользует llm.NewProvider + storage.GetCredential.
// Авторизация — internal JWT (роут обёрнут internalJWTRequired в server.go).

type llmCompleteMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmCompleteRequest struct {
	CredentialsID  string               `json:"credentials_id"`
	Messages       []llmCompleteMessage `json:"messages"`
	MaxTokens      int                  `json:"max_tokens,omitempty"`
	Temperature    float32              `json:"temperature,omitempty"`
	Model          string               `json:"model,omitempty"` // необязательный override
	JSONMode       bool                 `json:"json_mode,omitempty"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format,omitempty"`
}

type llmCompleteResponse struct {
	Content          string  `json:"content"`
	TokensIn         int     `json:"tokens_in"`
	TokensOut        int     `json:"tokens_out"`
	Model            string  `json:"model"`
	Provider         string  `json:"provider"`
	CostUSDEstimated float64 `json:"cost_usd_estimated"`
	LatencyMS        int     `json:"latency_ms"`
}

// defaultModelFor — дефолтная модель провайдера, если model не задан в запросе.
func defaultModelFor(provider string) string {
	switch provider {
	case "anthropic":
		return "claude-haiku-4-5"
	case "openai":
		return "gpt-4o-mini"
	case "openrouter":
		return "openai/gpt-4o-mini"
	case "deepseek":
		return "deepseek-chat"
	case "gigachat":
		return "GigaChat"
	case "yandex_gpt":
		return "yandexgpt-lite/latest"
	case "t_pro":
		return "t-pro"
	default:
		return ""
	}
}

func (s *Server) handleLLMComplete(w http.ResponseWriter, r *http.Request) {
	if s.queries == nil {
		writeError(w, http.StatusServiceUnavailable, "storage not wired")
		return
	}
	var req llmCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}
	credID, err := uuid.Parse(req.CredentialsID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad credentials_id")
		return
	}
	secretsKey := os.Getenv("AGENT_SECRETS_KEY")
	if secretsKey == "" {
		writeError(w, http.StatusServiceUnavailable, "AGENT_SECRETS_KEY not configured")
		return
	}

	row, err := s.queries.GetCredential(r.Context(), pgtype.UUID{Bytes: credID, Valid: true})
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if !row.IsActive {
		writeError(w, http.StatusBadRequest, "credential inactive")
		return
	}

	provider := row.CredentialType
	model := req.Model
	if model == "" {
		model = defaultModelFor(provider)
	}

	k1, err := decryptText(row.Key1, secretsKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt key1")
		return
	}
	k2, err := decryptText(row.Key2, secretsKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt key2")
		return
	}
	k3, err := decryptText(row.Key3, secretsKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt key3")
		return
	}

	// Семантика key1/2/3 — как в agentstore.LoadProviderCredentials:
	// Key3 = model (override для OpenAI-семейства), реальный 3-й ключ — в Extra.
	creds := llm.Credentials{
		ID:    credID,
		Key1:  k1,
		Key2:  k2,
		Key3:  model,
		Extra: map[string]string{"scope": "GIGACHAT_API_PERS"},
	}
	if k3 != "" {
		creds.Extra["key3"] = k3
	}

	// Политика — единичный allow-list (как в cmd/agent/main.go): credential
	// уже выбран владельцем тенанта, поэтому разрешаем именно его провайдер.
	policy := llm.ProfilePolicy{AllowedLLMProviders: []string{provider}}
	prov, err := llm.NewProvider(provider, creds, policy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	msgs := make([]llm.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, llm.Message{Role: llm.Role(m.Role), Content: m.Content})
	}
	jsonMode := req.JSONMode || (req.ResponseFormat != nil && req.ResponseFormat.Type != "")
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	start := time.Now()
	result, err := prov.Complete(r.Context(), llm.CompleteRequest{
		Model:       model,
		Messages:    msgs,
		Temperature: req.Temperature,
		MaxTokens:   maxTokens,
		JSONMode:    jsonMode,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	respModel := result.Model
	if respModel == "" {
		respModel = model
	}
	writeJSON(w, http.StatusOK, llmCompleteResponse{
		Content:          result.Content,
		TokensIn:         result.TokensIn,
		TokensOut:        result.TokensOut,
		Model:            respModel,
		Provider:         provider,
		CostUSDEstimated: result.CostUSD,
		LatencyMS:        int(time.Since(start).Milliseconds()),
	})
}

// decryptText — XChaCha20-Poly1305 (формат backend utils/crypto.rs:
// base64(nonce[24]||ciphertext)). Дублирует agentstore.decryptSecret, т.к. пакет
// api НЕ может импортировать agentstore (agentstore импортирует api → цикл).
func decryptText(t pgtype.Text, keyB64 string) (string, error) {
	if !t.Valid || t.String == "" {
		return "", nil
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(keyB64))
	if err != nil {
		return "", fmt.Errorf("AGENT_SECRETS_KEY must be base64: %w", err)
	}
	if len(key) != chacha20poly1305.KeySize {
		return "", fmt.Errorf("AGENT_SECRETS_KEY must be 32 bytes")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	buf, err := base64.StdEncoding.DecodeString(strings.TrimSpace(t.String))
	if err != nil {
		return "", fmt.Errorf("ciphertext not base64: %w", err)
	}
	ns := chacha20poly1305.NonceSizeX
	if len(buf) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	plain, err := aead.Open(nil, buf[:ns], buf[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed: %w", err)
	}
	return string(plain), nil
}
