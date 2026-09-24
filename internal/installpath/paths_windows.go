package installpath

import (
	"golang.org/x/sys/windows"
	"path/filepath"
)

func windowsLayout(scope string) (Layout, error) {
	folder := windows.FOLDERID_LocalAppData
	if scope == "system" {
		folder = windows.FOLDERID_ProgramData
	}
	base, err := windows.KnownFolderPath(folder, 0)
	if err != nil {
		return Layout{}, err
	}
	base = filepath.Join(base, "CPRa")
	bin := filepath.Join(base, "bin")
	if scope == "system" {
		programs, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
		if err != nil {
			return Layout{}, err
		}
		bin = filepath.Join(programs, "CPRa")
	}
	return Layout{Scope: scope, ConfigDir: filepath.Join(base, "config"), StateDir: filepath.Join(base, "state"), LogDir: filepath.Join(base, "logs"), BinDir: bin}, nil
}
