package platform

// Capabilities describes runtime platform capabilities detected at startup.
type Capabilities struct {
	GOOS                    string
	IsWSL                   bool
	InContainer             bool
	SupportsCgroups         bool
	SupportsJobObject       bool
	DefaultDockerHostScheme string
	CaseInsensitiveFS       bool
}

// Detect returns platform capabilities using OS-specific implementations.
func Detect() Capabilities {
	return detectCapabilities()
}
