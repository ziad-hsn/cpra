// Package util provides legacy aliases for pkg/util.
// This file will be removed after import paths are updated.
package util

import (
	"cpra/pkg/util"
)

// Re-export functions from pkg/util
var (
	ValidateEnum = util.ValidateEnum
)

// Re-export generic functions
func AppendIf[T any](slice []T, condition bool, items ...T) []T {
	return util.AppendIf(slice, condition, items...)
}

func InRange[T interface{ ~int | ~float64 }](value, min, max T) bool {
	return util.InRange(value, min, max)
}