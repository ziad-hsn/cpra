//go:build !linux && !darwin && !windows

package platform

import "runtime"

func detectCapabilities() Capabilities {
	return Capabilities{
		GOOS:                    runtime.GOOS,
		DefaultDockerHostScheme: "",
	}
}
