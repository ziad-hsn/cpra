//go:build externaljobs

package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestExternalOperationInventory(t *testing.T) {
	for _, op := range inventory(t) {
		if op.BuildTag == "" {
			continue
		}
		t.Run(op.OperationID, func(t *testing.T) {
			c := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != op.Method || r.URL.Path != strings.ReplaceAll(op.Path, "{id}", "m") {
					t.Errorf("operation mismatch %s %s", r.Method, r.URL.Path)
				}
				if strings.HasPrefix(op.SDKMethod, "WorkerClient.") {
					_ = json.NewEncoder(w).Encode(workerProtocolReply(op.OperationID))
					return
				}
				_, _ = w.Write(inventoryResponse(t, op.Schema))
			}, nil)
			var service any
			split := strings.Split(op.SDKMethod, ".")
			switch split[0] {
			case "JobTypesService":
				service = c.JobTypes()
			case "Client":
				service = c
			case "WorkerClient":
				service = &WorkerClient{c}
			}
			if service == nil {
				t.Fatal(op.SDKMethod)
			}
			method := reflect.ValueOf(service).MethodByName(split[1])
			args := []reflect.Value{reflect.ValueOf(context.Background())}
			seenString := false
			for i := 1; i < method.Type().NumIn(); i++ {
				typ := method.Type().In(i)
				var v reflect.Value
				switch typ.Name() {
				case "PollRequest", "StartRequest", "HeartbeatRequest", "Outcome", "LateEvidenceRequest":
					v = reflect.ValueOf(workerProtocolRequest(op.OperationID))
				case "JobType":
					v = reflect.ValueOf(api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "m"}, Spec: api.JobTypeSpec{Version: "1", Kind: "check", Handler: "check", ProtocolVersion: "1", ParameterSchema: json.RawMessage(`{}`), ResultSchema: json.RawMessage(`{}`)}})
				case "string":
					s := "m"
					if seenString {
						s = "rv-1"
					}
					seenString = true
					v = reflect.ValueOf(s)
				default:
					v = requestValue(t, typ)
				}
				args = append(args, v)
			}
			results := method.Call(args)
			if e := results[len(results)-1].Interface(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestWorkerRequiresAuthAndTLS(t *testing.T) {
	if _, e := NewWorkerClient(Config{BaseURL: "https://host"}); e == nil {
		t.Fatal("anonymous worker accepted")
	}
	if _, e := NewWorkerClient(Config{BaseURL: "http://host", AuthToken: "secret"}); e == nil {
		t.Fatal("insecure worker accepted")
	}
}
func TestExternalBuildAcceptsTypedExternalConfig(t *testing.T) {
	raw, _ := json.Marshal(api.ExternalConfig{JobTypeID: "custom", Version: "1", Parameters: json.RawMessage(`{"url":"https://target"}`)})
	if e := api.ValidateDriver("check", api.DriverConfig{Type: "external", Config: raw}); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(api.Schema()), "external-workers") {
		t.Fatal("tagged schema absent")
	}
}

func TestWorkerMalformedOutcomeFailsLocally(t *testing.T) {
	c := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("malformed outcome sent") }, nil)
	worker := &WorkerClient{c}
	_, err := worker.Result(context.Background(), api.Outcome{Data: json.RawMessage(`{"broken"`)})
	if err == nil || errors.Is(err, ErrAmbiguous) {
		t.Fatal(err)
	}
}
