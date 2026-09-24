//go:build externaljobs

package cprafixture

func extensionKind(route string) (string, bool) {
	if route == "job-types" {
		return "JobType", true
	}
	return "", false
}
