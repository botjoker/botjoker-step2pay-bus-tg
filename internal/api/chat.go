package api

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

type chatStartRequest struct {
	AgentSlug      string `json:"agent_slug"`
	ExternalUserID string `json:"external_user_id"`
	ConsentGranted bool   `json:"consent_granted"`
}

func conversationFromToken(raw, secret string, allowedScopes ...string) (uuid.UUID, error) {
	claims, err := verifyJWT(secret, raw, time.Now())
	if err != nil {
		return uuid.Nil, err
	}
	allowed := false
	for _, scope := range allowedScopes {
		if claims.Scope == scope {
			allowed = true
			break
		}
	}
	if !allowed {
		return uuid.Nil, errInvalidToken
	}
	return uuid.Parse(claims.ConversationID)
}

// handleChatIntakeForm отдаёт серверную схему всех ещё не собранных полей.
// LLM выбирает только момент показа формы, но не её поля и типы.
func (s *Server) handleChatIntakeForm(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	rawToken := r.URL.Query().Get("token")
	convID, err := conversationFromToken(rawToken, s.internalSecret, "sse", "intake-form")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	fields, err := s.engine.IntakeForm(r.Context(), convID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "intake form failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"fields": fields,
		"consent": map[string]string{
			"text":        "Я согласен на обработку персональных данных.",
			"privacy_url": "/chat/privacy?token=" + url.QueryEscape(rawToken),
		},
	})
}

type chatIntakeSubmitRequest struct {
	Token          string         `json:"token"`
	Values         map[string]any `json:"values"`
	ConsentGranted bool           `json:"consent_granted"`
}

// handleChatIntakeSubmit сохраняет форму напрямую: значения не становятся
// сообщением чата и не запускают модель.
func (s *Server) handleChatIntakeSubmit(w http.ResponseWriter, r *http.Request) {
	if s.engine == nil {
		writeError(w, http.StatusServiceUnavailable, "engine not wired")
		return
	}
	var req chatIntakeSubmitRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	convID, err := conversationFromToken(req.Token, s.internalSecret, "sse", "intake-form")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if err := s.engine.SubmitIntake(r.Context(), convID, IntakeSubmission{
		Values:         req.Values,
		ConsentGranted: req.ConsentGranted,
		UserAgent:      r.UserAgent(),
	}); err != nil {
		var inputErr *IntakeValidationError
		if errors.As(err, &inputErr) {
			writeError(w, http.StatusBadRequest, inputErr.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "save intake failed")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleChatPrivacyPage(w http.ResponseWriter, r *http.Request) {
	convID, err := conversationFromToken(r.URL.Query().Get("token"), s.internalSecret, "sse", "intake-form")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired link")
		return
	}
	document, err := s.engine.ConsentDocument(r.Context(), convID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "privacy document failed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'")
	_, _ = w.Write([]byte(`<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + html.EscapeString(document.Title) + `</title><style>:root{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#172033;background:#f4f6fa}body{margin:0;padding:32px 16px}main{max-width:760px;margin:auto;background:#fdfdff;border:1px solid #d9deea;border-radius:16px;padding:clamp(22px,5vw,44px);box-shadow:0 12px 36px rgba(34,45,72,.1)}h1{margin:0 0 24px;font-size:clamp(24px,4vw,34px)}article{white-space:pre-wrap;line-height:1.7;color:#3f4a60}.version{margin-top:28px;color:#69748a;font-size:13px}</style></head><body><main><h1>` + html.EscapeString(document.Title) + `</h1><article>` + html.EscapeString(document.Body) + `</article><div class="version">Редакция: ` + html.EscapeString(document.Version) + `</div></main></body></html>`))
}

// handleChatIntakePage — единая безопасная форма для Telegram/VK. Мессенджер
// открывает её во встроенном браузере; данные отправляются напрямую в runtime.
func (s *Server) handleChatIntakePage(w http.ResponseWriter, r *http.Request) {
	if _, err := conversationFromToken(r.URL.Query().Get("token"), s.internalSecret, "intake-form"); err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired form link")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'none'; base-uri 'none'")
	_, _ = w.Write([]byte(intakePageHTML))
}

const intakePageHTML = `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Заполните данные для связи</title><style>
:root{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:#172033;background:#f4f6fa}*{box-sizing:border-box}body{margin:0;padding:24px 16px}main{max-width:560px;margin:0 auto;background:#fdfdff;border:1px solid #d9deea;border-radius:16px;padding:24px;box-shadow:0 12px 36px rgba(34,45,72,.1)}h1{font-size:22px;margin:0 0 8px}p{color:#59647a;line-height:1.5;margin:0 0 22px}.fields{display:grid;gap:16px}label{display:grid;gap:6px;font-size:14px;font-weight:600}small{font-weight:400;color:#68738a}input,textarea,select{width:100%;border:1px solid #aab3c4;border-radius:8px;background:#fff;padding:11px 12px;font:inherit;color:#172033}textarea{min-height:92px;resize:vertical}.consent{display:flex;align-items:flex-start;gap:9px;margin-top:18px;font-size:13px;font-weight:400;line-height:1.5;color:#505b70}.consent input{width:18px;height:18px;margin:1px 0 0;flex:none}.consent a{color:#245fc7}button{margin-top:20px;border:0;border-radius:8px;background:#6d3be8;color:#fff;padding:11px 18px;font:inherit;font-weight:700;cursor:pointer}button:disabled{opacity:.6}.error{margin-top:14px;color:#a51d2d}.success{padding:18px;border-radius:10px;background:#eaf8ef;color:#175c32}b{color:#a51d2d}</style></head><body><main><h1>Заполните данные для связи</h1><p>Менеджер свяжется с вами в ближайшее время.</p><form id="form"><div class="fields" id="fields"></div><label class="consent"><input id="consent" type="checkbox" required><span>Я согласен на <a id="privacy" target="_blank" rel="noopener noreferrer">обработку персональных данных</a>.</span></label><div class="error" id="error" role="alert"></div><button id="submit">Отправить</button></form><div id="success" class="success" hidden><strong>Спасибо! Контакт отправлен менеджеру.</strong></div></main><script>
const token=new URLSearchParams(location.search).get('token'),fields=document.getElementById('fields'),form=document.getElementById('form'),error=document.getElementById('error'),success=document.getElementById('success');const esc=s=>{const d=document.createElement('div');d.textContent=s;return d.innerHTML};
fetch('/chat/intake-form?token='+encodeURIComponent(token)).then(r=>r.ok?r.json():Promise.reject()).then(({fields:f,consent:c})=>{if(!f.length){form.hidden=true;success.hidden=false;return}document.getElementById('privacy').href=c.privacy_url;fields.innerHTML=f.map(x=>{let c;if(x.type==='multiline')c='<textarea data-key="'+esc(x.key)+'"'+(x.required?' required':'')+'></textarea>';else if(x.type==='enum'||x.type==='boolean'){const o=x.type==='boolean'?['true','false']:(x.options||[]);c='<select data-key="'+esc(x.key)+'"'+(x.required?' required':'')+'><option value="">Выберите</option>'+o.map(v=>'<option value="'+esc(v)+'">'+(x.type==='boolean'?(v==='true'?'Да':'Нет'):esc(v))+'</option>').join('')+'</select>'}else{const t=x.type==='phone'?'tel':x.type==='email'?'email':x.type==='number'?'number':x.type==='date'?'date':'text';c='<input type="'+t+'" data-key="'+esc(x.key)+'"'+(x.required?' required':'')+'>'}return '<label>'+esc(x.label)+(x.required?' <b>*</b>':'')+c+(x.why?'<small>'+esc(x.why)+'</small>':'')+'</label>'}).join('')}).catch(()=>{error.textContent='Ссылка недействительна или устарела'});
form.addEventListener('submit',async e=>{e.preventDefault();error.textContent='';const button=document.getElementById('submit');button.disabled=true;const values={};fields.querySelectorAll('[data-key]').forEach(x=>{if(x.value!=='')values[x.dataset.key]=x.value});try{const r=await fetch('/chat/intake-form',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({token,values,consent_granted:document.getElementById('consent').checked})});if(!r.ok){const b=await r.json();throw new Error(b.error||'Не удалось сохранить данные')}form.hidden=true;success.hidden=false}catch(e){error.textContent=e.message;button.disabled=false}});
</script></body></html>`

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
