package persistence

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestDigestV1ProjectionCoversEveryLegacyField(t *testing.T) {
	var command Command
	c := reflect.ValueOf(&command).Elem()
	frozen := reflect.TypeFor[operationCommandDigestV1]()
	if c.NumField() != frozen.NumField()+1 {
		t.Fatal("Command gained a field without a reviewed legacy projection")
	}
	for n := 0; n < c.NumField(); n++ {
		field := c.Type().Field(n)
		if field.Name == "commandExtensions" {
			if !field.Anonymous || field.IsExported() || field.Type != reflect.TypeFor[commandExtensions]() {
				t.Fatal("unreviewed extension shape")
			}
			continue
		}
		original, ok := frozen.FieldByName(field.Name)
		if !ok || original.Type != field.Type && field.Name != "Catalog" || original.Tag != field.Tag {
			t.Fatalf("legacy field changed: %s", field.Name)
		}
		v := c.Field(n)
		switch v.Kind() {
		case reflect.Pointer:
			v.Set(reflect.New(v.Type().Elem()))
		case reflect.String:
			v.SetString(field.Name)
		case reflect.Bool:
			v.SetBool(true)
		case reflect.Uint64:
			v.SetUint(17)
		case reflect.Struct:
			if v.Type() != reflect.TypeFor[time.Time]() {
				t.Fatal("new legacy struct needs coverage")
			}
			v.Set(reflect.ValueOf(time.Date(2026, 9, 24, 1, 2, 3, 4, time.UTC)))
		default:
			t.Fatalf("legacy field %s needs a nonzero test value", field.Name)
		}
	}
	projected := reflect.ValueOf(legacyOperationCommandDigest(command))
	for n := 0; n < projected.NumField(); n++ {
		name := projected.Type().Field(n).Name
		a, _ := json.Marshal(projected.Field(n).Interface())
		b, _ := json.Marshal(c.FieldByName(name).Interface())
		if !bytes.Equal(a, b) {
			t.Fatalf("projection omitted %s", name)
		}
	}
	current, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	original, err := json.Marshal(projected.Interface())
	if err != nil || !bytes.Equal(current, original) {
		t.Fatal("empty extension changed historical command bytes", err)
	}
}

func TestJobTypeCannotBypassDedicatedStateViaCatalog(t *testing.T) {
	s := openCatalogMemory(t)
	r := catalogRecord(t, s, "JobType", "external", "uid", "revision", "protected")
	if err := (CatalogMutation{Record: r, Create: true}).validate(); err == nil {
		t.Fatal("ordinary catalog accepted JobType")
	}
	if err := (BootstrapCommand{Action: "seed", StageID: "stage", Ordinal: 1, Record: &r}).validate(); err == nil {
		t.Fatal("bootstrap accepted JobType")
	}
	r.CommittedIndex = 1
	i := image{Version: CatalogFormatVersion, Index: 1, Catalog: map[string]CatalogRecord{r.Key.indexKey(): r}, Monitors: map[string]Monitor{}}
	if validateCatalogImage(i) == nil {
		t.Fatal("ordinary catalog snapshot accepted JobType")
	}
}

func TestDigestV1CatalogProjectionCoverage(t *testing.T) {
	for _, pair := range [][2]reflect.Type{{reflect.TypeFor[CatalogRecord](), reflect.TypeFor[catalogRecordDigestV1]()}, {reflect.TypeFor[CatalogMutation](), reflect.TypeFor[catalogMutationDigestV1]()}} {
		count := 0
		for n := 0; n < pair[0].NumField(); n++ {
			field := pair[0].Field(n)
			if field.Name == "catalogRecordExtensions" {
				if !field.Anonymous || field.IsExported() {
					t.Fatal("extension shape changed")
				}
				continue
			}
			original, ok := pair[1].FieldByName(field.Name)
			if !ok || original.Tag != field.Tag || original.Type != field.Type && field.Name != "Record" {
				t.Fatal("unprojected legacy field", field.Name)
			}
			count++
		}
		if count != pair[1].NumField() {
			t.Fatal("projection field count changed")
		}
	}
	r := catalogDeltaRecord("Monitor", "frozen", CatalogKey{Kind: "Credential", ID: "one"})
	r.CommittedIndex = 42
	r.DependentsVersion = 24
	m := &CatalogMutation{OperationID: "operation", Actor: "actor", Record: r, Create: true, ExpectedUID: "uid", ExpectedRevision: "revision", ExpectedDependentsVersion: 8, Conditions: []CatalogCondition{{Key: r.References[0], UID: "target", Revision: "target-revision"}}}
	a, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(legacyCatalogMutationDigest(m))
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("catalog historical projection changed bytes", err)
	}
}
