package persistence

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func collectionAllocatedBytes(file *os.File) (int64, bool) {
	// FILE_STANDARD_INFO contains two LARGE_INTEGERs, a DWORD, two BOOLEANs,
	// and native tail padding. Its size is 24 bytes on supported 64-bit targets.
	var info struct {
		allocation, endOfFile    int64
		links                    uint32
		deletePending, directory byte
		padding                  [2]byte
	}
	if windows.GetFileInformationByHandleEx(windows.Handle(file.Fd()), windows.FileStandardInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))) != nil || info.allocation < 0 || info.deletePending != 0 || info.directory != 0 {
		return 0, false
	}
	return info.allocation, true
}
