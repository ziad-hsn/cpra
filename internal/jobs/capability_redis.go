//go:build redis

package jobs

func init() { enabledDriverTags["redis"] = true }
