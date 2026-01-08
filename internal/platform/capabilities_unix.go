//go:build linux || darwin

package platform

import (
	"os"
	"runtime"
	"strings"
)

func detectCapabilities() Capabilities {
	goos := runtime.GOOS

	isWSL := false
	if data, err := os.ReadFile("/proc/version"); err == nil {
		lower := strings.ToLower(string(data))
		if strings.Contains(lower, "microsoft") || strings.Contains(lower, "wsl") {
			isWSL = true
		}
	}

	inContainer := false
	if _, err := os.Stat("/.dockerenv"); err == nil {
		inContainer = true
	}

	supportsCgroups := false
	if _, err := os.Stat("/sys/fs/cgroup"); err == nil {
		supportsCgroups = true
	}

	return Capabilities{
		GOOS:                    goos,
		IsWSL:                   isWSL,
		InContainer:             inContainer,
		SupportsCgroups:         supportsCgroups,
		SupportsJobObject:       false,
		DefaultDockerHostScheme: "unix",
		CaseInsensitiveFS:       goos == "darwin",
	}
}
