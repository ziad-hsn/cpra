package drivers

import (
	"context"
)

// EmailDriver sends notifications via email (SMTP or email service API).
//
// TODO: Implement actual email sending using SMTP or email service API.
// The implementation should:
// - Support SMTP configuration (host, port, auth)
// - Support email service APIs (SendGrid, SES, etc.)
// - Handle HTML and plain text formats
type EmailDriver struct {
	SMTPHost    string
	SMTPPort    int
	FromAddress string
	ToAddresses []string
}

// Name returns the driver identifier.
func (d *EmailDriver) Name() string {
	return "email"
}

// Send delivers the notification via email.
// Currently a placeholder implementation that succeeds without action.
func (d *EmailDriver) Send(ctx context.Context, notification *Notification) error {
	// TODO: Implement SMTP or email API integration
	// Example implementation:
	// msg := fmt.Sprintf("Subject: %s Alert: %s\r\n\r\n%s",
	//     strings.ToUpper(notification.Color),
	//     notification.Monitor,
	//     notification.Message)
	// auth := smtp.PlainAuth("", d.Username, d.Password, d.SMTPHost)
	// return smtp.SendMail(
	//     fmt.Sprintf("%s:%d", d.SMTPHost, d.SMTPPort),
	//     auth,
	//     d.FromAddress,
	//     d.ToAddresses,
	//     []byte(msg))
	return nil
}
