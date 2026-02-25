//go:build windows

package limits

func detect() Limits {
	return Limits{
		Cores:       nil,
		MemoryBytes: nil,
		Source:      "jobobject (not detected)",
	}
}
