package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func testDiscovery() *discovery {
	return &discovery{clusterID: "cluster-a", namespace: "payments", domain: "cluster.local", pageSize: 2,
		output: io.Discard, diagnostics: io.Discard}
}

func TestEveryServiceGetsDNSAndUsableTCPPorts(t *testing.T) {
	tests := []struct {
		name           string
		change         func(*corev1.Service)
		count, notices int
		target         int64
	}{
		{"ClusterIP", func(*corev1.Service) {}, 2, 0, 8080},
		{"NodePort", func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeNodePort; s.Spec.Ports[0].NodePort = 32080 }, 2, 0, 8080},
		{"LoadBalancer", func(s *corev1.Service) { s.Spec.Type = corev1.ServiceTypeLoadBalancer }, 2, 0, 8080},
		{"ExternalName", func(s *corev1.Service) {
			s.Spec.Type = corev1.ServiceTypeExternalName
			s.Spec.ClusterIP = ""
			s.Spec.ExternalName = "provider.example"
		}, 2, 0, 8080},
		{"ExternalName-no-port", func(s *corev1.Service) {
			s.Spec.Type = corev1.ServiceTypeExternalName
			s.Spec.ExternalName = "provider.example"
			s.Spec.Ports = nil
		}, 1, 0, 0},
		{"headless-number", func(s *corev1.Service) {
			s.Spec.ClusterIP = corev1.ClusterIPNone
			s.Spec.Ports[0].TargetPort = intstr.FromInt32(9090)
		}, 2, 0, 9090},
		{"headless-name", func(s *corev1.Service) {
			s.Spec.ClusterIP = corev1.ClusterIPNone
			s.Spec.Ports[0].TargetPort = intstr.FromString("app")
		}, 1, 1, 0},
		{"UDP", func(s *corev1.Service) { s.Spec.Ports[0].Protocol = corev1.ProtocolUDP }, 1, 1, 0},
		{"SCTP", func(s *corev1.Service) { s.Spec.Ports[0].Protocol = corev1.ProtocolSCTP }, 1, 1, 0},
		{"mixed", func(s *corev1.Service) {
			s.Spec.Ports = append(s.Spec.Ports, corev1.ServicePort{Port: 53, Protocol: corev1.ProtocolUDP}, corev1.ServicePort{Port: 9443, Protocol: corev1.ProtocolTCP})
		}, 3, 1, 8080},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := demoService("api", corev1.ProtocolTCP, 8080)
			tt.change(&s)
			got, notices, err := testDiscovery().desired(&s)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.count || len(notices) != tt.notices {
				t.Fatalf("monitors=%d notices=%v", len(got), notices)
			}
			for _, m := range got {
				if err := api.ValidateResourceValue(m); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(got[0].Spec.Check.Driver)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte(`"type":"dns"`)) || !bytes.Contains(raw, []byte(`api.payments.svc.cluster.local`)) {
				t.Fatalf("DNS config %s", raw)
			}
			if tt.target != 0 {
				raw, err = json.Marshal(got[1].Spec.Check.Driver)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(raw, []byte(fmt.Sprintf(`"port":%d`, tt.target))) {
					t.Fatalf("TCP config %s", raw)
				}
			}
		})
	}
}

func TestIdentitySurvivesPortRenameButSeparatesClustersAndServiceRecreation(t *testing.T) {
	d := testDiscovery()
	s := demoService("api", corev1.ProtocolTCP, 8080)
	first, _, err := d.desired(&s)
	if err != nil {
		t.Fatal(err)
	}
	s.Spec.Ports[0].Name = "renamed"
	second, _, err := d.desired(&s)
	if err != nil {
		t.Fatal(err)
	}
	if first[1].Metadata.ID != second[1].Metadata.ID {
		t.Fatal("port rename changed monitor identity")
	}
	s.UID = "replacement-service"
	third, _, err := d.desired(&s)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Metadata.ID == third[0].Metadata.ID {
		t.Fatal("recreated Service reused monitor identity")
	}
	d.clusterID = "cluster-b"
	fourth, _, err := d.desired(&s)
	if err != nil {
		t.Fatal(err)
	}
	if third[0].Metadata.ID == fourth[0].Metadata.ID {
		t.Fatal("different clusters shared monitor identity")
	}
}

func newKubeDiscovery(t *testing.T, handler http.HandlerFunc) *discovery {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	core, err := typedcore.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	d := testDiscovery()
	d.services = core.Services(d.namespace)
	d.printOnly = true
	return d
}

func writeList(t *testing.T, w http.ResponseWriter, version, next string, services ...corev1.Service) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(corev1.ServiceList{
		TypeMeta: metav1.TypeMeta{Kind: "ServiceList", APIVersion: "v1"},
		ListMeta: metav1.ListMeta{ResourceVersion: version, Continue: next}, Items: services,
	}); err != nil {
		t.Error(err)
	}
}

func TestPaginatedListUsesContinueAndCollectionVersion(t *testing.T) {
	requests := 0
	d := newKubeDiscovery(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/namespaces/payments/services" || r.URL.Query().Get("limit") != "2" {
			t.Errorf("request %s", r.URL)
		}
		if requests == 1 {
			if r.URL.Query().Get("continue") != "" {
				t.Errorf("unexpected first cursor")
			}
			writeList(t, w, "900", "next-page", demoService("api", corev1.ProtocolTCP, 8080))
		} else {
			if r.URL.Query().Get("continue") != "next-page" {
				t.Errorf("missing next cursor")
			}
			writeList(t, w, "900", "", demoService("dns", corev1.ProtocolUDP, 53))
		}
	})
	var output bytes.Buffer
	d.output = &output
	version, err := d.list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != "900" || requests != 2 || strings.Count(output.String(), "\n") != 3 {
		t.Fatalf("version %q requests %d output %s", version, requests, output.String())
	}
}

func TestPartialListReportsFailureAndStillVisitsOtherServices(t *testing.T) {
	d := newKubeDiscovery(t, func(w http.ResponseWriter, r *http.Request) {
		bad := demoService("invalid", corev1.ProtocolTCP, 8080)
		bad.UID = ""
		writeList(t, w, "900", "", bad, demoService("good", corev1.ProtocolUDP, 53))
	})
	var output, diagnostics bytes.Buffer
	d.output, d.diagnostics = &output, &diagnostics
	version, err := d.list(context.Background())
	if err == nil || version != "" {
		t.Fatalf("partial success: version=%q err=%v", version, err)
	}
	if !strings.Contains(output.String(), "good.payments.svc") || !strings.Contains(diagnostics.String(), "invalid") {
		t.Fatal("missing source error or healthy Service")
	}
}

func TestWatchProcessesAddAndBookmarkThenRelistsExpiredVersion(t *testing.T) {
	requests := 0
	d := newKubeDiscovery(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if r.URL.Query().Get("watch") != "true" || r.URL.Query().Get("resourceVersion") != "900" {
				t.Errorf("watch %s", r.URL)
			}
			service := demoService("new", corev1.ProtocolTCP, 8080)
			service.ResourceVersion = "901"
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "ADDED", "object": service})
			bookmark := corev1.Service{TypeMeta: metav1.TypeMeta{Kind: "Service", APIVersion: "v1"}, ObjectMeta: metav1.ObjectMeta{ResourceVersion: "902"}}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "BOOKMARK", "object": bookmark})
		} else if requests == 2 {
			if r.URL.Query().Get("resourceVersion") != "902" {
				t.Errorf("watch did not resume after bookmark")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "ERROR", "object": metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonExpired, Code: 410}})
		} else {
			if r.URL.Query().Get("watch") != "" || r.URL.Query().Get("continue") != "" {
				t.Errorf("expired watch did not restart a fresh list")
			}
			writeList(t, w, "1000", "", demoService("new", corev1.ProtocolTCP, 8080))
		}
	})
	var output bytes.Buffer
	d.output = &output
	version, err := d.watch(context.Background(), "900")
	if err != nil || version != "902" {
		t.Fatalf("version %q err %v", version, err)
	}
	if strings.Count(output.String(), "\n") != 2 {
		t.Fatal("add event was not reconciled")
	}
	version, err = d.watch(context.Background(), version)
	if err != nil || version != "" {
		t.Fatalf("expired version %q err %v", version, err)
	}
	version, err = d.list(context.Background())
	if err != nil || version != "1000" {
		t.Fatalf("relist version %q err %v", version, err)
	}
}

func TestDeletedServiceDoesNotMutateCPRa(t *testing.T) {
	d := newKubeDiscovery(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := demoService("gone", corev1.ProtocolTCP, 8080)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "DELETED", "object": s})
	})
	d.printOnly = false // no CPRa client: accidental writes would panic
	var diagnostic bytes.Buffer
	d.diagnostics = &diagnostic
	if _, err := d.watch(context.Background(), "40"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diagnostic.String(), "retained for operator review") {
		t.Fatal("deletion was not reported")
	}
}

func TestUpsertUsesAbsencePreconditionAndPatchPreservesControls(t *testing.T) {
	d := testDiscovery()
	s := demoService("api", corev1.ProtocolTCP, 8080)
	monitors, _, err := d.desired(&s)
	if err != nil {
		t.Fatal(err)
	}
	desired := monitors[1]
	var current *api.Monitor
	creates, patches := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if current == nil {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"status":404}`)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPost:
			creates++
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("create lacked absence condition")
			}
			current = new(api.Monitor)
			if err := json.NewDecoder(r.Body).Decode(current); err != nil {
				t.Error(err)
			}
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(current)
		case http.MethodPatch:
			patches++
			if r.Header.Get("If-Match") != `"v1"` {
				t.Error("patch lacked exact read version")
			}
			var patch map[string]map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
				t.Error(err)
			}
			if len(patch) != 1 || len(patch["spec"]) != 1 || patch["spec"]["check"] == nil {
				t.Errorf("patch touched fields outside check: %#v", patch)
			}
			if err := json.Unmarshal(patch["spec"]["check"], &current.Spec.Check); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(current)
		default:
			t.Errorf("unexpected %s", r.Method)
			w.WriteHeader(405)
		}
	}))
	defer server.Close()
	d.client, err = cpra.New(cpra.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.upsert(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if err := d.upsert(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	current.Spec.Enabled = api.Pointer(false)
	desired.Spec.Check.Interval = "120s"
	if err := d.upsert(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || patches != 1 || current.Spec.Enabled == nil || *current.Spec.Enabled {
		t.Fatal("replay duplicated creation or update changed disabled state")
	}
	(*current.Metadata.Labels)[ownerLabel] = "someone-else"
	if err := d.upsert(context.Background(), desired); err == nil {
		t.Fatal("foreign monitor overwritten")
	}
}

func TestConflictIsReturnedWithoutFetchingAndOverwritingNewerVersion(t *testing.T) {
	d := testDiscovery()
	s := demoService("api", corev1.ProtocolTCP, 8080)
	monitors, _, err := d.desired(&s)
	if err != nil {
		t.Fatal(err)
	}
	wanted := monitors[0]
	old := wanted
	old.Spec.Check.Interval = "120s"
	reads, writes := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			reads++
			w.Header().Set("ETag", `"v1"`)
			_ = json.NewEncoder(w).Encode(old)
			return
		}
		writes++
		w.WriteHeader(412)
		_, _ = io.WriteString(w, `{"status":412}`)
	}))
	defer server.Close()
	d.client, err = cpra.New(cpra.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.upsert(context.Background(), wanted); !errors.Is(err, cpra.ErrConflict) {
		t.Fatalf("err=%v", err)
	}
	if reads != 1 || writes != 1 {
		t.Fatalf("reads=%d writes=%d", reads, writes)
	}
}

func TestDemoRunsWithoutExternalAccounts(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if err := runDemo(context.Background(), &output, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "5 monitors") || !strings.Contains(diagnostics.String(), "UDP") {
		t.Fatalf("output %s diagnostics %s", output.String(), diagnostics.String())
	}
}
