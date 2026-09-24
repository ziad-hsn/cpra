package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
)

const ownerLabel = "examples.cpra.io/owner"
const ownerValue = "kubernetes-services"

type discovery struct {
	services                     typedcore.ServiceInterface
	client                       *cpra.Client
	clusterID, namespace, domain string
	output, diagnostics          io.Writer
	printOnly                    bool
	pageSize                     int64
}

// Identity uses the operator's stable cluster ID and the immutable Service UID.
// A deleted and recreated Service must not inherit the old monitor's incident.
func identity(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s", len(p), p)
	}
	return "k8s-" + hex.EncodeToString(h.Sum(nil))[:40]
}

func (d *discovery) desired(s *corev1.Service) ([]api.Monitor, []string, error) {
	if s.UID == "" || s.Name == "" || s.Namespace != d.namespace {
		return nil, nil, errors.New("Service has missing identity or unexpected namespace")
	}
	host := s.Name + "." + s.Namespace + ".svc." + d.domain
	labels := map[string]string{
		ownerLabel:                     ownerValue,
		"examples.cpra.io/cluster":     identity(d.clusterID),
		"examples.cpra.io/service-uid": string(s.UID),
		"examples.cpra.io/namespace":   s.Namespace,
		"examples.cpra.io/service":     s.Name,
	}
	makeMonitor := func(key, title, typ string, config any) (api.Monitor, error) {
		driver, err := api.Driver("check", typ, config)
		if err != nil {
			return api.Monitor{}, err
		}
		return api.Monitor{
			APIVersion: api.APIVersion, Kind: "Monitor",
			Metadata: api.Metadata{
				ID:   identity(d.clusterID, string(s.UID), key),
				Name: api.Pointer(s.Namespace + "/" + s.Name + " " + title), Labels: &labels,
			},
			Spec: api.MonitorSpec{Check: api.CheckSpec{Driver: driver, Interval: "60s", Timeout: "5s"}},
		}, nil
	}
	dns, err := makeMonitor("dns", "DNS resolution", "dns", api.PulseDNSConfig{Host: &host})
	if err != nil {
		return nil, nil, err
	}
	monitors := []api.Monitor{dns}
	var notices []string
	for _, p := range s.Spec.Ports {
		if p.Protocol != "" && p.Protocol != corev1.ProtocolTCP {
			notices = append(notices, fmt.Sprintf("port %d/%s has DNS coverage only; protocol health needs an explicit check", p.Port, p.Protocol))
			continue
		}
		port := p.Port
		if s.Spec.ClusterIP == corev1.ClusterIPNone {
			// No Service proxy translates servicePort to targetPort for headless DNS.
			if p.TargetPort.StrVal != "" {
				notices = append(notices, fmt.Sprintf("headless port %d has named targetPort %q; DNS coverage only (EndpointSlice lookup is required for TCP)", p.Port, p.TargetPort.StrVal))
				continue
			}
			if p.TargetPort.IntVal != 0 {
				port = p.TargetPort.IntVal
			}
		}
		if port < 1 || port > 65535 {
			return nil, nil, fmt.Errorf("invalid TCP target port %d", port)
		}
		key := fmt.Sprintf("tcp:%d", p.Port)
		monitor, err := makeMonitor(key, fmt.Sprintf("TCP %d", port), "tcp", api.PulseTCPConfig{Host: &host, Port: api.Pointer(int64(port))})
		if err != nil {
			return nil, nil, err
		}
		monitors = append(monitors, monitor)
	}
	return monitors, notices, nil
}

func (d *discovery) reconcile(ctx context.Context, service *corev1.Service) error {
	monitors, notices, err := d.desired(service)
	if err != nil {
		return err
	}
	for _, n := range notices {
		fmt.Fprintf(d.diagnostics, "%s/%s: %s\n", service.Namespace, service.Name, n)
	}
	var failures []error
	for _, desired := range monitors {
		if d.printOnly {
			err = json.NewEncoder(d.output).Encode(desired)
		} else {
			err = d.upsert(ctx, desired)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("monitor %s: %w", desired.Metadata.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (d *discovery) upsert(ctx context.Context, desired api.Monitor) error {
	current, err := d.client.Monitors.Get(ctx, desired.Metadata.ID)
	if errors.Is(err, cpra.ErrNotFound) {
		_, err = d.client.Monitors.Create(ctx, desired)
		if err == nil {
			fmt.Fprintf(d.output, "created %s\n", desired.Metadata.ID)
		}
		return err
	}
	if err != nil {
		return err
	}
	got := current.Data
	if got.Metadata.ID != desired.Metadata.ID || got.Metadata.Labels == nil {
		return errors.New("existing monitor is not owned by this discovery service")
	}
	for _, key := range []string{ownerLabel, "examples.cpra.io/cluster", "examples.cpra.io/service-uid"} {
		if (*got.Metadata.Labels)[key] != (*desired.Metadata.Labels)[key] {
			return errors.New("existing monitor ownership differs; refusing to overwrite")
		}
	}
	// Newer servers can return unknown drivers for observation. Discovery must
	// not turn such an observation into an unsupported write.
	if err := api.ValidateDriver("check", got.Spec.Check.Driver); err != nil {
		return fmt.Errorf("existing check cannot be safely reconciled: %w", err)
	}
	oldCheck, err := json.Marshal(got.Spec.Check)
	if err != nil {
		return err
	}
	newCheck, err := json.Marshal(desired.Spec.Check)
	if err != nil {
		return err
	}
	if bytes.Equal(oldCheck, newCheck) {
		return nil
	}
	// Merge Patch recurses into objects. Explicit nulls remove obsolete fields
	// in this owned check without touching disable state or notification policy.
	checkPatch, err := replaceObjectPatch(oldCheck, newCheck)
	if err != nil {
		return err
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"check": checkPatch}})
	if err != nil {
		return err
	}
	version := current.ResourceVersion
	if version == "" {
		version = got.Metadata.ResourceVersion
	}
	_, err = d.client.Monitors.Patch(ctx, desired.Metadata.ID, version, api.MergePatch(patch))
	if err == nil {
		fmt.Fprintf(d.output, "updated %s\n", desired.Metadata.ID)
	}
	return err
}

// replaceObjectPatch produces a JSON Merge Patch that makes one object match
// its desired replacement. RawMessage preserves numeric values exactly.
func replaceObjectPatch(oldJSON, desiredJSON []byte) (map[string]json.RawMessage, error) {
	var old, desired map[string]json.RawMessage
	if err := json.Unmarshal(oldJSON, &old); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(desiredJSON, &desired); err != nil {
		return nil, err
	}
	patch := make(map[string]json.RawMessage, len(old)+len(desired))
	for key := range old {
		if _, exists := desired[key]; !exists {
			patch[key] = json.RawMessage("null")
		}
	}
	for key, value := range desired {
		before := bytes.TrimSpace(old[key])
		after := bytes.TrimSpace(value)
		if len(before) > 0 && before[0] == '{' && len(after) > 0 && after[0] == '{' {
			nested, err := replaceObjectPatch(before, after)
			if err != nil {
				return nil, err
			}
			value, err = json.Marshal(nested)
			if err != nil {
				return nil, err
			}
		}
		patch[key] = value
	}
	return patch, nil
}

// list reconciles one page at a time and retains no full namespace cache.
// A failed traversal never interprets missing items as deletions.
func (d *discovery) list(ctx context.Context) (string, error) {
	var cursor, version string
	seen := make(map[string]struct{})
	failures := 0
	for pages := 0; pages < 10000; pages++ {
		page, err := d.services.List(ctx, metav1.ListOptions{Limit: d.pageSize, Continue: cursor})
		if err != nil {
			return "", err
		}
		if version == "" {
			version = page.ResourceVersion
		}
		if version == "" || page.ResourceVersion != version {
			return "", errors.New("list pages lack a consistent resourceVersion")
		}
		for i := range page.Items {
			if err := d.reconcile(ctx, &page.Items[i]); err != nil {
				failures++
				fmt.Fprintf(d.diagnostics, "Service %s/%s failed: %v\n", d.namespace, page.Items[i].Name, err)
			}
		}
		cursor = page.Continue
		if cursor == "" {
			if failures > 0 {
				return "", fmt.Errorf("%d Services had monitor errors; no complete reconciliation", failures)
			}
			return version, nil
		}
		if _, ok := seen[cursor]; ok {
			return "", errors.New("Kubernetes repeated a list continuation token")
		}
		seen[cursor] = struct{}{}
	}
	return "", errors.New("namespace exceeded the 10000-page example limit")
}

// watch resumes after the last processed event. A 410 clears the cursor so Run
// relists. The Kubernetes resourceVersion is opaque; it is never compared numerically.
func (d *discovery) watch(ctx context.Context, version string) (string, error) {
	timeout := int64(60)
	w, err := d.services.Watch(ctx, metav1.ListOptions{ResourceVersion: version, AllowWatchBookmarks: true, TimeoutSeconds: &timeout})
	if err != nil {
		if kerrors.IsResourceExpired(err) || kerrors.IsGone(err) {
			return "", nil
		}
		return version, err
	}
	defer w.Stop()
	for {
		select {
		case <-ctx.Done():
			return version, ctx.Err()
		case event, ok := <-w.ResultChan():
			if !ok {
				return version, nil
			}
			if event.Type == watch.Error {
				err := kerrors.FromObject(event.Object)
				if kerrors.IsResourceExpired(err) || kerrors.IsGone(err) {
					return "", nil
				}
				return version, err
			}
			s, ok := event.Object.(*corev1.Service)
			if !ok {
				return version, errors.New("unexpected Kubernetes watch object")
			}
			if s.ResourceVersion == "" {
				return version, errors.New("watch event lacks resourceVersion")
			}
			switch event.Type {
			case watch.Added, watch.Modified:
				if err := d.reconcile(ctx, s); err != nil {
					// A fresh list visits the other Services even while this one fails.
					return "", err
				}
			case watch.Deleted:
				fmt.Fprintf(d.diagnostics, "Service %s/%s deleted (UID %s); existing monitors retained for operator review\n", s.Namespace, s.Name, s.UID)
			case watch.Bookmark:
			default:
				return version, errors.New("unexpected Kubernetes watch event")
			}
			version = s.ResourceVersion
		}
	}
}

func (d *discovery) run(ctx context.Context, once bool) error {
	return d.runWithIntervals(ctx, once, 5*time.Minute, 5*time.Second)
}

// The full resync also repairs changes made directly in CPRa when Kubernetes
// emits no Service event. Tests use shorter intervals with the same HTTP loop.
func (d *discovery) runWithIntervals(ctx context.Context, once bool, resync, reconnect time.Duration) error {
	if resync <= 0 || reconnect <= 0 {
		return errors.New("resync and reconnect intervals must be positive")
	}
	version := ""
	var nextResync time.Time
	for {
		var err error
		if version == "" || !time.Now().Before(nextResync) {
			version, err = d.list(ctx)
			if err == nil {
				nextResync = time.Now().Add(resync)
			}
		} else {
			watchCtx, cancel := context.WithDeadline(ctx, nextResync)
			version, err = d.watch(watchCtx, version)
			cancel()
			if !time.Now().Before(nextResync) {
				version = ""
				if errors.Is(err, context.DeadlineExceeded) {
					err = nil // The planned resync ended the watch.
				}
			}
		}
		if once {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if kerrors.IsUnauthorized(err) || kerrors.IsForbidden(err) {
			return err
		}
		if err != nil {
			fmt.Fprintf(d.diagnostics, "discovery will retry after %s: %v\n", reconnect, err)
		}
		// Bound reconnect and error traffic, including a server that closes watches immediately.
		timer := time.NewTimer(reconnect)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
