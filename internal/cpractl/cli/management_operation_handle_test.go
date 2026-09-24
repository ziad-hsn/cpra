package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const sequencedOperationID = "op." + cliOperationID + ".00000000000000000001"

func TestManagementOperationHandlesAreCanonicalAndBounded(t *testing.T) {
	for _, value := range []string{cliOperationID, sequencedOperationID, "op." + cliOperationID + ".18446744073709551615"} {
		if !safeManagementHandle(value) {
			t.Fatalf("canonical operation handle rejected: %q", value)
		}
	}
	invalid := []string{"", "operation-1", "00000000-0000-0000-0000-000000000000", strings.ToUpper(cliOperationID), "urn:uuid:" + cliOperationID,
		"op.00000000-0000-0000-0000-000000000000.00000000000000000001", "op." + cliOperationID + ".00000000000000000000", "op." + cliOperationID + ".18446744073709551616",
		"op." + cliOperationID + ".1", "op." + cliOperationID + ".+0000000000000000001", "OP." + cliOperationID + ".00000000000000000001",
		" " + sequencedOperationID, sequencedOperationID + " ", sequencedOperationID + "\n", sequencedOperationID + "\x1b[31m", sequencedOperationID + "/next", sequencedOperationID + "?token=x", sequencedOperationID + "#x", sequencedOperationID + "%2e", "https://host/" + sequencedOperationID}
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	for _, value := range invalid {
		if safeManagementHandle(value) {
			t.Fatalf("noncanonical operation handle accepted: %q", value)
		}
		if _, _, err := fixture.run(t, nil, "get", "operation", value); err == nil {
			t.Fatal("invalid operation read was not rejected")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid operation handle reached the server")
	}
}

func TestManagementOperationReceiptsRetainOriginalHandle(t *testing.T) {
	for _, handle := range []string{cliOperationID, sequencedOperationID} {
		for _, interrupted := range []bool{false, true} {
			t.Run(handle+map[bool]string{false: "/success", true: "/interrupted"}[interrupted], func(t *testing.T) {
				var writes, reads atomic.Int32
				fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Operation-ID", handle)
					if r.Method == http.MethodGet {
						reads.Add(1)
						if r.URL.EscapedPath() != "/api/v2/operations/"+handle || r.URL.RawQuery != "" {
							t.Error("operation handle changed in request path")
						}
						_ = json.NewEncoder(w).Encode(api.Operation{ID: handle, ContentDigest: strings.Repeat("a", 64), State: "committed", Committed: api.Pointer(int64(1))})
						return
					}
					writes.Add(1)
					if interrupted {
						w.Header().Set("Content-Length", "10000")
						w.WriteHeader(http.StatusCreated)
						_, _ = io.WriteString(w, "{")
						return
					}
					_ = json.NewEncoder(w).Encode(cliResource("Monitor", "m"))
				})
				raw, _ := json.Marshal(cliResource("Monitor", "m"))
				stdout, stderr, err := fixture.run(t, raw, "create", "monitor", "-f", "-", "-o", "json")
				if interrupted {
					if !errors.Is(err, cpra.ErrAmbiguous) || !strings.Contains(err.Error(), "operation "+handle) || stdout != "" {
						t.Fatal("uncertain receipt lost its original operation", err)
					}
				} else if err != nil || !strings.Contains(stderr, "Operation: "+handle+" ") {
					t.Fatal("successful receipt lost its original operation", err)
				}
				stdout, _, err = fixture.run(t, nil, "get", "operation", handle, "-o", "json")
				var operation api.Operation
				if err != nil || json.Unmarshal([]byte(stdout), &operation) != nil || operation.ID != handle || reads.Load() != 1 || writes.Load() != 1 {
					t.Fatal("operation read changed identity or retried a mutation", err)
				}
			})
		}
	}
}

func TestManagementAllocationUnconfirmedMessageRequiresServerGuarantee(t *testing.T) {
	for _, marker := range []bool{false, true} {
		t.Run(map[bool]string{false: "ambiguous", true: "not-submitted"}[marker], func(t *testing.T) {
			var calls atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/problem+json")
				if marker {
					w.Header().Set("X-CPRa-Admission", "not-submitted")
				}
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"type":"about:blank","title":"Operation allocation unavailable","status":503,"code":"operationAllocationUnconfirmed","detail":"never-display-this-private-detail"}`)
			})
			raw, _ := json.Marshal(cliResource("Monitor", "m"))
			stdout, stderr, err := fixture.run(t, raw, "create", "monitor", "-f", "-", "-o", "json")
			if err == nil || calls.Load() != 1 || errors.Is(err, cpra.ErrNotAdmitted) != marker || errors.Is(err, cpra.ErrAmbiguous) == marker || !errors.Is(err, cpra.ErrUnavailable) {
				t.Fatal("CLI weakened allocation uncertainty or retried", err)
			}
			if strings.Contains(stdout+stderr+err.Error(), "never-display-this-private-detail") || stdout != "" {
				t.Fatal("CLI disclosed problem details or emitted a success receipt")
			}
			if marker && !strings.Contains(err.Error(), "no resource or action mutation was submitted") {
				t.Fatal("CLI omitted confirmed non-admission", err)
			}
		})
	}
}

func TestManagementOperationCountsPreserveUnavailableAndZero(t *testing.T) {
	for _, test := range []struct {
		name, fields, want string
	}{
		{"absent", "", "unavailable"},
		{"null", `,"uploaded":null,"committed":null,"applied":null`, "unavailable"},
		{"zero", `,"uploaded":0,"committed":0,"applied":0`, "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&output)
			raw := []byte(`{"id":"` + sequencedOperationID + `","state":"committed","contentDigest":"` + strings.Repeat("a", 64) + `"` + test.fields + `}`)
			if err := writeManagementTable(command, &options{}, raw, false); err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, line := range strings.Split(output.String(), "\n") {
				fields := strings.Fields(line)
				if len(fields) > 0 && (fields[0] == "Uploaded:" || fields[0] == "Committed:" || fields[0] == "Applied:") {
					seen++
					if fields[len(fields)-1] != test.want {
						t.Fatal("operation count availability was fabricated", line)
					}
				}
			}
			if seen != 3 {
				t.Fatal("operation counts were omitted from output")
			}
		})
	}
}

func TestManagementOperationCountPresenceThroughTLS(t *testing.T) {
	for _, test := range []struct {
		name, fields, want string
		invalid            bool
	}{
		{"absent", "", "unavailable", false},
		{"zero", `,"uploaded":0,"committed":0,"applied":0`, "0", false},
		{"null", `,"uploaded":null,"committed":null,"applied":null`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/"+sequencedOperationID {
					t.Error("unexpected operation request")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"`+sequencedOperationID+`","state":"committed","contentDigest":"`+strings.Repeat("a", 64)+`"`+test.fields+`}`)
			})
			stdout, _, err := fixture.run(t, nil, "get", "operation", sequencedOperationID)
			if test.invalid {
				if err == nil || stdout != "" {
					t.Fatal("invalid null response was displayed as an operation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			seen := 0
			for _, line := range strings.Split(stdout, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 0 && (fields[0] == "Uploaded:" || fields[0] == "Committed:" || fields[0] == "Applied:") {
					seen++
					if fields[len(fields)-1] != test.want {
						t.Fatalf("TLS response count presence was lost: %s", line)
					}
				}
			}
			if seen != 3 {
				t.Fatal("operation counts missing from CLI output")
			}
		})
	}
}
