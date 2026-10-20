//go:build !darwin && !windows

package persistence

import "os"

// Linux's POSIX access ACL mask is constrained by the group bits cleared by
// Fchmod. Default directory ACLs cannot override a child's protected mode.
func snapshotAccessACL(_ *os.File) error { return nil }
