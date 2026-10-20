package jobs

import (
	"fmt"
	"net/url"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

// notificationURL uses the complete configured operation URL, retaining the
// same validation and transport policy as the production destination.
func notificationURL(configured, fallback string) (string, error) {
	target := configured
	if target == "" {
		target = fallback
	}
	if err := validateTargetURL(target); err != nil {
		return "", err
	}
	return target, nil
}

func pagerDutyURL(configured string) (string, error) {
	return notificationURL(configured, "https://events.pagerduty.com/v2/enqueue")
}

func opsgenieURL(configured string) (string, error) {
	return notificationURL(configured, "https://api.opsgenie.com/v2/alerts")
}

func pushoverURL(configured string) (string, error) {
	return notificationURL(configured, "https://api.pushover.net/1/messages.json")
}

func twilioURL(configured, account string) (string, error) {
	return notificationURL(configured, "https://api.twilio.com/2010-04-01/Accounts/"+url.PathEscape(account)+"/Messages.json")
}

func datadogURL(configured, site string) (string, error) {
	if site == "" {
		site = "datadoghq.com"
	}
	return notificationURL(configured, "https://api."+site+"/api/v1/events")
}

// NotificationURL resolves optional operation URLs using the same rules as
// execution. It performs no I/O. Its result can contain provider credentials
// and must not be returned through observations, errors or metric labels.
func NotificationURL(config any) (string, error) {
	switch c := config.(type) {
	case *manifest.CodeNotificationPagerDuty:
		return pagerDutyURL(c.URL)
	case *manifest.CodeNotificationOpsgenie:
		return opsgenieURL(c.URL)
	case *manifest.CodeNotificationPushover:
		return pushoverURL(c.URL)
	case *manifest.CodeNotificationTelegram:
		return telegramURL(c.URL, c.BotToken, c.TestMode)
	case *manifest.CodeNotificationTwilio:
		return twilioURL(c.URL, c.AccountSID)
	case *manifest.CodeNotificationDatadog:
		return datadogURL(c.URL, c.Site)
	case *manifest.CodeNotificationVictorOps:
		return victorOpsURL(c.URL, c.RestEndpointKey, c.RoutingKey)
	default:
		return "", fmt.Errorf("notification has no optional operation URL")
	}
}

func telegramURL(configured, token string, testMode bool) (string, error) {
	if configured != "" && testMode {
		return "", fmt.Errorf("telegram url and test_mode are mutually exclusive")
	}
	path := "/sendMessage"
	if testMode {
		path = "/test/sendMessage"
	}
	return notificationURL(configured, "https://api.telegram.org/bot"+url.PathEscape(token)+path)
}

func victorOpsURL(configured, key, routing string) (string, error) {
	return notificationURL(configured, "https://alert.victorops.com/integrations/generic/20131114/alert/"+url.PathEscape(key)+"/"+url.PathEscape(routing))
}
