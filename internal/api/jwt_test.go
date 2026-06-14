package api

import (
	"testing"
	"time"
)

func TestJWT_RoundTrip(t *testing.T) {
	now := time.Now()
	secret := "test-secret"
	tok, err := issueJWT(secret, jwtClaims{ConversationID: "c1", ProfileID: "p1", Scope: "sse"}, time.Hour, now)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := verifyJWT(secret, tok, now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.ConversationID != "c1" || claims.ProfileID != "p1" || claims.Scope != "sse" {
		t.Errorf("claims = %+v", claims)
	}
}

func TestJWT_BadSignature(t *testing.T) {
	now := time.Now()
	tok, _ := issueJWT("secret-a", jwtClaims{Scope: "x"}, time.Hour, now)
	if _, err := verifyJWT("secret-b", tok, now); err == nil {
		t.Fatal("expected signature mismatch error")
	}
}

func TestJWT_Expired(t *testing.T) {
	now := time.Now()
	tok, _ := issueJWT("s", jwtClaims{Scope: "x"}, time.Minute, now)
	later := now.Add(2 * time.Minute)
	if _, err := verifyJWT("s", tok, later); err == nil {
		t.Fatal("expected expired error")
	}
}

func TestJWT_Malformed(t *testing.T) {
	if _, err := verifyJWT("s", "not-a-jwt", time.Now()); err == nil {
		t.Fatal("expected error for malformed token")
	}
}
