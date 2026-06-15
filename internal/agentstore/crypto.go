package agentstore

import (
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// decryptSecret расшифровывает строку в формате backend utils/crypto.rs:
// base64(nonce[24] || ciphertext) через XChaCha20-Poly1305.
// keyB64 — AGENT_SECRETS_KEY (base64 от 32 байт), тот же, что у backend.
func decryptSecret(enc, keyB64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(trimSpace(keyB64))
	if err != nil {
		return "", fmt.Errorf("AGENT_SECRETS_KEY must be base64: %w", err)
	}
	if len(key) != chacha20poly1305.KeySize { // 32
		return "", fmt.Errorf("AGENT_SECRETS_KEY must be 32 bytes")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}

	buf, err := base64.StdEncoding.DecodeString(trimSpace(enc))
	if err != nil {
		return "", fmt.Errorf("ciphertext not base64: %w", err)
	}
	ns := chacha20poly1305.NonceSizeX // 24
	if len(buf) < ns {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := buf[:ns], buf[ns:]
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed: %w", err)
	}
	return string(plain), nil
}

func trimSpace(s string) string {
	// локальный trim без импорта strings ради одной функции
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
