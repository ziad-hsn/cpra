package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/ziad-hsn/cpra/examples/sdk/internal/cprafixture"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func demoService(name string, protocol corev1.Protocol, port int32) corev1.Service {
	return corev1.Service{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "payments", UID: types.UID("demo-" + name), ResourceVersion: "41"},
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: "10.96.0.10",
			Ports: []corev1.ServicePort{{Name: "main", Protocol: protocol, Port: port, TargetPort: intstr.FromInt32(port)}}},
	}
}

func runDemo(ctx context.Context, output, diagnostics io.Writer) error {
	// These are wire fixtures. The real client-go and CPRa SDK HTTP clients run
	// unchanged, but this does not exercise a Kubernetes control plane or CPRa server.
	redis := demoService("redis", corev1.ProtocolTCP, 6379)
	dns := demoService("resolver", corev1.ProtocolUDP, 53)
	added := demoService("checkout", corev1.ProtocolTCP, 8080)
	added.ResourceVersion = "42"
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/payments/services" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("watch") == "true" {
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "ADDED", "object": added})
			return
		}
		items, next := []corev1.Service{redis}, "page-two"
		if r.URL.Query().Get("continue") == "page-two" {
			items, next = []corev1.Service{dns}, ""
		}
		_ = json.NewEncoder(w).Encode(corev1.ServiceList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceList"},
			ListMeta: metav1.ListMeta{ResourceVersion: "41", Continue: next}, Items: items})
	}))
	defer kube.Close()
	server := cprafixture.New()
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: cprafixture.Token, AllowInsecureHTTP: true})
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	core, err := typedcore.NewForConfig(&rest.Config{Host: kube.URL})
	if err != nil {
		return err
	}
	d := &discovery{services: core.Services("payments"), client: client, clusterID: "demo-cluster", namespace: "payments",
		domain: "cluster.local", pageSize: 1, output: output, diagnostics: diagnostics}
	fmt.Fprintln(output, "Demo: two list pages, one Service added through watch, then the same list replayed.")
	version, err := d.list(ctx)
	if err != nil {
		return err
	}
	if _, err := d.watch(ctx, version); err != nil {
		return err
	}
	if _, err := d.list(ctx); err != nil {
		return err
	}
	fmt.Fprintf(output, "Demo complete: %d monitors; replay created no duplicates. Evidence: local HTTP fixtures only.\n", len(server.Snapshot()))
	return nil
}
