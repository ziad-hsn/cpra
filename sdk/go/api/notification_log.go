package api

import (
	"io/fs"
	"strings"
	"unicode"
)

// ValidateNotificationLogPath accepts portable relative slash-separated names.
// An omitted or empty name selects the process's standard output.
// Runtime os.Root and file-identity checks enforce confinement on the host.
func ValidateNotificationLogPath(name string) error {
	if name == "" {
		return nil
	}
	if name == "." || len(name) > 1024 || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return InvalidField("config.file", "logPath")
	}
	for _, part := range strings.Split(name, "/") {
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || base == "CON" || base == "NUL" || base == "PRN" || base == "AUX" ||
			len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9' {
			return InvalidField("config.file", "logPath")
		}
	}
	return nil
}
