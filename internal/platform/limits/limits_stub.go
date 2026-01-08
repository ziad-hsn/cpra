//go:build !linux && !darwin && !windows

package limits

func detect() Limits {
	return Limits{
		Source: "undefined",
	}
}
