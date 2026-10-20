//go:build !windows

package systems

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

func TestNotificationRefusedReceiverRecoversThroughOwnerRetry(t *testing.T) {
	// Reserve the actual destination without listening. This produces refusal
	// without closing and racing another test for an ephemeral port.
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	syscall.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "notification-receiver")
	t.Cleanup(func() { _ = file.Close() })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	address, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(address.(*syscall.SockaddrInet4).Port))
	world := ecs.NewWorld()
	ent := releaseMonitor(t, &world, 1, 1)
	ecs.NewMap1[components.CodeConfig](&world).Get(ent).Configs = map[string]*components.ColorCodeConfig{
		"red": {Dispatch: true, Notify: "webhook", EndpointIDs: []string{"recovering-receiver"}},
	}
	ecs.NewMap1[components.JobStorage](&world).Get(ent).CodeJobs = map[string][]jobs.Job{
		"red": {&jobs.CodeWebhookJob{URL: url, Method: http.MethodPost, Color: "red", Monitor: "release"}},
	}
	queue := &admissionQueue{}
	system := newIncidentSystem(t, &world, nil, queue)
	monitor := applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target unhealthy")})
	system.dispatchActions(time.Now().UTC())
	if len(queue.admitted) != 1 {
		t.Fatal("initial notification was not admitted exactly once", len(queue.admitted))
	}
	refused := queue.admitted[0].Execute()
	if effect := jobs.ClassifyEffect(refused.Err); effect.Outcome != jobs.EffectNotApplied || !effect.Retryable {
		t.Fatal("actual connection refusal lost its retryable no-effect classification", effect, refused.Err)
	}
	started, _ := system.store.Get(monitor.ID)
	first := incidentActions(started, "code", "red")
	if len(first) != 1 || first[0].State != persistence.Started || first[0].Deadline.IsZero() {
		t.Fatal("refused attempt lacks its committed start and deadline", first)
	}
	original := first[0]
	if !system.commitResults([]jobs.Result{refused, refused}) {
		t.Fatal("actual refusal did not commit idempotently", system.lastError)
	}
	retained, _ := monitorWithActionHistory(t, system.store, monitor.ID)
	failed := retained.Actions[original.ID]
	if failed.State != persistence.Failed || !failed.Retryable {
		t.Fatal("refusal was not retained as a confirmed retryable failure", failed)
	}
	current, _ := system.store.Get(monitor.ID)
	retries := incidentActions(current, "code", "red")
	if len(retries) != 1 || retries[0].Attempt != 2 || retries[0].State != persistence.Queued {
		t.Fatal("refusal did not admit exactly one successor", retries)
	}
	retry := retries[0]
	if retry.ID == original.ID || retry.DeliveryID != original.DeliveryID || retry.EndpointID != original.EndpointID ||
		!retry.QueuedAt.Equal(original.QueuedAt) || !retry.Deadline.Equal(original.Deadline) {
		t.Fatal("retry changed its logical delivery, destination or budget", original, retry)
	}
	if due, ok := system.actionDue[ent]; !ok || !due.Equal(retry.NotBefore) || !due.After(time.Now()) {
		t.Fatal("result processing did not schedule the future retry", due, retry.NotBefore)
	}
	system.dispatchActions(time.Now().UTC())
	if len(queue.admitted) != 1 {
		t.Fatal("retry dispatched before its retained deadline")
	}

	// The same reserved socket becomes available before the scheduled retry.
	if err := syscall.Listen(fd, 8); err != nil {
		t.Fatal(err)
	}
	listener, err := net.FileListener(file)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	var posts atomic.Int64
	observed := make(chan map[string]any, 2)
	receiver := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.Method != http.MethodPost {
			t.Error("unexpected notification method", r.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
			t.Error("invalid notification envelope", err)
		}
		select {
		case observed <- body:
		default:
			t.Error("receiver saw unexpected repeated delivery")
		}
		w.WriteHeader(http.StatusOK)
	}))
	_ = receiver.Listener.Close()
	receiver.Listener = listener
	receiver.Start()
	t.Cleanup(receiver.Close)
	limit := time.Now().Add(10 * time.Second)
	for len(queue.admitted) < 2 && time.Now().Before(limit) {
		system.dispatchActions(time.Now().UTC())
		time.Sleep(10 * time.Millisecond)
	}
	if len(queue.admitted) != 2 {
		t.Fatal("owner did not dispatch the scheduled retry", len(queue.admitted))
	}
	succeeded := queue.admitted[1].Execute()
	if succeeded.Err != nil || !system.commitResults([]jobs.Result{succeeded, succeeded}) {
		t.Fatal("receiver success was not committed", succeeded.Err, system.lastError)
	}
	select {
	case body := <-observed:
		if body["delivery_id"] != original.DeliveryID || body["action_id"] != retry.ID {
			t.Fatal("receiver saw a replacement logical delivery or wrong attempt", body)
		}
	default:
		t.Fatal("successful result has no target-side request")
	}
	for range 3 {
		applyIncidentPulse(t, system, ent, jobs.Result{Err: errors.New("target still unhealthy")})
		system.dispatchActions(time.Now().UTC())
	}
	retained, _ = monitorWithActionHistory(t, system.store, monitor.ID)
	final := incidentActions(retained, "code", "red")
	if len(final) != 2 || retained.Actions[original.ID].State != persistence.Failed ||
		retained.Actions[retry.ID].State != persistence.Succeeded || posts.Load() != 1 || len(queue.admitted) != 2 {
		t.Fatal("retry lost an outcome or repeated a delivered notification", final, posts.Load(), len(queue.admitted))
	}
	for _, action := range final {
		if action.DeliveryID != original.DeliveryID || !action.Deadline.Equal(original.Deadline) {
			t.Fatal("completion renewed the delivery identity or deadline", action)
		}
	}
}
