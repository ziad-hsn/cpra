//go:build windows

package collection

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Install an owner-only protected DACL at creation, before plaintext is written.
// Child files inherit this DACL; Unix permission bits alone are insufficient.
func privateDirectory(parent string) (string, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return "", err
	}
	sddl, err := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return "", err
	}
	var descriptor uintptr
	convert := syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	ok, _, callErr := convert.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if ok == 0 {
		return "", fmt.Errorf("create private staging security descriptor: %w", callErr)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	attributes := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: descriptor}
	for attempts := 0; attempts < 10; attempts++ {
		var suffix [16]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", err
		}
		path := filepath.Join(parent, "cpra-collection-"+hex.EncodeToString(suffix[:]))
		name, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return "", err
		}
		err = syscall.CreateDirectory(name, &attributes)
		if err == nil {
			return path, nil
		}
		if err != syscall.ERROR_ALREADY_EXISTS {
			return "", err
		}
	}
	return "", fmt.Errorf("cannot allocate private staging directory")
}
