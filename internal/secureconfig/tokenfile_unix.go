//go:build !windows

package secureconfig

import (
	"os"
	"syscall"
)

func checkTokenReader(policy KeyFileOptions) error {
	if policy.ReaderGroupID == nil || os.Geteuid() == 0 || os.Getegid() == *policy.ReaderGroupID {
		return nil
	}
	groups, err := os.Getgroups()
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group == *policy.ReaderGroupID {
			return nil
		}
	}
	return ErrTokenFilePolicy
}

func checkTokenDirectory(file *os.File, policy KeyFileOptions) error {
	if err := CheckTrustedDirectory(file); err != nil {
		return err
	}
	if policy.ReaderGroupID != nil {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		native, ok := info.Sys().(*syscall.Stat_t)
		if !ok || native.Uid != 0 {
			return ErrTokenFilePolicy
		}
	}
	return nil
}

func checkTokenAncestors(original, resolved string, policy KeyFileOptions) error {
	for _, path := range []string{original, resolved} {
		if err := checkUnixAncestors(path, policy); err != nil {
			return err
		}
		directories, err := aliasDirectories(path)
		if err != nil {
			return err
		}
		for _, directory := range directories {
			if err := checkUnixAncestors(directory, policy); err != nil {
				return err
			}
		}
	}
	return nil
}
