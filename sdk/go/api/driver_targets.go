package api

import (
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Target syntax is shared by all input paths. These checks neither resolve
// names nor instantiate a provider client; credentials are checked after the
// server resolves their references using the same validator.
func validateDriverTargets(category string, driver DriverConfig, fields map[string]json.RawMessage) error {
	referenced := func(key string) bool { return driver.CredentialRefs != nil && (*driver.CredentialRefs)[key] != "" }
	text := func(key string) string { var value string; _ = json.Unmarshal(fields[key], &value); return value }
	check := func(key, rule string, valid func(string) bool, optional bool) error {
		if referenced(key) || optional && text(key) == "" || valid(text(key)) {
			return nil
		}
		return InvalidField("config."+key, rule)
	}
	key := category + "/" + driver.Type
	switch key {
	case "check/tcp", "check/udp", "check/grpc", "check/tls", "check/dns", "check/icmp":
		return check("host", "networkHost", validNetworkHost, false)
	case "check/docker", "recovery/docker":
		return check("container", "namedTarget", validNamedTarget, false)
	case "check/redis":
		return check("addr", "redisAddress", func(value string) bool {
			return strings.HasPrefix(value, "/") && validNamedTarget(value) || validServerAddress(value, false)
		}, true)
	case "check/postgres", "check/mysql":
		if !referenced("port") {
			var port int
			_ = json.Unmarshal(fields["port"], &port)
			if port < 0 || port > 65535 {
				return InvalidField("config.port", "optionalPortRange")
			}
		}
		if driver.Type == "postgres" {
			if err := check("sslmode", "postgresSSLMode", func(mode string) bool {
				return slices.Contains([]string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}, mode)
			}, true); err != nil {
				return err
			}
			if !referenced("dsn") && text("dsn") != "" {
				if _, err := postgresTargetHost(text("dsn")); err != nil {
					return err
				}
			}
		} else if !referenced("dsn") && text("dsn") != "" {
			if _, err := mysqlNetwork(text("dsn")); err != nil {
				return err
			}
		}
	case "check/mongo":
		return check("uri", "mongoURI", validMongoURI, false)
	case "check/rabbitmq":
		return check("url", "amqpURL", func(value string) bool {
			u, err := url.Parse(value)
			return err == nil && (u.Scheme == "amqp" || u.Scheme == "amqps") && u.Opaque == "" && u.Fragment == "" &&
				!strings.ContainsAny(value, " \t\r\n\x00") && validURLPort(u)
		}, false)
	case "check/kafka":
		if !referenced("brokers") {
			var brokers []string
			_ = json.Unmarshal(fields["brokers"], &brokers)
			for _, broker := range brokers {
				if !validServerAddress(broker, true) {
					return InvalidField("config.brokers", "dnsServer")
				}
			}
		}
	case "recovery/kubernetes":
		for _, field := range []string{"name", "namespace"} {
			if err := check(field, "namedTarget", validNamedTarget, false); err != nil {
				return err
			}
		}
	case "recovery/systemd":
		return check("unit", "systemdUnit", func(value string) bool {
			return validNamedTarget(value) && !strings.ContainsAny(value, "/\\")
		}, false)
	case "recovery/aws":
		return check("instanceId", "namedTarget", validNamedTarget, false)
	case "notification/datadog":
		return check("site", "networkHost", validNetworkHost, true)
	case "notification/victorops":
		return check("messageType", "victoropsMessageType", func(value string) bool {
			return slices.Contains([]string{"CRITICAL", "WARNING", "ACKNOWLEDGEMENT", "INFO", "RECOVERY"}, value)
		}, true)
	case "recovery/a2a", "notification/a2a":
		return validateA2ASettings(category, driver, fields)
	}
	return nil
}

func validNamedTarget(value string) bool {
	return value != "" && !strings.ContainsAny(value, "\x00\r\n")
}

func validNetworkHost(value string) bool {
	return validNamedTarget(value) && !strings.ContainsAny(value, " /\\\t?#@")
}

func validURLPort(u *url.URL) bool {
	if u.Port() == "" {
		return true
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535
}

func validServerAddress(value string, optionalPort bool) bool {
	if !validNetworkHost(value) {
		return false
	}
	host, port, err := net.SplitHostPort(value)
	if err == nil {
		n, err := strconv.Atoi(port)
		return err == nil && validNetworkHost(host) && n > 0 && n <= 65535
	}
	if !optionalPort {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		ip, err := netip.ParseAddr(value[1 : len(value)-1])
		return err == nil && ip.Is6()
	}
	return !strings.ContainsAny(value, ":[]")
}

func validMongoURI(value string) bool {
	rest, ok := strings.CutPrefix(value, "mongodb://")
	if !ok || strings.ContainsAny(rest, "\x00\r\n#") {
		return false
	}
	authority, _, _ := strings.Cut(rest, "/")
	if i := strings.LastIndex(authority, "@"); i >= 0 {
		authority = authority[i+1:]
	}
	for _, host := range strings.Split(authority, ",") {
		if !validServerAddress(host, true) {
			return false
		}
	}
	return true
}

// PostgreSQL gives the query's host setting precedence over the URI authority.
// An empty host retains the provider's defaults; only explicit transports can
// be checked without reading environment variables or service files.
func postgresTargetHost(connection string) (string, error) {
	invalid := func() (string, error) { return "", InvalidField("config.dsn", "connectionSyntax") }
	if strings.ContainsRune(connection, '\x00') {
		return invalid()
	}
	if strings.HasPrefix(connection, "postgres://") || strings.HasPrefix(connection, "postgresql://") {
		u, err := url.Parse(connection)
		if err != nil || u.Fragment != "" || u.Opaque != "" {
			return invalid()
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return invalid()
		}
		if hosts, ok := query["host"]; ok && len(hosts) > 0 {
			return hosts[0], nil
		}
		return u.Hostname(), nil
	}
	fields, err := postgresConnectionFields(connection)
	if err != nil {
		return invalid()
	}
	return fields["host"], nil
}

// Match the pinned MySQL driver's network boundary, not its credential grammar.
// Passwords and Unix addresses may contain slashes; the last slash separates
// the database name. Runtime construction still uses mysql.ParseDSN.
func mysqlNetwork(dsn string) (string, error) {
	end := strings.LastIndex(dsn, "/")
	if end < 0 || strings.ContainsRune(dsn, '\x00') {
		return "", InvalidField("config.dsn", "connectionSyntax")
	}
	part := dsn[:end]
	part = part[strings.LastIndex(part, "@")+1:]
	if i := strings.IndexByte(part, '('); i >= 0 {
		if !strings.HasSuffix(part, ")") {
			return "", InvalidField("config.dsn", "connectionSyntax")
		}
		part = part[:i]
	}
	if part == "" {
		part = "tcp"
	}
	return part, nil
}

func validateA2ASettings(category string, driver DriverConfig, fields map[string]json.RawMessage) error {
	referenced := func(key string) bool { return driver.CredentialRefs != nil && (*driver.CredentialRefs)[key] != "" }
	text := func(key string) string { var value string; _ = json.Unmarshal(fields[key], &value); return value }
	if !referenced("url") {
		value := text("url")
		u, err := url.Parse(value)
		var insecure bool
		_ = json.Unmarshal(fields["allowInsecureHttp"], &insecure)
		if len(value) > 4096 || !utf8.ValidString(value) || err != nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !validURLPort(u) ||
			u.Scheme != "https" && !(u.Scheme == "http" && insecure) {
			return InvalidField("config.url", "a2aURL")
		}
	}
	if !referenced("tenant") && (len(text("tenant")) > 256 || !utf8.ValidString(text("tenant")) || strings.ContainsFunc(text("tenant"), unicode.IsControl)) {
		return InvalidField("config.tenant", "a2aTenant")
	}
	if !referenced("token") && (len(text("token")) > 8192 || strings.ContainsFunc(text("token"), func(r rune) bool { return r < 0x21 || r > 0x7e })) {
		return InvalidField("config.token", "a2aToken")
	}
	if request, err := driverDuration(fields, "requestTimeout"); err != nil || request < 0 || request > 5*time.Minute {
		return InvalidField("config.requestTimeout", "a2aRequestTimeout")
	}
	key := "taskTimeout"
	if category == "notification" {
		key = "timeout"
	}
	task, err := driverDuration(fields, key)
	if task == 0 {
		task = time.Minute
	}
	if err != nil || task < time.Second || task > 5*time.Minute {
		return InvalidField("config."+key, "a2aTaskTimeout")
	}
	poll, err := driverDuration(fields, "pollInterval")
	if poll == 0 {
		poll = min(5*time.Second, task)
	}
	if err != nil || poll < time.Second || poll > 30*time.Second || poll > task {
		return InvalidField("config.pollInterval", "a2aPollInterval")
	}
	return nil
}
