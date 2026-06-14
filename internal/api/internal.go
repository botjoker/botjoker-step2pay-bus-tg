package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/botjoker/sambacrm-business-tg/internal/llm"
	"github.com/google/uuid"
)

// detachedContext — фоновый контекст для fire-and-forget обработки сообщения
// (переживает завершение HTTP-запроса).
func detachedContext() context.Context { return context.Background() }

type internalTestRequest struct {
	AgentID string `json:"agent_id"`
	Message string `json:"message"`
}

// handleInternalTest — admin playground: прогон одного сообщения, ответ стримом SSE.
func (s *Server) handleInternalTest(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	var req internalTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}
	agentID, err := uuid.Parse(req.AgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad agent_id")
		return
	}

	events, err := s.engine.Test(r.Context(), agentID, req.Message)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	for ev := range events {
		msg := sseMessage{Type: ev.Type, Text: ev.Text}
		if ev.ToolCall != nil {
			msg.Tool = ev.ToolCall.Name
		}
		if ev.Error != nil {
			msg.Error = ev.Error.Error()
		}
		b, _ := json.Marshal(msg)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		if ev.Type == llm.EventDone || ev.Type == llm.EventError {
			break
		}
	}
}

type internalIngestRequest struct {
	SourceID  string `json:"source_id"`
	ProfileID string `json:"profile_id"`
}

// handleInternalIngest — триггер индексации источника знаний (от backend).
func (s *Server) handleInternalIngest(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	var req internalIngestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}
	sourceID, err1 := uuid.Parse(req.SourceID)
	profileID, err2 := uuid.Parse(req.ProfileID)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "bad ids")
		return
	}
	if err := s.engine.TriggerIngest(r.Context(), sourceID, profileID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

type operatorMessageRequest struct {
	ConversationID    string `json:"conversation_id"`
	OperatorAccountID string `json:"operator_account_id"`
	Text              string `json:"text"`
	Mode              string `json:"mode"`
}

// handleOperatorMessage — пересылка сообщения оператора (live takeover) в транспорт.
func (s *Server) handleOperatorMessage(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	var req operatorMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad body")
		return
	}
	convID, err1 := uuid.Parse(req.ConversationID)
	opID, err2 := uuid.Parse(req.OperatorAccountID)
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "bad ids")
		return
	}
	if err := s.engine.OperatorMessage(r.Context(), convID, opID, req.Text, req.Mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// handleAuthRefresh выдаёт internal-JWT (1ч) для tools/registry (tool-exec).
// Защищён тем же internalSecret: вызывающий должен прислать действующий Bearer.
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if s.internalSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "internal auth not configured")
		return
	}
	token, err := issueJWT(s.internalSecret, jwtClaims{Scope: "tool-exec"}, time.Hour, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}
