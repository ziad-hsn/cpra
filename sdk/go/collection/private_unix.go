//go:build !windows

package collection

import "os"

func privateDirectory(parent string) (string, error) { return os.MkdirTemp(parent, "cpra-collection-") }
