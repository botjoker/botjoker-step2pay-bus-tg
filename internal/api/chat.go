package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type chatStartRequest struct {
	AgentSlug      string `json:"agent_slug"`
	ExternalUserID string `json:"external_user_id"`
	ConsentGranted bool   `json:"consent_granted"`
}

type chatStartResponse struct {
	ConversationID string `json:"conversation_id"`
	SSEToken       string `json:"sse_token"`
}

// handleChatStart резолвит канал по web_slug, создаёт диалог, выдаёт SSE-токен.
func (s *Server) handleChatStart(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	var req chatStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.AgentSlug == "" {
		writeError(w, http.StatusBadRequest, "agent_slug required")
		return
	}
	extUser := req.ExternalUserID
	if extUser == "" {
		extUser = uuid.NewString()
	}

	res, err := s.engine.StartConversation(r.Context(), req.AgentSlug, extUser)
	if err != nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	token := ""
	if s.internalSecret != "" {
		token, _ = issueJWT(s.internalSecret, jwtClaims{
			ConversationID: res.ConversationID.String(),
			ProfileID:      res.ProfileID.String(),
			AgentID:        res.AgentID.String(),
			Scope:          "sse",
		}, 24*time.Hour, time.Now())
	}

	writeJSON(w, http.StatusOK, chatStartResponse{
		ConversationID: res.ConversationID.String(),
		SSEToken:       token,
	})
}

// handleSSE отдаёт SSE-стрим событий диалога (token в query).
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if s.sse == nil {
		writeError(w, http.StatusServiceUnavailable, "sse not configured")
		return
	}
	token := r.URL.Query().Get("token")
	claims, err := verifyJWT(s.internalSecret, token, time.Now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid sse token")
		return
	}
	convID, err := uuid.Parse(claims.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad conversation id")
		return
	}
	_ = s.sse.Stream(r.Context(), w, convID)
}

// handleChatHistory отдаёт видимую историю диалога веб-виджету (восстановление
// при перезагрузке/реконнекте). Токен — тот же sse_token (несёт conversation_id).
func (s *Server) handleChatHistory(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	token := r.URL.Query().Get("token")
	claims, err := verifyJWT(s.internalSecret, token, time.Now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	convID, err := uuid.Parse(claims.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad conversation id")
		return
	}
	msgs, err := s.engine.History(r.Context(), convID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "history failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

type chatMessageRequest struct {
	Token string `json:"token"`
	Text  string `json:"text"`
}

// handleChatMessage принимает сообщение и запускает агента (события идут в SSE).
func (s *Server) handleChatMessage(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	var req chatMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Text == "" {
		writeError(w, http.StatusBadRequest, "text required")
		return
	}
	claims, err := verifyJWT(s.internalSecret, req.Token, time.Now())
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	convID, err := uuid.Parse(claims.ConversationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad conversation id")
		return
	}

	// Запускаем обработку в фоне — события публикуются в SSE-хаб.
	go func() {
		_ = s.engine.HandleMessage(detachedContext(), convID, req.Text)
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
