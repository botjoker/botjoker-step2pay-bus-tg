package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Минимальный HS256 JWT (без внешних зависимостей) для internal-JWT и SSE-токенов.

type jwtClaims struct {
	ConversationID string `json:"cid,omitempty"`
	ProfileID      string `json:"pid,omitempty"`
	AgentID        string `json:"aid,omitempty"`
	Scope          string `json:"scope,omitempty"`
	Exp            int64  `json:"exp"`
	Iat            int64  `json:"iat"`
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func signHS256(secret, signingInput string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return b64(mac.Sum(nil))
}

// issueJWT создаёт подписанный токен с заданными claims и TTL.
func issueJWT(secret string, claims jwtClaims, ttl time.Duration, now time.Time) (string, error) {
	claims.Iat = now.Unix()
	claims.Exp = now.Add(ttl).Unix()

	header := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := b64(payloadBytes)
	signingInput := header + "." + payload
	return signingInput + "." + signHS256(secret, signingInput), nil
}

// IssueInternalToken выдаёт internal-JWT (scope tool-exec) — для tools/billing
// клиентов, которым нужен Bearer к backend/runtime. Экспортируется для cmd/agent.
func IssueInternalToken(secret string, ttl time.Duration) (string, error) {
	return issueJWT(secret, jwtClaims{Scope: "tool-exec"}, ttl, time.Now())
}

var errInvalidToken = errors.New("invalid token")

// verifyJWT проверяет подпись и срок, возвращает claims.
func verifyJWT(secret, token string, now time.Time) (*jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errInvalidToken
	}
	signingInput := parts[0] + "." + parts[1]
	expected := signHS256(secret, signingInput)
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, errInvalidToken
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errInvalidToken
	}
	var claims jwtClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, errInvalidToken
	}
	if claims.Exp > 0 && now.Unix() > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}
	return &claims, nil
}
