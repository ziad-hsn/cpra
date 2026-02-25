//go:build darwin

package limits

import "runtime"

func detect() Limits {
	cores := runtime.NumCPU()
	return Limits{
		Cores:       &cores,
		MemoryBytes: nil,
		Source:      "sysctl/rlimit (info only)",
	}
}
