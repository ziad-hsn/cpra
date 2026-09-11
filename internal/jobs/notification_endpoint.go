package jobs

import (
	"fmt"
	"net/url"
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
