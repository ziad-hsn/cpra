//go:build windows

package platform

import (
	"runtime"
)

func detectCapabilities() Capabilities {
	return Capabilities{
		GOOS:                    runtime.GOOS,
		IsWSL:                   false,
		InContainer:             false, // could be extended with job object/container detection
		SupportsCgroups:         false,
		SupportsJobObject:       true,
		DefaultDockerHostScheme: "npipe",
		CaseInsensitiveFS:       true,
	}
}
