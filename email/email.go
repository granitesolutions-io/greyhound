package email

import (
	"fmt"
	"net/smtp"
	"strings"
)

// Sender sends emails via SMTP.
type Sender interface {
	Send(to, subject, body string) error
	IsConfigured() bool
}

// Config holds SMTP configuration.
type Config struct {
	Host     string // SMTP host (e.g. smtp-relay.brevo.com)
	Port     int    // SMTP port (e.g. 587)
	Username string // SMTP username
	Password string // SMTP password
	From     string // From address (e.g. noreply@granitesolutions.io)
	FromName string // From display name (e.g. GraniteSolutions)
}

// New creates a Sender from the given config. Returns a no-op sender if
// credentials are not configured.
func New(cfg Config) Sender {
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" {
		return &noopSender{}
	}
	return &smtpSender{cfg: cfg}
}

// smtpSender sends emails via SMTP relay.
type smtpSender struct {
	cfg Config
}

func (s *smtpSender) Send(to, subject, body string) error {
	from := s.cfg.From
	if s.cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", s.cfg.FromName, s.cfg.From)
	}

	headers := []string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
	}

	msg := strings.Join(headers, "\r\n") + "\r\n\r\n" + body

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)

	return smtp.SendMail(addr, auth, s.cfg.From, []string{to}, []byte(msg))
}

func (s *smtpSender) IsConfigured() bool {
	return true
}

// noopSender is a no-op sender used when SMTP is not configured.
type noopSender struct{}

func (n *noopSender) Send(to, subject, body string) error {
	return fmt.Errorf("email not configured")
}

func (n *noopSender) IsConfigured() bool {
	return false
}
