package notify_test

import (
	"testing"
	"time"

	"github.com/yann/mist-drive/api/internal/config"
	"github.com/yann/mist-drive/api/internal/notify"
)

func TestMailer_DisabledWhenNoHost(t *testing.T) {
	m := notify.New(config.Config{SMTPHost: ""})
	if m.Enabled() {
		t.Fatal("expected Enabled()=false when SMTPHost is empty")
	}
}

func TestMailer_EnabledWhenHostSet(t *testing.T) {
	m := notify.New(config.Config{SMTPHost: "smtp.example.com"})
	if !m.Enabled() {
		t.Fatal("expected Enabled()=true when SMTPHost is set")
	}
}

func TestMailer_SendNewIP_NoopWhenDisabled(t *testing.T) {
	m := notify.New(config.Config{})
	if err := m.SendNewIP("to@example.com", "1.2.3.4", "Mozilla", time.Now()); err != nil {
		t.Fatalf("disabled mailer should return nil, got: %v", err)
	}
}

func TestMailer_SendFailedLogin_NoopWhenDisabled(t *testing.T) {
	m := notify.New(config.Config{})
	if err := m.SendFailedLogin("admin@example.com", "alice", "1.2.3.4", "curl", 3, time.Now()); err != nil {
		t.Fatalf("disabled mailer should return nil, got: %v", err)
	}
}
