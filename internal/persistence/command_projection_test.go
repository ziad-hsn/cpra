package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDigestV1ProjectionCoversEveryLegacyField(t *testing.T) {
	var command Command
	c := reflect.ValueOf(&command).Elem()
	frozen := reflect.TypeFor[operationCommandDigestV1]()
	if c.NumField() != frozen.NumField()+10 {
		t.Fatal("Command gained a field without a reviewed legacy projection")
	}
	for n := 0; n < c.NumField(); n++ {
		field := c.Type().Field(n)
		if field.Name == "ActionRetirement" {
			if field.Type != reflect.TypeFor[*ActionRetirementCommand]() || field.Tag.Get("json") != "action_retirement,omitempty" {
				t.Fatal("unreviewed action retirement shape")
			}
			// Retirement has its own format-46 command contract; resource-operation
			// replay identities must reject it rather than silently omit it.
			if _, _, err := operationDigest(Command{Kind: "action_retire", ActionRetirement: &ActionRetirementCommand{}}); err == nil {
				t.Fatal("operation digest accepted retirement data")
			}
			continue
		}
		if field.Name == "InstallationID" || field.Name == "NoticeRevision" {
			tag := "installation_id,omitempty"
			if field.Name == "NoticeRevision" {
				tag = "notice_revision,omitempty"
			}
			if field.Type != reflect.TypeFor[string]() || field.Tag.Get("json") != tag {
				t.Fatal("unreviewed installation/notification identity shape")
			}
			// Format 44/45 execution identities never enter the frozen
			// management-operation digest; nonzero values are rejected.
			continue
		}
		if field.Name == "Failure" || field.Name == "RetryDelay" {
			want, tag := reflect.TypeFor[FailureDiagnostic](), "failure,omitzero"
			if field.Name == "RetryDelay" {
				want, tag = reflect.TypeFor[time.Duration](), "retry_delay,omitzero"
			}
			if field.Type != want || field.Tag.Get("json") != tag {
				t.Fatal("unreviewed execution diagnostic/retry shape")
			}
			// Execution-only fields require format 40/41. They cannot alter
			// the frozen resource-management digest, and their zero values
			// must preserve the historical JSON bytes checked below.
			continue
		}
		if field.Name == "ActionDeadline" {
			if field.Type != reflect.TypeFor[time.Time]() || field.Tag.Get("json") != "action_deadline,omitzero" {
				t.Fatal("unreviewed notification deadline shape")
			}
			// Deadlines are allowed only for execution starts, never operations
			// using the frozen resource-management digest v1 projection.
			continue
		}
		if field.Name == "AgentConnection" || field.Name == "AgentObservation" {
			want, tag := reflect.TypeFor[*AgentConnection](), "agent_connection,omitempty"
			if field.Name == "AgentObservation" {
				want, tag = reflect.TypeFor[*AgentObservationCommand](), "agent_observation,omitempty"
			}
			if field.Type != want || field.Tag.Get("json") != tag {
				t.Fatal("unreviewed agent observation shape")
			}
			continue
		}
		if field.Name == "AgentReceipt" {
			if field.Type != reflect.TypeFor[*AgentReceiptCommand]() || field.Tag.Get("json") != "agent_receipt,omitempty" {
				t.Fatal("unreviewed agent receipt shape")
			}
			continue
		}
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

func TestOperationDigestRejectsExecutionOnlyFields(t *testing.T) {
	base := Command{Kind: "control", MonitorID: "monitor", At: policyTime(), Control: &ControlCommand{
		Action: "disable", MonitorUID: "uid", ExpectedRevision: "old", Revision: "new", OperationID: "original", Actor: "alice",
	}}
	for _, mutate := range []struct {
		name string
		set  func(*Command)
	}{
		{"failure", func(c *Command) { c.Failure = NewFailureDiagnostic("dns_failed") }},
		{"retry delay", func(c *Command) { c.RetryDelay = time.Second }},
		{"action deadline", func(c *Command) { c.ActionDeadline = c.At.Add(time.Minute) }},
		{"installation identity", func(c *Command) { c.InstallationID = "installation" }},
		{"notice revision", func(c *Command) { c.NoticeRevision = "notice" }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			c := base
			mutate.set(&c)
			if _, _, err := operationDigest(c); !errors.Is(err, ErrOperationReservation) {
				t.Fatal("frozen operation digest silently omitted an execution field", err)
			}
		})
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
