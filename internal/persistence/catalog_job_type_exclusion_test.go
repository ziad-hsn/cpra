//go:build !externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestCatalogJobTypeDefaultExclusion(t *testing.T) {
	for _, value := range []string{"null", "[]", `[{"job_type_id":"x"}]`} {
		raw := []byte(fmt.Sprintf(`{"key":{"kind":"Monitor","id":"x"},"job_type_references":%s}`, value))
		var record CatalogRecord
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if d.Decode(&record) == nil {
			t.Fatal("default decoded nested external metadata", value)
		}
		for _, version := range []int{1, 14, 15, 16, 17, 18} {
			command := fmt.Sprintf(`{"version":%d,"commands":[{"kind":"catalog","catalog":{"record":%s}}]}`, version, raw)
			if _, err := decodeEnvelope([]byte(command)); err == nil {
				t.Fatal("default decoded external command", version, value)
			}
			image := fmt.Sprintf(`{"version":%d,"index":1,"monitors":{},"slo":{},"catalog":{"Monitor\u0000x":%s}}`, version, raw)
			if _, err := decodeImage(bytes.NewBufferString(image)); err == nil {
				t.Fatal("default decoded external image", version, value)
			}
		}
	}
	if _, ok := reflect.TypeFor[CatalogRecord]().FieldByName("JobTypeReferences"); ok {
		t.Fatal("default exposed record metadata")
	}
	if _, ok := reflect.TypeFor[*Store]().MethodByName("LookupJobTypeVersion"); ok {
		t.Fatal("default exposed version lookup")
	}
	if supportedFormat(18) || LatestFormatVersion != 14 {
		t.Fatal("default enabled external reference format")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-18\n{}"), ""); err == nil {
		t.Fatal("default accepted reference snapshot")
	}
}
