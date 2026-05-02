package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yann/mist-drive/api/internal/auth"
)

const testSecret = "test-secret-32bytesxxxxxxxxxx!!"

func TestAuth_IssueAndParse(t *testing.T) {
	tok, err := auth.Issue(testSecret, "uid1", "admin", time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := auth.Parse(testSecret, tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if claims.UID != "uid1" {
		t.Fatalf("UID: got %q want %q", claims.UID, "uid1")
	}
	if claims.Role != "admin" {
		t.Fatalf("Role: got %q want %q", claims.Role, "admin")
	}
}

func TestAuth_ParseWrongSecret(t *testing.T) {
	tok, err := auth.Issue(testSecret, "uid1", "user", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Parse("different-secret-32bytesxxxxxxxx", tok)
	if err == nil {
		t.Fatal("expected error with wrong secret, got nil")
	}
}

func TestAuth_ParseExpired(t *testing.T) {
	tok, err := auth.Issue(testSecret, "uid1", "user", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = auth.Parse(testSecret, tok)
	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}
}

func TestAuth_ParseTamperedSignature(t *testing.T) {
	tok, err := auth.Issue(testSecret, "uid1", "user", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the last character of the signature (last segment of the JWT).
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT format: %d parts", len(parts))
	}
	sig := []byte(parts[2])
	sig[len(sig)-1] ^= 0xff
	parts[2] = string(sig)
	tampered := strings.Join(parts, ".")

	_, err = auth.Parse(testSecret, tampered)
	if err == nil {
		t.Fatal("expected error for tampered signature, got nil")
	}
}

func TestAuth_VerifyPassword(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !auth.VerifyPassword(hash, "correct-horse") {
		t.Fatal("VerifyPassword returned false for correct password")
	}
	if auth.VerifyPassword(hash, "wrong-horse") {
		t.Fatal("VerifyPassword returned true for wrong password")
	}
	if auth.VerifyPassword(hash, "") {
		t.Fatal("VerifyPassword returned true for empty password")
	}
}
