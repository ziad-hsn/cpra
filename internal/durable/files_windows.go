package durable

import (
	"fmt"
	"golang.org/x/sys/windows"
)

// The caller has synced and closed a temporary file in the destination's
// directory. MoveFileEx publishes it with write-through; COPY_ALLOWED is
// deliberately absent, so replacement cannot degrade to a cross-volume copy.
// Windows cannot fsync a directory opened with os.Open. Keep file sync errors
// fatal and use the native rename contract instead of ignoring Access Denied.
// Temporary files inherit the protected data-directory DACL established by the
// installer, so initial creation and replacement have the same access policy.
func replaceDurableFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("replace durable metadata: %w", err)
	}
	return nil
}
