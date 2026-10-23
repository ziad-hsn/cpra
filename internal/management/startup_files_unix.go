//go:build linux || darwin

package management

import (
	"os"

	"golang.org/x/sys/unix"
)

func startupOpenDirectory(root *os.Root) (*os.File, error) {
	return root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

func startupOpenRegular(root *os.Root, name string, _ bool) (*os.File, error) {
	// Read-only descriptors support fsync on these platforms. Preserve the
	// existing required permissions for intent revalidation.
	return root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func startupDirectoryCheckpoint(directory *os.File) error {
	return directory.Sync()
}

func startupRemove(root *os.Root, _ *os.File, name string, _ startupIdentity, _ bool) error {
	return root.Remove(name)
}
