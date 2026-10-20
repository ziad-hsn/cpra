package api

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// ValidateTLSSettings checks transport and certificate policy without opening
// files, constructing clients or importing provider libraries. It is also used
// when the server prepares a built-in check from a file manifest.
func ValidateTLSSettings(driver DriverConfig) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(driver.Config, &fields) != nil {
		return InvalidField("config", "objectRequired")
	}
	referenced := func(key string) bool { return driver.CredentialRefs != nil && (*driver.CredentialRefs)[key] != "" }
	text := func(key string) string { var value string; _ = json.Unmarshal(fields[key], &value); return value }
	boolean := func(key string) bool { var value bool; _ = json.Unmarshal(fields[key], &value); return value }
	present := func(key string) bool { return referenced(key) || len(fields[key]) > 0 && string(fields[key]) != "null" }
	skip, ca, name := boolean("insecureSkipVerify"), text("caFile"), text("serverName")
	if skip && (ca != "" || referenced("caFile")) {
		return InvalidField("config.caFile", "skipVerificationCAConflict")
	}
	details := skip || ca != "" || name != "" || referenced("insecureSkipVerify") || referenced("caFile") || referenced("serverName")
	if strings.ContainsAny(ca, "\x00\r\n") || strings.ContainsAny(name, "\x00\r\n /\\?#@") {
		return InvalidField("config", "invalidTLSName")
	}
	switch driver.Type {
	case "http":
		if details && !referenced("url") {
			u, err := url.Parse(text("url"))
			if err != nil || u.Scheme != "https" {
				return InvalidField("config.url", "httpsRequired")
			}
		}
		return nil
	case "tls":
		return nil
	case "grpc":
		if details && text("mode") != "health" {
			return InvalidField("config.mode", "grpcHealthSettings")
		}
		if details && boolean("allowInsecure") {
			return InvalidField("config.allowInsecure", "cleartextTLSConflict")
		}
		return nil
	case "redis", "postgres", "mysql", "mongo", "rabbitmq", "kafka":
	default:
		if details || present("tls") || present("insecureSkipVerify") {
			return InvalidField("config", "noTLSSettings")
		}
		return nil
	}
	typed := present("tls") || present("insecureSkipVerify") || details
	if !typed {
		return nil
	}
	if !present("tls") {
		return InvalidField("config.tls", "explicitTLS")
	}
	if referenced("tls") {
		return nil
	}
	enabled := boolean("tls")
	if driver.Type == "redis" && enabled && strings.HasPrefix(text("addr"), "/") {
		return InvalidField("config.addr", "redisTLSTCP")
	}
	if driver.Type == "postgres" && enabled && !referenced("dsn") {
		host, field := text("host"), "host"
		if text("dsn") != "" {
			var err error
			host, err = postgresTargetHost(text("dsn"))
			if err != nil {
				return err
			}
			field = "dsn"
		}
		if !referenced(field) {
			for _, candidate := range strings.Split(host, ",") {
				if strings.HasPrefix(candidate, "/") {
					return InvalidField("config."+field, "postgresTLSTCP")
				}
			}
		}
	}
	if driver.Type == "mysql" && enabled && !referenced("dsn") && text("dsn") != "" {
		network, err := mysqlNetwork(text("dsn"))
		if err != nil {
			return err
		}
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return InvalidField("config.dsn", "mysqlTLSTCP")
		}
	}
	if !enabled && details {
		return InvalidField("config.tls", "cleartextTLSConflict")
	}
	if driver.Type == "postgres" && text("sslmode") != "" && !referenced("sslmode") {
		if !postgresTLSModeMatches(text("sslmode"), enabled, skip) {
			return InvalidField("config.sslmode", "tlsPolicyConflict")
		}
	}
	connectionKey := map[string]string{"postgres": "dsn", "mysql": "dsn", "mongo": "uri", "rabbitmq": "url"}[driver.Type]
	if connectionKey == "" || referenced(connectionKey) || text(connectionKey) == "" {
		return nil
	}
	connection := text(connectionKey)
	if driver.Type == "rabbitmq" {
		u, err := url.Parse(connection)
		if err != nil || (u.Scheme == "amqps") != enabled {
			return InvalidField("config.url", "tlsPolicyConflict")
		}
	}
	settings, err := connectionTLSSettings(driver.Type, connection)
	if err != nil {
		return err
	}
	for key, value := range settings {
		switch {
		case driver.Type == "mysql" && key == "allowfallbacktoplaintext":
			fallback, err := strconv.ParseBool(value)
			if err != nil || enabled && fallback {
				return InvalidField("config.dsn", "tlsPolicyConflict")
			}
		case driver.Type == "postgres" && key == "sslmode":
			if !postgresTLSModeMatches(value, enabled, skip) {
				return InvalidField("config.dsn", "tlsPolicyConflict")
			}
		case driver.Type == "mysql" && key == "tls":
			normalized := value
			if v, err := strconv.ParseBool(value); err == nil {
				normalized = strconv.FormatBool(v)
			}
			match := !enabled && normalized == "false" || enabled && !skip && normalized == "true" || enabled && skip && normalized == "skip-verify"
			if !match {
				return InvalidField("config.dsn", "tlsPolicyConflict")
			}
		case driver.Type == "mongo" && (key == "tls" || key == "ssl"):
			v, err := strconv.ParseBool(value)
			if err != nil || v != enabled {
				return InvalidField("config.uri", "tlsPolicyConflict")
			}
		default:
			return InvalidField("config", "tlsPolicyConflict")
		}
	}
	return nil
}

func postgresTLSModeMatches(mode string, enabled, skip bool) bool {
	return !enabled && mode == "disable" || enabled && !skip && mode == "verify-full" || enabled && skip && mode == "require"
}

func connectionTLSSettings(driver, connection string) (map[string]string, error) {
	all := make(map[string]string)
	if driver == "postgres" && !strings.HasPrefix(connection, "postgres://") && !strings.HasPrefix(connection, "postgresql://") {
		var err error
		all, err = postgresConnectionFields(connection)
		if err != nil {
			return nil, err
		}
	} else {
		query := ""
		if driver == "mysql" {
			_, query, _ = strings.Cut(connection[strings.LastIndex(connection, "/")+1:], "?")
		} else {
			// Parse only the query; Mongo permits comma-separated host lists that
			// are outside net/url's host grammar.
			_, query, _ = strings.Cut(connection, "?")
		}
		for _, pair := range strings.Split(query, "&") {
			key, raw, ok := strings.Cut(pair, "=")
			if !ok {
				continue
			}
			key, err := url.QueryUnescape(key)
			if err != nil {
				return nil, InvalidField("config", "connectionSyntax")
			}
			value, err := url.QueryUnescape(raw)
			if err != nil {
				return nil, InvalidField("config", "connectionSyntax")
			}
			if err := addConnectionField(all, key, value); err != nil {
				return nil, err
			}
		}
	}
	out := make(map[string]string)
	for key, value := range all {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "ssl") || strings.HasPrefix(lower, "tls") || driver == "mysql" && lower == "allowfallbacktoplaintext" || driver == "postgres" && (lower == "service" || lower == "servicefile") || driver == "rabbitmq" && (lower == "cacertfile" || lower == "certfile" || lower == "keyfile" || lower == "server_name_indication") {
			out[lower] = value
		}
	}
	return out, nil
}

func addConnectionField(fields map[string]string, key, value string) error {
	key = strings.ToLower(key)
	if previous, exists := fields[key]; exists && previous != value &&
		(strings.HasPrefix(key, "tls") || strings.HasPrefix(key, "ssl") || key == "allowfallbacktoplaintext" || key == "service" || key == "servicefile" || key == "cacertfile" || key == "certfile" || key == "keyfile" || key == "server_name_indication") {
		return InvalidField("config", "connectionSyntax")
	}
	fields[key] = value
	return nil
}

// PostgreSQL keyword DSNs use whitespace-separated name=value pairs with
// single-quoted values and backslash escapes. Values are never included in errors.
func postgresConnectionFields(input string) (map[string]string, error) {
	fields := make(map[string]string)
	space := func(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\v' || b == '\f' }
	for i := 0; i < len(input); {
		for i < len(input) && space(input[i]) {
			i++
		}
		if i == len(input) {
			break
		}
		start := i
		for i < len(input) && !space(input[i]) && input[i] != '=' {
			i++
		}
		key := input[start:i]
		for i < len(input) && space(input[i]) {
			i++
		}
		if key == "" || i == len(input) || input[i] != '=' {
			return nil, InvalidField("config", "connectionSyntax")
		}
		i++
		for i < len(input) && space(input[i]) {
			i++
		}
		quoted := i < len(input) && input[i] == '\''
		if quoted {
			i++
		}
		var value strings.Builder
		closed := !quoted
		for i < len(input) {
			b := input[i]
			if quoted && b == '\'' {
				i++
				closed = true
				break
			}
			if !quoted && space(b) {
				break
			}
			if b == '\\' {
				i++
				if i == len(input) {
					return nil, InvalidField("config", "connectionSyntax")
				}
				b = input[i]
			}
			value.WriteByte(b)
			i++
		}
		if !closed || i < len(input) && !space(input[i]) {
			return nil, InvalidField("config", "connectionSyntax")
		}
		if err := addConnectionField(fields, key, value.String()); err != nil {
			return nil, err
		}
	}
	return fields, nil
}
