//go:build linux || darwin

package persistence

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func collectionGCOpenDirectory(root *os.Root) (*os.File, error) {
	return root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

func collectionGCOpenRegular(root *os.Root, name string, _ bool) (*os.File, error) {
	// Read-only descriptors support fsync on these platforms. Preserve the
	// existing required permissions for intent revalidation.
	return root.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
}

func collectionGCDirectoryCheckpoint(directory *os.File) error {
	return directory.Sync()
}

func collectionGCRemove(root *os.Root, _ *os.File, name string, _ collectionGCIdentity, _ bool) error {
	return root.Remove(name)
}

func collectionGCPublishIntent(r *collectionRetirement, raw []byte) error {
	file, err := r.root.OpenFile(collectionGCIntentName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return errCollectionGCBlocked
	}
	// Preserve incomplete intent evidence rather than deleting/replacing it.
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return r.dir.Sync()
}
