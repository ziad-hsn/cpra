//go:build !linux && !darwin && !windows

package management

import "os"

func startupNativeMode(*os.File, bool, bool) (startupIdentity, error) {
	return startupIdentity{}, ErrStageUnavailable
}
func startupOpenDirectory(*os.Root) (*os.File, error)             { return nil, ErrStageUnavailable }
func startupOpenRegular(*os.Root, string, bool) (*os.File, error) { return nil, ErrStageUnavailable }
func startupDirectoryCheckpoint(*os.File) error                   { return ErrStageUnavailable }
func startupRemove(*os.Root, *os.File, string, startupIdentity, bool) error {
	return ErrStageUnavailable
}
func startupPublish(*os.File, *os.Root, string, string) error { return ErrStageUnavailable }
