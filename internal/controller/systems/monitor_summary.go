package systems

import (
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"net/url"
	"strconv"
)

// classifyStatus derives a dashboard status string from monitor state flags.
func classifyStatus(state *components.MonitorState) string {
	if state.Flags&components.StateVerifying != 0 {
		return "verifying"
	}
	if state.Flags&components.StateIncidentOpen != 0 {
		return "incident"
	}
	if state.PulseFailures > 0 || state.LastError != nil {
		return "down"
	}
	if state.LastSuccessTime.IsZero() {
		return "unknown"
	}
	if state.PulseWarning != "" {
		return "degraded"
	}
	return "up"
}

// pulseTarget returns a human-readable endpoint for display, derived from the
// monitor's pulse config. Returns an empty string when unknown.
func pulseTarget(cfg *components.PulseConfig) string {
	if cfg == nil || cfg.Config == nil {
		return ""
	}
	switch c := cfg.Config.(type) {
	case *manifest.PulseHTTPConfig:
		return redactURL(c.Url)
	case *manifest.PulseTCPConfig:
		return joinHostPort(c.Host, c.Port)
	case *manifest.PulseICMPConfig:
		return c.Host
	case *manifest.PulseDNSConfig:
		return c.Host
	case *manifest.PulseUDPConfig:
		return joinHostPort(c.Host, c.Port)
	case *manifest.PulseGRPCConfig:
		return joinHostPort(c.Host, c.Port)
	case *manifest.PulseDockerConfig:
		return c.Container
	default:
		return ""
	}
}

// redactURL strips userinfo, query, and fragment from a URL so embedded
// credentials (e.g. http://user:pass@host/) are not leaked through the
// read-only API.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// joinHostPort joins a host and port for display; returns host alone when port is 0.
func joinHostPort(host string, port int) string {
	if host == "" {
		return ""
	}
	if port == 0 {
		return host
	}
	return host + ":" + strconv.Itoa(port)
}
