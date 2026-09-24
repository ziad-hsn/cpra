//go:build !windows

package installpath

import "fmt"

func windowsLayout(string) (Layout, error) {
	return Layout{}, fmt.Errorf("Windows paths unavailable on this platform")
}
