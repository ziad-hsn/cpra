//go:build postgres

package jobs

func init() { enabledDriverTags["postgres"] = true }
