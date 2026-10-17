package api

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// CheckTargetSummary returns a bounded display value from unresolved public
// configuration. Credential-backed fields and URL paths are never displayed.
func CheckTargetSummary(driver DriverConfig) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(driver.Config, &fields) != nil {
		return ""
	}
	text := func(name string) string {
		if driver.CredentialRefs != nil && (*driver.CredentialRefs)[name] != "" {
			return ""
		}
		var value string
		if json.Unmarshal(fields[name], &value) != nil || len(value) > 1024 || strings.ContainsFunc(value, unicode.IsControl) {
			return ""
		}
		return value
	}
	switch driver.Type {
	case "http":
		u, err := url.Parse(text("url"))
		if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
			return ""
		}
		return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	case "tcp", "udp", "grpc", "tls":
		host := text("host")
		if host == "" || strings.ContainsAny(host, "/\\?#@ ") {
			return ""
		}
		var port int
		if driver.CredentialRefs == nil || (*driver.CredentialRefs)["port"] == "" {
			_ = json.Unmarshal(fields["port"], &port)
		}
		if port < 1 || port > 65535 {
			return host
		}
		return net.JoinHostPort(host, strconv.Itoa(port))
	case "dns", "icmp":
		return text("host")
	case "docker":
		return text("container")
	default:
		// Database connection strings can carry credentials in provider-specific
		// syntax. An unavailable summary is preferable to guessing at redaction.
		return ""
	}
}
