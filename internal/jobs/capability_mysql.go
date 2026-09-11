//go:build mysql

package jobs

func init() { enabledDriverTags["mysql"] = true }
