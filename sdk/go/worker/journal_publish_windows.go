//go:build externaljobs && windows

package worker

import "golang.org/x/sys/windows"

func publishJournalBackup(temporary, path string) error {
	from, err := windows.UTF16PtrFromString(temporary)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// No REPLACE_EXISTING: an original backup is never overwritten. The backup
	// is on the same volume and its contents have already been FlushFileBuffers'd
	// through os.File.Sync. Windows does not support Unix directory fsync.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}
