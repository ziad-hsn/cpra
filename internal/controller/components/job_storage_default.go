//go:build !externaljobs

package components

type jobStorageExtensions struct{}

func (e jobStorageExtensions) clone() jobStorageExtensions { return e }

func (j *JobStorage) LocalCheck() bool { return true }
