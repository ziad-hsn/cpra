//go:build kubernetes

package jobs

func init() { enabledDriverTags["kubernetes"] = true }
