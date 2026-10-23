package management

import (
	"golang.org/x/sys/unix"
	"os"
)

func startupPublish(parent *os.File, _ *os.Root, fromName, toName string) error {
	return unix.RenameatxNp(int(parent.Fd()), fromName, int(parent.Fd()), toName, unix.RENAME_EXCL)
}
