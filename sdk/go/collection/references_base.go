//go:build !externaljobs

package collection

func additionalReferences(Item, func(string, string) error) error { return nil }
