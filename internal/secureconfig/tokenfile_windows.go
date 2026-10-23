package secureconfig

import (
	"os"

	"golang.org/x/sys/windows"
)

func checkTokenReader(KeyFileOptions) error { return nil }

func checkTokenDirectory(file *os.File, policy KeyFileOptions) error {
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return ErrTokenFilePolicy
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return checkKeySecurityDescriptor(sd, policy, false)
}

func checkTokenAncestors(original, resolved string, policy KeyFileOptions) error {
	for _, path := range []string{original, resolved} {
		if err := checkWindowsAncestors(path, policy); err != nil {
			return err
		}
		directories, err := aliasDirectories(path)
		if err != nil {
			return err
		}
		for _, directory := range directories {
			if err := checkWindowsAncestors(directory, policy); err != nil {
				return err
			}
		}
	}
	return nil
}
