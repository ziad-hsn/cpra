//go:build !externaljobs

package persistence

func workerRetainsAction(image, string) bool { return false }
