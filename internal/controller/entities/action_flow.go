package entities

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

// actionDestination excludes payloads, colors, attempts, monitor identities and
// timeouts. It is used only by the process-local queue, never persisted or
// exposed through API/metrics. A conservative shared destination is safe;
// splitting one destination by a message setting defeats fairness and ordering.
func actionDestination(driver string, config any) (components.ActionFlow, error) {
	var parts []string
	switch c := config.(type) {
	case *manifest.InterventionTargetDocker:
		parts = []string{c.Container}
	case *manifest.InterventionTargetWebhook:
		parts = []string{destinationURL(c.URL)}
	case *manifest.InterventionTargetKubernetes:
		parts = []string{filepath.Clean(c.KubeconfigPath), c.Namespace, c.Kind, c.Name}
	case *manifest.InterventionTargetSystemd:
		parts = []string{c.Unit}
	case *manifest.InterventionTargetAWS:
		parts = []string{c.Region, c.InstanceID}
	case *manifest.InterventionTargetA2A:
		parts = []string{destinationURL(c.URL), c.Tenant}
	case *manifest.CodeNotificationA2A:
		parts = []string{destinationURL(c.URL), c.Tenant}
	case *manifest.CodeNotificationLog:
		parts = []string{filepath.Clean(c.File)}
	case *manifest.CodeNotificationEmail:
		parts = []string{destinationURL(c.Server), c.To}
	case *manifest.CodeNotificationWebhook:
		parts = []string{destinationURL(c.URL)}
	case *manifest.CodeNotificationPagerDuty:
		parts = []string{"", c.RoutingKey}
	case *manifest.CodeNotificationSlack:
		parts = []string{destinationURL(c.WebHook)}
	case *manifest.CodeNotificationTelegram:
		parts = []string{"", c.ChatID}
	case *manifest.CodeNotificationDiscord:
		parts = []string{destinationURL(c.WebhookURL)}
	case *manifest.CodeNotificationOpsgenie:
		parts = []string{"", c.APIKey}
	case *manifest.CodeNotificationTeams:
		parts = []string{destinationURL(c.WebhookURL)}
	case *manifest.CodeNotificationMattermost:
		parts = []string{destinationURL(c.WebhookURL), c.Channel}
	case *manifest.CodeNotificationPushover:
		parts = []string{"", c.UserKey}
	case *manifest.CodeNotificationTwilio:
		parts = []string{"", c.AccountSID, c.To}
	case *manifest.CodeNotificationDatadog:
		parts = []string{"", c.APIKey}
	case *manifest.CodeNotificationVictorOps:
		parts = []string{""}
	default:
		return components.ActionFlow{}, errors.New("action destination has no queue identity")
	}
	switch config.(type) {
	case *manifest.CodeNotificationPagerDuty, *manifest.CodeNotificationTelegram,
		*manifest.CodeNotificationOpsgenie, *manifest.CodeNotificationPushover,
		*manifest.CodeNotificationTwilio, *manifest.CodeNotificationDatadog,
		*manifest.CodeNotificationVictorOps:
		target, err := jobs.NotificationURL(config)
		if err != nil {
			return components.ActionFlow{}, errors.New("action destination is invalid")
		}
		parts[0] = destinationURL(target)
	}
	raw, err := json.Marshal(struct {
		Driver string
		Parts  []string
	}{driver, parts})
	if err != nil {
		return components.ActionFlow{}, errors.New("action destination cannot be encoded")
	}
	digest := sha256.Sum256(raw)
	clear(raw)
	return components.ActionFlow{Driver: driver, Key: binary.BigEndian.Uint64(digest[:8]), Digest: hex.EncodeToString(digest[:])}, nil
}

func destinationURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw // Static validation owns syntax; do not report target values.
	}
	u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	u.User, u.Fragment, u.RawFragment = nil, "", ""
	if u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80" {
		u.Host = strings.TrimSuffix(u.Host, ":"+u.Port())
	}
	if u.Host != "" && u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}
