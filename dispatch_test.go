package fsm

import (
	"context"
	"sync"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

// hookRecorder tracks hook calls in order for test assertions.
type hookRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *hookRecorder) record(name string) HookFunc {
	return func(_ context.Context, _ *HookContext) error {
		r.mu.Lock()
		r.calls = append(r.calls, name)
		r.mu.Unlock()
		return nil
	}
}

func (r *hookRecorder) getCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	dst := make([]string, len(r.calls))
	copy(dst, r.calls)
	return dst
}

// doorFSM creates a simple door state machine for testing.
func doorFSM(rec *hookRecorder) *Definition {
	d := NewDefinition("closed")
	d.AddState(&StateDef{
		Name:    "closed",
		OnInit:  rec.record("closed:on_init"),
		OnEntry: rec.record("closed:on_entry"),
		OnExit:  rec.record("closed:on_exit"),
	})
	d.AddState(&StateDef{
		Name:    "open",
		OnEntry: rec.record("open:on_entry"),
		OnExit:  rec.record("open:on_exit"),
	})
	d.AddState(&StateDef{
		Name:    "locked",
		OnEntry: rec.record("locked:on_entry"),
		OnExit:  rec.record("locked:on_exit"),
	})
	d.OnChange = rec.record("on_change")

	d.AddEvent(&EventDef{
		Name: "open",
		Transitions: []*TransitionDef{
			{FromState: "closed", ToState: "open", Action: rec.record("open:action")},
		},
	})
	d.AddEvent(&EventDef{
		Name: "close",
		Transitions: []*TransitionDef{
			{FromState: "open", ToState: "closed"},
		},
	})
	d.AddEvent(&EventDef{
		Name: "lock",
		Transitions: []*TransitionDef{
			{FromState: "closed", ToState: "locked"},
		},
	})
	d.AddEvent(&EventDef{
		Name: "unlock",
		Transitions: []*TransitionDef{
			{FromState: "locked", ToState: "closed"},
		},
	})

	return d
}

func startInstance(t *testing.T, name string, def *Definition) *Instance {
	t.Helper()
	inst := NewInstance(name, def)
	NewFsmCapsule(inst)
	if err := inst.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { inst.Stop() })
	return inst
}

func TestDispatch_BasicTransition(t *testing.T) {
	rec := &hookRecorder{}
	def := doorFSM(rec)
	inst := startInstance(t, "door", def)

	inst.EnqueueEvent(Event{Name: "open"})
	inst.Stop()

	if got := inst.CurrentState(); got != "open" {
		t.Fatalf("expected state 'open', got %q", got)
	}

	count, _ := inst.Count(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 transition, got %d", count)
	}
}

func TestDispatch_HookExecutionOrder(t *testing.T) {
	rec := &hookRecorder{}
	def := doorFSM(rec)
	inst := startInstance(t, "door", def)

	inst.EnqueueEvent(Event{Name: "open"})
	inst.Stop()

	calls := rec.getCalls()
	// Expected: on_init, then transition hooks
	expected := []string{
		"closed:on_init",
		"closed:on_exit",
		"open:action",
		"open:on_entry",
		"on_change",
	}
	if len(calls) != len(expected) {
		t.Fatalf("expected %d calls, got %d: %v", len(expected), len(calls), calls)
	}
	for i, want := range expected {
		if calls[i] != want {
			t.Fatalf("call[%d]: expected %q, got %q (all: %v)", i, want, calls[i], calls)
		}
	}
}

func TestDispatch_MultipleTransitions(t *testing.T) {
	rec := &hookRecorder{}
	def := doorFSM(rec)
	inst := startInstance(t, "door", def)

	inst.EnqueueEvent(Event{Name: "open"})
	inst.EnqueueEvent(Event{Name: "close"})
	inst.EnqueueEvent(Event{Name: "lock"})
	inst.Stop()

	if got := inst.CurrentState(); got != "locked" {
		t.Fatalf("expected 'locked', got %q", got)
	}
	count, _ := inst.Count(context.Background())
	if count != 3 {
		t.Fatalf("expected 3 transitions, got %d", count)
	}
}

func TestDispatch_EventIgnoredWhenNoMatch(t *testing.T) {
	rec := &hookRecorder{}
	def := doorFSM(rec)
	inst := startInstance(t, "door", def)

	// "open" from "closed" works, but "lock" from "open" doesn't match
	inst.EnqueueEvent(Event{Name: "open"})
	inst.EnqueueEvent(Event{Name: "lock"}) // no transition from "open" to "locked"
	inst.Stop()

	if got := inst.CurrentState(); got != "open" {
		t.Fatalf("expected 'open', got %q", got)
	}
	count, _ := inst.Count(context.Background())
	if count != 1 {
		t.Fatalf("expected 1 transition (lock should be ignored), got %d", count)
	}
}

func TestDispatch_SelfTransition(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("active")
	d.AddState(&StateDef{
		Name:    "active",
		OnEntry: rec.record("active:on_entry"),
		OnExit:  rec.record("active:on_exit"),
	})
	d.OnChange = rec.record("on_change")
	d.AddEvent(&EventDef{
		Name: "heartbeat",
		Transitions: []*TransitionDef{
			{FromState: "active", ToState: "active", Action: rec.record("heartbeat:action")},
		},
	})

	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "heartbeat"})
	inst.Stop()

	if got := inst.CurrentState(); got != "active" {
		t.Fatalf("expected 'active', got %q", got)
	}
	count, _ := inst.Count(context.Background())
	if count != 1 {
		t.Fatalf("self-transition should count, got %d", count)
	}

	// Full hook sequence should fire for self-transition.
	calls := rec.getCalls()
	expected := []string{"active:on_exit", "heartbeat:action", "active:on_entry", "on_change"}
	if len(calls) != len(expected) {
		t.Fatalf("expected %d calls, got %d: %v", len(expected), len(calls), calls)
	}
	for i, want := range expected {
		if calls[i] != want {
			t.Fatalf("call[%d]: expected %q, got %q", i, want, calls[i])
		}
	}
}

func TestDispatch_WildcardTransition(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.AddState(&StateDef{Name: "emergency"})
	d.AddEvent(&EventDef{
		Name: "start",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "active"},
		},
	})
	d.AddEvent(&EventDef{
		Name: "emergency",
		Transitions: []*TransitionDef{
			{FromState: "*", ToState: "emergency", Action: rec.record("emergency:action")},
		},
	})

	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "start"})
	inst.EnqueueEvent(Event{Name: "emergency"})
	inst.Stop()

	if got := inst.CurrentState(); got != "emergency" {
		t.Fatalf("expected 'emergency', got %q", got)
	}
}

func TestDispatch_WildcardAfterExplicit(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("error")
	d.AddState(&StateDef{Name: "error"})
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.AddEvent(&EventDef{
		Name: "reset",
		Transitions: []*TransitionDef{
			// Wildcard: from anything else
			{FromState: "*", ToState: "idle", Action: rec.record("reset:wildcard")},
			// Explicit: from "error", use specific action
			{FromState: "error", ToState: "idle", Action: rec.record("reset:from_error")},
		},
	})

	// From "error" state: explicit should match first.
	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "reset"})
	inst.Stop()

	calls := rec.getCalls()
	found := false
	for _, c := range calls {
		if c == "reset:from_error" {
			found = true
		}
		if c == "reset:wildcard" {
			t.Fatal("wildcard should not fire when explicit matches")
		}
	}
	if !found {
		t.Fatal("expected explicit reset action to fire")
	}
}

func TestDispatch_GuardFiltering(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "hot"})
	d.AddState(&StateDef{Name: "cold"})
	d.AddEvent(&EventDef{
		Name: "check",
		Transitions: []*TransitionDef{
			{
				FromState: "idle", ToState: "hot",
				Guard: func(_ context.Context, _ *HookContext) (bool, error) {
					return false, nil // never matches
				},
			},
			{
				FromState: "idle", ToState: "cold",
				Guard: func(_ context.Context, _ *HookContext) (bool, error) {
					return true, nil // always matches
				},
			},
		},
	})

	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "check"})
	inst.Stop()

	if got := inst.CurrentState(); got != "cold" {
		t.Fatalf("expected 'cold' (second guard matches), got %q", got)
	}
}

func TestDispatch_OnEvent_NoTransition(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{
		Name:    "idle",
		OnEvent: rec.record("idle:on_event"),
	})
	d.AddEvent(&EventDef{
		Name: "activate",
		Transitions: []*TransitionDef{
			{FromState: "active", ToState: "idle"}, // won't match from "idle"
		},
	})

	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "activate"})
	inst.Stop()

	calls := rec.getCalls()
	found := false
	for _, c := range calls {
		if c == "idle:on_event" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected on_event to fire when no transition matches")
	}
}

func TestDispatch_WatchableNotification(t *testing.T) {
	d := NewDefinition("closed")
	d.AddState(&StateDef{Name: "closed"})
	d.AddState(&StateDef{Name: "open"})
	d.AddEvent(&EventDef{
		Name: "open",
		Transitions: []*TransitionDef{
			{FromState: "closed", ToState: "open"},
		},
	})

	inst := startInstance(t, "door", d)

	var mu sync.Mutex
	var oldVal, newVal cty.Value
	notified := make(chan struct{}, 1)

	watcher := &testWatcher{
		fn: func(_ context.Context, old, new_ cty.Value) {
			mu.Lock()
			oldVal = old
			newVal = new_
			mu.Unlock()
			select {
			case notified <- struct{}{}:
			default:
			}
		},
	}
	inst.Watch(watcher)
	defer inst.Unwatch(watcher)

	inst.EnqueueEvent(Event{Name: "open"})

	// Wait for notification via stop (ensures event is processed).
	inst.Stop()

	mu.Lock()
	defer mu.Unlock()
	if oldVal.AsString() != "closed" {
		t.Fatalf("expected old='closed', got %q", oldVal.AsString())
	}
	if newVal.AsString() != "open" {
		t.Fatalf("expected new='open', got %q", newVal.AsString())
	}
}

type testWatcher struct {
	fn func(ctx context.Context, old, new_ cty.Value)
}

func (w *testWatcher) OnChange(ctx context.Context, old, new_ cty.Value) {
	w.fn(ctx, old, new_)
}

func TestDispatch_HookErrorRoutedToOnError(t *testing.T) {
	var mu sync.Mutex
	var capturedError, capturedHook string

	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.OnError = func(_ context.Context, hookCtx *HookContext) {
		mu.Lock()
		capturedError = hookCtx.Error
		capturedHook = hookCtx.Hook
		mu.Unlock()
	}
	d.AddEvent(&EventDef{
		Name: "go",
		Transitions: []*TransitionDef{
			{
				FromState: "idle", ToState: "active",
				Action: func(_ context.Context, _ *HookContext) error {
					return context.DeadlineExceeded
				},
			},
		},
	})

	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "go"})
	inst.Stop()

	// Transition should still complete despite hook error.
	if got := inst.CurrentState(); got != "active" {
		t.Fatalf("expected 'active', got %q", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if capturedError != "context deadline exceeded" {
		t.Fatalf("expected error message, got %q", capturedError)
	}
	if capturedHook != "action" {
		t.Fatalf("expected hook 'action', got %q", capturedHook)
	}
}
