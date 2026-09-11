//go:build rabbitmq

package jobs

func init() { enabledDriverTags["rabbitmq"] = true }
