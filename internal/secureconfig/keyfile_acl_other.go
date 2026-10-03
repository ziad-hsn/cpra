//go:build !linux && !windows && !darwin

package secureconfig

import (
	"fmt"
	"os"
)

func checkKeyAccessACL(_ *os.File, _ string) error {
	// Mode bits alone cannot rule out a named-user ACL on this platform.
	return fmt.Errorf("%w: native key ACL verification is not yet supported on this OS", ErrKeyFile)
}

func publishKeyFile(_ *os.Root, _, _ string) error {
	return fmt.Errorf("%w: native exclusive key publication is not yet supported on this OS", ErrKeyFile)
}
