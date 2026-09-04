package email

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig holds configuration for the SMTP sender.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// SMTPSender sends emails via SMTP using the standard library net/smtp with context cancellation.
type SMTPSender struct {
	cfg    SMTPConfig
	logger *slog.Logger
}

// NewSMTPSender creates a configured SMTPSender.
func NewSMTPSender(cfg SMTPConfig, logger *slog.Logger) *SMTPSender {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Port <= 0 {
		cfg.Port = 1025
	}
	if cfg.From == "" {
		cfg.From = "no-reply@shopflow.io"
	}
	return &SMTPSender{
		cfg:    cfg,
		logger: logger,
	}
}

// Send dispatches an email message over SMTP.
func (s *SMTPSender) Send(ctx context.Context, msg EmailMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(msg.To) == 0 {
		return ErrInvalidRecipient
	}
	for _, recipient := range msg.To {
		if strings.TrimSpace(recipient) == "" {
			return ErrInvalidRecipient
		}
	}
	if strings.TrimSpace(msg.Subject) == "" {
		return ErrEmptySubject
	}
	if strings.TrimSpace(msg.TextBody) == "" && strings.TrimSpace(msg.HTMLBody) == "" {
		return ErrEmptyBody
	}

	from := msg.From
	if from == "" {
		from = s.cfg.From
	}

	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp dial %s failed: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client creation failed: %w", err)
	}
	defer client.Close()

	if s.cfg.Username != "" && s.cfg.Password != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM failed: %w", err)
	}
	for _, recipient := range msg.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("smtp RCPT TO failed for %s: %w", recipient, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA failed: %w", err)
	}

	rawMsg := buildMultipartAlternative(from, msg.To, msg.Subject, msg.TextBody, msg.HTMLBody)
	if _, err := w.Write(rawMsg); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write message failed: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data writer failed: %w", err)
	}

	return client.Quit()
}

func buildMultipartAlternative(from string, to []string, subject, textBody, htmlBody string) []byte {
	boundary := fmt.Sprintf("boundary-%d", time.Now().UnixNano())
	var b bytes.Buffer

	b.WriteString(fmt.Sprintf("From: %s\r\n", from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ", ")))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary))

	if textBody != "" {
		b.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		b.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		b.WriteString("Content-Transfer-Encoding: 7bit\r\n\r\n")
		b.WriteString(textBody)
		b.WriteString("\r\n\r\n")
	}

	if htmlBody != "" {
		b.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
		b.WriteString("Content-Transfer-Encoding: 7bit\r\n\r\n")
		b.WriteString(htmlBody)
		b.WriteString("\r\n\r\n")
	}

	b.WriteString(fmt.Sprintf("--%s--\r\n", boundary))
	return b.Bytes()
}
