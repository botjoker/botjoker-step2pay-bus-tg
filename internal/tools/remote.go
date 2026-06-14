package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/runtime"
)

// RemoteExecutor выполняет CRM-инструменты через backend
// POST /internal/agents/tools/exec/:tool (internal-JWT). Вся бизнес-логика и
// повторная проверка profile_id — на стороне backend (не доверяем рантайму).
type RemoteExecutor struct {
	backendURL string
	jwtFactory func() (string, error)
	client     *http.Client
}

// NewRemoteExecutor создаёт исполнитель. jwtFactory выдаёт internal-JWT (с ротацией).
func NewRemoteExecutor(backendURL string, jwtFactory func() (string, error)) *RemoteExecutor {
	return &RemoteExecutor{
		backendURL: backendURL,
		jwtFactory: jwtFactory,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Exec вызывает backend tool-exec и возвращает JSON-результат.
func (r *RemoteExecutor) Exec(ctx context.Context, ec runtime.ToolExecCtx, name string, args map[string]any) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{"arguments": args})
	if err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/internal/agents/tools/exec/%s", r.backendURL, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	if r.jwtFactory != nil {
		token, err := r.jwtFactory()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Profile-ID", ec.ProfileID.String())
	req.Header.Set("X-Agent-ID", ec.AgentID.String())
	req.Header.Set("X-Conversation-ID", ec.ConversationID.String())
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]any
	raw, _ := io.ReadAll(resp.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &result)
	}
	if resp.StatusCode != http.StatusOK {
		msg := ""
		if result != nil {
			if e, ok := result["error"].(string); ok {
				msg = e
			}
		}
		return nil, fmt.Errorf("tool %s: http %d: %s", name, resp.StatusCode, msg)
	}
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}
