//go:build linux || darwin

package persistence

import (
	"math"
	"os"

	"golang.org/x/sys/unix"
)

func collectionAllocatedBytes(file *os.File) (int64, bool) {
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || stat.Blocks < 0 || stat.Blocks > math.MaxInt64/512 {
		return 0, false
	}
	return stat.Blocks * 512, true // Linux/macOS st_blocks counts 512-byte units.
}
