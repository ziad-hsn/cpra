package jobs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func sendJSON(ctx context.Context, provider, method, target string, headers map[string]string, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return noEffect(err, false)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return noEffect(err, false)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := doEffect(effectHTTPClient(10*time.Second), req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return notificationResponseError(provider, resp)
}

func truncateNotification(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

func sendEmail(ctx context.Context, cfg manifest.CodeNotificationEmail, message string, identities ...notificationIdentity) error {
	if strings.ContainsAny(cfg.Subject, "\r\n") {
		return noEffect(fmt.Errorf("email subject contains a newline"), false)
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return noEffect(fmt.Errorf("invalid email sender"), false)
	}
	recipients, err := mail.ParseAddressList(cfg.To)
	if err != nil || len(recipients) == 0 {
		return noEffect(fmt.Errorf("invalid email recipients"), false)
	}
	host, _, err := net.SplitHostPort(cfg.Server)
	if err != nil {
		return noEffect(fmt.Errorf("SMTP server must include host and port"), false)
	}
	conn, err := dialBounded(ctx, "tcp", cfg.Server)
	if err != nil {
		return noEffect(err, transientBeforeEffect(err))
	}
	defer func() { _ = conn.Close() }()
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return smtpBeforeDataError(err)
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return smtpBeforeDataError(err)
		}
	} else if !cfg.AllowInsecure {
		return noEffect(fmt.Errorf("SMTP relay requires STARTTLS; allow_insecure is only for a trusted relay"), false)
	}
	if err := client.Mail(from.Address); err != nil {
		return smtpBeforeDataError(err)
	}
	to := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return smtpBeforeDataError(err)
		}
		to = append(to, recipient.String())
	}
	out, err := client.Data()
	if err != nil {
		return smtpBeforeDataError(err)
	}
	subject := cfg.Subject
	if subject == "" {
		subject = "CPRa monitoring alert"
	}
	at, delivery := time.Now().UTC(), ""
	if len(identities) == 1 {
		if !identities[0].At.IsZero() {
			at = identities[0].At.UTC()
		}
		delivery = identities[0].DeliveryID
	}
	if delivery == "" {
		delivery = at.Format(time.RFC3339Nano) + cfg.From + cfg.To + message
	}
	messageID := fmt.Sprintf("<%x@cpra.invalid>", sha256.Sum256([]byte(delivery)))
	text := "From: " + from.String() + "\r\nTo: " + strings.Join(to, ", ") + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) +
		"\r\nDate: " + at.Format(time.RFC1123Z) + "\r\nMessage-ID: " + messageID + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + message
	if _, err := out.Write([]byte(text)); err != nil {
		return err
	}
	// DATA completion is the relay's acceptance boundary; a subsequent QUIT
	// failure must not cause the accepted message to be resubmitted.
	err = out.Close()
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 400 && reply.Code < 600 {
		return noEffect(err, reply.Code < 500)
	}
	return err
}

func smtpBeforeDataError(err error) error {
	var reply *textproto.Error
	if errors.As(err, &reply) {
		return noEffect(err, reply.Code >= 400 && reply.Code < 500)
	}
	return noEffect(err, transientBeforeEffect(err))
}

// DeliveryError marks a confirmed rejection. Only Retryable rejections permit
// another controller-owned attempt. Unclassified transport failures and
// server/gateway errors never opt into automatic notification replay.
type DeliveryError struct {
	Status     int
	Retryable  bool
	RetryAfter time.Duration
}

func (e *DeliveryError) Error() string {
	return fmt.Sprintf("notification rejected (HTTP %d)", e.Status)
}
