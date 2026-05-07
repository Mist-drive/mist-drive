package httpx

import (
	"testing"

	"github.com/yann/mist-drive/api/internal/users"
)

func TestIsNewIP_EmptyHistory(t *testing.T) {
	if isNewIP("1.2.3.4", nil) {
		t.Fatal("empty history should return false (first login is not a new IP alert)")
	}
}

func TestIsNewIP_KnownIP(t *testing.T) {
	history := []users.LoginRecord{{IP: "1.2.3.4"}, {IP: "5.6.7.8"}}
	if isNewIP("1.2.3.4", history) {
		t.Fatal("known IP should return false")
	}
}

func TestIsNewIP_UnknownIP(t *testing.T) {
	history := []users.LoginRecord{{IP: "1.2.3.4"}}
	if !isNewIP("9.9.9.9", history) {
		t.Fatal("unknown IP should return true")
	}
}
