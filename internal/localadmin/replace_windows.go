package localadmin

import "golang.org/x/sys/windows"

func replacePath(source, destination string) error {
	s, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	d, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(s, d, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows has no supported equivalent to Unix directory fsync through Go's
// directory handle. Files are flushed before same-volume MoveFileEx publication;
// the native crash tests define the supported boundary, not power-loss claims.
func syncDirectory(string) error { return nil }
