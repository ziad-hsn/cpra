package cpra_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestConsumerOperationCountPresenceThroughTLS(t *testing.T) {
	for _, test := range []struct {
		name, fields string
		present      bool
		invalid      bool
	}{
		{name: "omitted"},
		{name: "explicit-zero", fields: `,"uploaded":0,"committed":0,"applied":0`, present: true},
		{name: "uploaded-null", fields: `,"uploaded":null`, invalid: true},
		{name: "committed-null", fields: `,"committed":null`, invalid: true},
		{name: "applied-null", fields: `,"applied":null`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := consumerClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/"+consumerHandle {
					t.Error("unexpected operation request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"`+consumerHandle+`","state":"committed","contentDigest":"`+strings.Repeat("a", 64)+`"`+test.fields+`}`)
			})
			response, err := client.Operations.Get(context.Background(), consumerHandle)
			if test.invalid {
				if err == nil {
					t.Fatal("non-nullable operation count accepted null")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(response.Data)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"uploaded", "committed", "applied"} {
				value, ok := fields[name]
				if ok != test.present || (ok && string(value) != "0") {
					t.Fatalf("public operation count %s lost availability: %s", name, raw)
				}
			}
		})
	}
}

func TestConsumerOperationFlagPresenceThroughTLS(t *testing.T) {
	for _, field := range []string{"validated", "committed", "applied"} {
		for _, value := range []string{"", "false", "true", "null"} {
			t.Run(field+"/"+value, func(t *testing.T) {
				body := `{"id":"` + consumerHandle + `","state":"failed","contentDigest":"digest"`
				addition := ""
				if value != "" {
					addition = `,"` + field + `":` + value
				}
				if field == "validated" {
					body += addition
				} else {
					body += `,"items":[{"id":"Monitor/service","outcome":"activation_rejected"` + addition + `}]`
				}
				body += "}"
				client := consumerClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, body)
				})
				response, err := client.Operations.Get(context.Background(), consumerHandle)
				if value == "null" {
					if err == nil {
						t.Fatal("non-nullable progress flag accepted null")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				observed := response.Data.Validated
				if field == "committed" {
					observed = response.Data.Items[0].Committed
				} else if field == "applied" {
					observed = response.Data.Items[0].Applied
				}
				if (observed == nil) != (value == "") || observed != nil && *observed != (value == "true") {
					t.Fatal("typed progress flag lost presence")
				}
				raw, err := json.Marshal(response.Data)
				if err != nil {
					t.Fatal(err)
				}
				var result map[string]json.RawMessage
				_ = json.Unmarshal(raw, &result)
				if field != "validated" {
					var items []map[string]json.RawMessage
					_ = json.Unmarshal(result["items"], &items)
					result = items[0]
				}
				if string(result[field]) != value {
					t.Fatal("re-encoded progress flag changed presence", string(raw))
				}
			})
		}
	}
}
