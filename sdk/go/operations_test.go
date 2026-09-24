package cpra

import (
	"context"
	"encoding/json"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type inventoryEntry struct{ OperationID, Method, Path, Schema, Request, SDKMethod, BuildTag, ContractTest string }

func inventory(t *testing.T) []inventoryEntry {
	t.Helper()
	raw, e := os.ReadFile("testdata/operations.json")
	if e != nil {
		t.Fatal(e)
	}
	var v []inventoryEntry
	if e = json.Unmarshal(raw, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func requestValue(t *testing.T, typ reflect.Type) reflect.Value {
	t.Helper()
	if typ == reflect.TypeOf(api.MergePatch(nil)) {
		return reflect.ValueOf(api.MergePatch(`{"spec":{"enabled":false}}`))
	}
	var value any
	switch typ.Name() {
	case "ListOptions":
		value = ListOptions{}
	case "Monitor":
		value = monitor()
	case "NotificationEndpoint":
		value = api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "m"}, Spec: api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"output.log"}`)}}
	case "Recipient":
		value = api.Recipient{APIVersion: api.APIVersion, Kind: "Recipient", Metadata: api.Metadata{ID: "m"}, Spec: api.RecipientSpec{EndpointRefs: []string{"e"}}}
	case "NotificationGroup":
		value = api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: "m"}, Spec: api.NotificationGroupSpec{EndpointRefs: []string{"e"}}}
	case "Credential":
		value = api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "m"}, Spec: api.CredentialSpec{Value: api.Pointer("secret")}}
	case "ControlRequest":
		value = api.ControlRequest{Revision: "rv-1", Reason: "investigating", Duration: "1h"}
	case "OperationCreateRequest":
		value = api.OperationCreateRequest{AdmissionTicket: "fixture-ticket", IdentityFormat: commitment.Format, IdentityKey: api.Pointer(strings.Repeat("a", 64)), SourceFingerprint: api.Pointer(strings.Repeat("b", 64)), ContentDigest: strings.Repeat("c", 64), ItemCount: 1}
	case "CollectionPrepareRequest":
		r := validCollectionFixture(t)
		value = api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount}
	case "PreflightRequest":
		value = validCollectionFixture(t)
	case "UploadRequest":
		value = api.UploadRequest{Items: validCollectionFixture(t).Items}
	case "MergePatch", "RawMessage":
		value = api.MergePatch(`{"spec":{"enabled":false}}`)
	default:
		return reflect.New(typ).Elem()
	}
	return reflect.ValueOf(value)
}
func TestOperationInventory(t *testing.T) {
	for _, op := range inventory(t) {
		if op.BuildTag != "" {
			continue
		}
		if op.ContractTest == "TestReselectionOperationInventory" {
			continue // Binary bodies and explicit 201/202/204 responses have their own inventory-driven contract test.
		}
		t.Run(op.OperationID, func(t *testing.T) {
			calls := 0
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != op.Method || r.URL.Path != strings.ReplaceAll(op.Path, "{id}", "m") {
					t.Errorf("operation mismatch: %s %s", r.Method, r.URL.Path)
				}
				if op.OperationID == "ValidateOperation" {
					w.WriteHeader(202)
				}
				if op.OperationID == "ActivateOperation" {
					_ = json.NewEncoder(w).Encode(api.Operation{ID: "m", State: "applying"})
					return
				}
				_, _ = w.Write(inventoryResponse(t, op.Schema))
			}, nil)
			services := map[string]any{"Client": c, "MonitorsService": c.Monitors, "NotificationEndpointsService": c.NotificationEndpoints, "NotificationGroupsService": c.NotificationGroups, "RecipientsService": c.Recipients, "CredentialsService": c.Credentials, "IncidentsService": c.Incidents, "ActionsService": c.Actions, "OperationsService": c.Operations, "QueuesService": c.Queues, "PoolsService": c.Pools, "SystemsService": c.Systems, "SLOService": c.SLO}
			split := strings.Split(op.SDKMethod, ".")
			if len(split) != 2 || services[split[0]] == nil {
				t.Fatal("missing SDK operation", op.SDKMethod)
			}
			method := reflect.ValueOf(services[split[0]]).MethodByName(split[1])
			if !method.IsValid() {
				t.Fatal("missing method", op.SDKMethod)
			}
			args := []reflect.Value{reflect.ValueOf(context.Background())}
			seenString := false
			for i := 1; i < method.Type().NumIn(); i++ {
				typ := method.Type().In(i)
				if typ.Kind() == reflect.String {
					v := "m"
					if seenString {
						v = "rv-1"
					}
					seenString = true
					args = append(args, reflect.ValueOf(v))
				} else if op.OperationID == "GetHistory" && typ == reflect.TypeOf(ListOptions{}) {
					args = append(args, reflect.ValueOf(ListOptions{MonitorID: "m"}))
				} else {
					args = append(args, requestValue(t, typ))
				}
			}
			result := method.Call(args)
			if err := result[len(result)-1].Interface(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("operation did not call transport")
			}
		})
	}
}

func inventoryResponse(t *testing.T, name string) []byte {
	t.Helper()
	raw, e := os.ReadFile("testdata/responses.json")
	if e != nil {
		t.Fatal(e)
	}
	var data map[string]json.RawMessage
	if e = json.Unmarshal(raw, &data); e != nil {
		t.Fatal(e)
	}
	if len(data[name]) == 0 {
		t.Fatal("missing response fixture", name)
	}
	return data[name]
}
