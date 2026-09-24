//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestJobTypeDefaultBuildExcludesWireAndMethods(t *testing.T) {
	for _, payload := range []string{"null", "{}", `{"action":"create"}`} {
		for _, version := range []int{1, 14, 15, 16} {
			wire := fmt.Sprintf(`{"version":%d,"commands":[{"kind":"recover","at":"2026-09-24T00:00:00Z","job_type":%s}]}`, version, payload)
			if _, err := decodeEnvelope([]byte(wire)); err == nil {
				t.Fatal("default accepted external command field", version, payload)
			}
			allocation := fmt.Sprintf(`{"version":%d,"commands":[{"kind":"recover","at":"2026-09-24T00:00:00Z","job_type_allocation":%s}]}`, version, payload)
			if _, err := decodeEnvelope([]byte(allocation)); err == nil {
				t.Fatal("default accepted external allocation field", version, payload)
			}
			wire = fmt.Sprintf(`{"version":%d,"index":1,"monitors":{},"slo":{},"job_types":%s}`, version, payload)
			if _, err := decodeImage(bytes.NewBufferString(wire)); err == nil {
				t.Fatal("default accepted external image field", version, payload)
			}
		}
	}
	if supportedFormat(15) || supportedFormat(16) || LatestFormatVersion != 14 {
		t.Fatal("default enabled external format")
	}
	for _, name := range []string{"CommitJobType", "JobType", "JobTypeVersion", "JobTypes", "JobTypeIDs", "ReserveJobTypeOperation", "CommitJobTypeOperation", "JobTypeSnapshot"} {
		if _, ok := reflect.TypeFor[*Store]().MethodByName(name); ok {
			t.Fatal("default exposed tagged method", name)
		}
	}
	if _, ok := reflect.TypeFor[Command]().FieldByName("JobType"); ok {
		t.Fatal("default exposed tagged command field")
	}
	for _, version := range []int{15, 16} {
		if _, _, err := decodeSnapshot(bytes.NewBufferString(fmt.Sprintf("CPRA-COLLECTION-SNAPSHOT-%d\n{}", version)), ""); err == nil {
			t.Fatal("default accepted external snapshot framing", version)
		}
	}
}
