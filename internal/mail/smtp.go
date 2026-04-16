package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPConfig holds connection + auth + envelope info for outbound mail.
type SMTPConfig struct {
	Host      string
	Port      int
	Username  string
	Password  string
	From      string // e.g. "AgentHub <noreply@agenthub.app>"
	UseTLS    bool   // implicit TLS (port 465); false for STARTTLS-or-plain
	TLSConfig *tls.Config
}

type smtpMailer struct{ cfg SMTPConfig }

// NewSMTP returns a Mailer that dials cfg.Host:cfg.Port per Send.
// Connection is NOT pooled — Plan 02's auth volume doesn't require it;
// pooling can be added later without changing the interface.
func NewSMTP(cfg SMTPConfig) Mailer { return &smtpMailer{cfg: cfg} }

func (m *smtpMailer) Send(_ context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", m.cfg.Host, m.cfg.Port)

	var auth smtp.Auth
	if m.cfg.Username != "" {
		auth = smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)
	}

	body := buildRFC5322(m.cfg.From, msg)

	if err := smtp.SendMail(addr, auth, m.cfg.From, []string{msg.To}, body); err != nil {
		return fmt.Errorf("smtp.SendMail: %w", err)
	}
	return nil
}

func buildRFC5322(from string, msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	b.WriteString("Subject: " + msg.Subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(msg.Text)
	return []byte(b.String())
}
