package jobs

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"cpra/internal/loader/schema"
)

func sendJSON(ctx context.Context, method, target string, headers map[string]string, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := effectHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return notificationStatusError(resp.StatusCode)
}

func notificationStatusError(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	return &DeliveryError{Status: status, Retryable: status == 429 || status >= 500}
}

func truncateNotification(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func sendEmail(ctx context.Context, cfg schema.CodeNotificationEmail, message string) error {
	if strings.ContainsAny(cfg.Subject, "\r\n") {
		return fmt.Errorf("email subject contains a newline")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("invalid email sender")
	}
	recipients, err := mail.ParseAddressList(cfg.To)
	if err != nil || len(recipients) == 0 {
		return fmt.Errorf("invalid email recipients")
	}
	host, _, err := net.SplitHostPort(cfg.Server)
	if err != nil {
		return fmt.Errorf("SMTP server must include host and port")
	}
	conn, err := dialBounded(ctx, "tcp", cfg.Server)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	} else if !cfg.AllowInsecure {
		return fmt.Errorf("SMTP relay requires STARTTLS; allow_insecure is only for a trusted relay")
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return err
		}
		to = append(to, recipient.String())
	}
	out, err := client.Data()
	if err != nil {
		return err
	}
	subject := cfg.Subject
	if subject == "" {
		subject = "CPRa monitoring alert"
	}
	text := "From: " + from.String() + "\r\nTo: " + strings.Join(to, ", ") + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + message
	if _, err := out.Write([]byte(text)); err != nil {
		return err
	}
	// DATA completion is the relay's acceptance boundary; a subsequent QUIT
	// failure must not cause the accepted message to be resubmitted.
	return out.Close()
}

// DeliveryError marks a definite rejection that is safe to retry. Transport
// errors and ambiguous timeouts do not opt into automatic notification replay.
type DeliveryError struct {
	Status    int
	Retryable bool
}

func (e *DeliveryError) Error() string { return fmt.Sprintf("notification HTTP %d", e.Status) }
