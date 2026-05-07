package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/wneessen/go-mail"
	"github.com/yann/mist-drive/api/internal/config"
)

type Mailer struct{ cfg config.Config }

func New(cfg config.Config) *Mailer { return &Mailer{cfg: cfg} }

func (m *Mailer) Enabled() bool { return m.cfg.SMTPHost != "" }

func (m *Mailer) SendNewIP(to, ip, ua string, at time.Time) error {
	if !m.Enabled() {
		return nil
	}
	body := fmt.Sprintf(
		"A new sign-in to your Mist Drive account was detected.\n\nIP: %s\nClient: %s\nTime: %s\n\nIf this wasn't you, change your password and log out all active sessions from Settings.",
		ip, ua, at.Format(time.RFC1123),
	)
	if m.cfg.PublicURL != "" {
		body += "\n" + strings.TrimRight(m.cfg.PublicURL, "/") + "/settings"
	}
	return m.send(to, "New sign-in detected – Mist Drive", body)
}

func (m *Mailer) SendFailedLogin(to, targetLogin, ip, ua string, count int64, at time.Time) error {
	if !m.Enabled() {
		return nil
	}
	body := fmt.Sprintf(
		"%d failed sign-in attempt(s) on account '%s'.\n\nIP: %s\nClient: %s\nTime: %s",
		count, targetLogin, ip, ua, at.Format(time.RFC1123),
	)
	if m.cfg.PublicURL != "" {
		body += "\n\nReview active sessions:\n" + strings.TrimRight(m.cfg.PublicURL, "/") + "/settings"
	}
	return m.send(to, fmt.Sprintf("Failed login alert – Mist Drive (%s)", targetLogin), body)
}

func (m *Mailer) send(to, subject, body string) error {
	msg := mail.NewMsg()
	if err := msg.From(m.cfg.SMTPFrom); err != nil {
		return err
	}
	if err := msg.To(to); err != nil {
		return err
	}
	msg.Subject(subject)
	msg.SetBodyString(mail.TypeTextPlain, body)

	var tlsPolicy mail.TLSPolicy
	switch m.cfg.SMTPTLS {
	case "tls":
		tlsPolicy = mail.TLSMandatory
	case "none":
		tlsPolicy = mail.NoTLS
	default:
		tlsPolicy = mail.TLSOpportunistic
	}

	port := m.cfg.SMTPPort
	if port == 0 {
		port = 587
	}

	opts := []mail.Option{
		mail.WithPort(port),
		mail.WithTLSPolicy(tlsPolicy),
	}
	if m.cfg.SMTPUser != "" {
		opts = append(opts, mail.WithUsername(m.cfg.SMTPUser), mail.WithPassword(m.cfg.SMTPPassword))
	} else {
		opts = append(opts, mail.WithSMTPAuth(mail.SMTPAuthNoAuth))
	}
	client, err := mail.NewClient(m.cfg.SMTPHost, opts...)
	if err != nil {
		return err
	}
	return client.DialAndSend(msg)
}
