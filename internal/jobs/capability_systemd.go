//go:build linux && systemd

package jobs

func init() { enabledDriverTags["systemd"] = true }
