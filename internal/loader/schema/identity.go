package schema

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
)

var monitorIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

// EffectiveID survives label changes when the manifest supplies an explicit ID.
func (m Monitor) EffectiveID() (string, error) {
	if m.ID != "" {
		if !monitorIDPattern.MatchString(m.ID) {
			return "", fmt.Errorf("monitor id must be 1..128 letters, digits, dot, underscore, colon or hyphen")
		}
		return m.ID, nil
	}
	if m.Name == "" {
		return "", fmt.Errorf("monitor name is required")
	}
	sum := sha256.Sum256([]byte(m.Name))
	return "name:" + hex.EncodeToString(sum[:]), nil
}

// ConfigurationRevision hashes credentials but never returns or persists them.
// Only referenced groups enter the hash; changing an unrelated endpoint does
// not cancel this monitor's pending work. A label rename preserves its revision.
func ConfigurationRevision(m Monitor, endpoints map[string]Endpoint, groups NotificationGroups) (string, error) {
	m.ID, m.Name = "", ""
	used := make(map[string][]Endpoint)
	for _, code := range m.Codes {
		if code.NotifyGroup == "" {
			continue
		}
		for _, name := range groups[code.NotifyGroup] {
			used[code.NotifyGroup] = append(used[code.NotifyGroup], endpoints[name])
		}
	}
	data, err := json.Marshal(struct {
		Monitor Monitor
		Groups  map[string][]Endpoint
	}{m, used})
	if err != nil {
		return "", fmt.Errorf("cannot fingerprint monitor configuration: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
