package alerts

import (
	"errors"
	"testing"
	"time"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

func newTestWorld() (*ecs.World, ecs.Entity) {
	w := ecs.NewWorld()
	world := &w
	statusMapper := ecs.NewMap1[components.CodeStatus](world)
	ent := statusMapper.NewEntity(&components.CodeStatus{
		Status: map[string]*components.ColorCodeStatus{
			"red":   {},
			"green": {},
		},
	})
	return world, ent
}

func TestDefaultPolicy_FirstDispatchAllowed(t *testing.T) {
	world, ent := newTestWorld()
	policy := NewDefaultPolicy(world, true)

	ok, reason := policy.ShouldDispatch(ent, "red", time.Now())
	if !ok {
		t.Fatalf("expected first dispatch to be allowed, got reason %q", reason)
	}
}

func TestDefaultPolicy_CooldownSuppresses(t *testing.T) {
	world, ent := newTestWorld()
	policy := NewDefaultPolicy(world, true)
	now := time.Now()

	// Simulate a prior enqueue that set NotBefore in the future.
	status := ecs.NewMap1[components.CodeStatus](world).Get(ent)
	status.Status["red"].NotBefore = now.Add(time.Minute)

	ok, _ := policy.ShouldDispatch(ent, "red", now)
	if ok {
		t.Fatal("expected dispatch to be suppressed during cooldown")
	}

	ok, _ = policy.ShouldDispatch(ent, "red", now.Add(2*time.Minute))
	if !ok {
		t.Fatal("expected dispatch to be allowed after cooldown elapsed")
	}
}

func TestDefaultPolicy_RecoveryBypass(t *testing.T) {
	world, ent := newTestWorld()
	policy := NewDefaultPolicy(world, true)
	now := time.Now()

	status := ecs.NewMap1[components.CodeStatus](world).Get(ent)
	status.Status["green"].NotBefore = now.Add(time.Minute)

	ok, _ := policy.ShouldDispatch(ent, "green", now)
	if !ok {
		t.Fatal("expected green recovery to bypass cooldown")
	}

	// With bypass disabled, green is suppressed like any other color.
	policyNoBypass := NewDefaultPolicy(world, false)
	ok, _ = policyNoBypass.ShouldDispatch(ent, "green", now)
	if ok {
		t.Fatal("expected green to be suppressed when recovery bypass is disabled")
	}
}

func TestManager_RequestOnEnqueuedOnResult(t *testing.T) {
	world, ent := newTestWorld()
	cooldown := time.Minute
	policy := NewDefaultPolicy(world, true)
	mgr := NewManager(world, policy, cooldown)

	now := time.Now()

	// First request: immediate.
	imm, _, _ := mgr.Request(ent, "red", now)
	if !imm {
		t.Fatal("expected first request to be immediate")
	}

	// Enqueue starts the cooldown.
	mgr.OnEnqueued(ent, "red", now)

	// Second request within cooldown: deferred.
	imm, notBefore, _ := mgr.Request(ent, "red", now.Add(time.Second))
	if imm {
		t.Fatal("expected second request to be deferred during cooldown")
	}
	if !notBefore.After(now.Add(time.Second)) {
		t.Fatalf("expected notBefore in the future, got %v", notBefore)
	}

	// After cooldown elapses: immediate again.
	imm, _, _ = mgr.Request(ent, "red", now.Add(2*time.Minute))
	if !imm {
		t.Fatal("expected request after cooldown to be immediate")
	}
}

func TestManager_OnResultFailureClearsCooldown(t *testing.T) {
	world, ent := newTestWorld()
	cooldown := time.Minute
	policy := NewDefaultPolicy(world, true)
	mgr := NewManager(world, policy, cooldown)

	now := time.Now()
	mgr.OnEnqueued(ent, "red", now)

	// Failure clears the cooldown so a retry is not suppressed.
	mgr.OnResult(ent, "red", errors.New("send failed"), now.Add(time.Second))

	status := ecs.NewMap1[components.CodeStatus](world).Get(ent)
	if !status.Status["red"].NotBefore.IsZero() {
		t.Fatal("expected NotBefore to be cleared after failure")
	}
	if status.Status["red"].ConsecutiveFailures != 1 {
		t.Fatalf("expected ConsecutiveFailures=1, got %d", status.Status["red"].ConsecutiveFailures)
	}
}

func TestManager_OnResultSuccessStampsStatus(t *testing.T) {
	world, ent := newTestWorld()
	policy := NewDefaultPolicy(world, true)
	mgr := NewManager(world, policy, time.Minute)

	now := time.Now()
	mgr.OnResult(ent, "red", nil, now)

	status := ecs.NewMap1[components.CodeStatus](world).Get(ent)
	if status.Status["red"].LastStatus != "success" {
		t.Fatalf("expected LastStatus=success, got %q", status.Status["red"].LastStatus)
	}
}
