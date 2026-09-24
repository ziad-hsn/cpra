//go:build !externaljobs

package runtimeconfig

// Normal builds have no custom-job configuration fields. Strict YAML decoding
// therefore rejects extension settings instead of silently ignoring them.
type processExtensions struct{}

func (Config) validateExtensions() error { return nil }
