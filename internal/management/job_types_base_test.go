//go:build !externaljobs

package management

import (
	"reflect"
	"testing"
)

func TestCatalogDefaultBuildExcludesJobTypes(t *testing.T) {
	for _, name := range []string{"PrepareJobType", "CommitJobType", "GetJobType", "PrepareDeleteJobType"} {
		if _, ok := reflect.TypeFor[*Catalog]().MethodByName(name); ok {
			t.Fatalf("optional method %s is compiled into default catalog", name)
		}
	}
	if supportedKind("JobType") {
		t.Fatal("generic catalog accepts JobType")
	}
}
