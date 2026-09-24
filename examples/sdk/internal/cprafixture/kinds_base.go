//go:build !externaljobs

package cprafixture

func extensionKind(string) (string, bool) { return "", false }
