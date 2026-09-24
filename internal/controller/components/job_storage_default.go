//go:build !externaljobs

package components

type jobStorageExtensions struct{}

func (e jobStorageExtensions) clone() jobStorageExtensions { return e }
