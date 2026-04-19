package fsm

import (
	"context"
	"testing"
)

func TestLifecycle_OnInitFires(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{
		Name:   "idle",
		OnInit: rec.record("idle:on_init"),
	})
	d.AddState(&StateDef{Name: "active"})
	d.AddEvent(&EventDef{
		Name:        "go",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "active"}},
	})

	inst := startInstance(t, "test", d)
	inst.Stop()

	calls := rec.getCalls()
	if len(calls) != 1 || calls[0] != "idle:on_init" {
		t.Fatalf("expected on_init to fire, got: %v", calls)
	}
}

func TestLifecycle_OnInitOnlyOnInitialState(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{
		Name:   "idle",
		OnInit: rec.record("idle:on_init"),
	})
	d.AddState(&StateDef{
		Name:   "active",
		OnInit: rec.record("active:on_init"), // should NOT fire
	})
	d.AddEvent(&EventDef{
		Name:        "go",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "active"}},
	})

	inst := startInstance(t, "test", d)
	inst.EnqueueEvent(Event{Name: "go"})
	inst.Stop()

	calls := rec.getCalls()
	for _, c := range calls {
		if c == "active:on_init" {
			t.Fatal("on_init should not fire on non-initial state")
		}
	}
}

func TestLifecycle_ShutdownEvent(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("running")
	d.AddState(&StateDef{Name: "running"})
	d.AddState(&StateDef{
		Name:    "stopped",
		OnEntry: rec.record("stopped:on_entry"),
	})
	d.ShutdownEvent = "shutdown"
	d.AddEvent(&EventDef{
		Name: "shutdown",
		Transitions: []*TransitionDef{
			{FromState: "*", ToState: "stopped"},
		},
	})

	inst := startInstance(t, "test", d)
	inst.Stop() // triggers shutdown event

	if got := inst.CurrentState(); got != "stopped" {
		t.Fatalf("expected 'stopped' after shutdown, got %q", got)
	}

	calls := rec.getCalls()
	found := false
	for _, c := range calls {
		if c == "stopped:on_entry" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected stopped:on_entry to fire during shutdown")
	}
}

func TestLifecycle_ShutdownEventPriority(t *testing.T) {
	// Fill the event queue, then stop. The shutdown event should be processed
	// before the remaining queued events are discarded.
	d := NewDefinition("running")
	d.AddState(&StateDef{Name: "running"})
	d.AddState(&StateDef{Name: "other"})
	d.AddState(&StateDef{Name: "stopped"})
	d.ShutdownEvent = "shutdown"
	d.QueueSize = 128

	d.AddEvent(&EventDef{
		Name: "noop",
		Transitions: []*TransitionDef{
			{FromState: "running", ToState: "other"},
			{FromState: "other", ToState: "running"},
		},
	})
	d.AddEvent(&EventDef{
		Name: "shutdown",
		Transitions: []*TransitionDef{
			{FromState: "*", ToState: "stopped"},
		},
	})

	inst := NewInstance("test", d)
	NewFsmCapsule(inst)
	inst.Start(context.Background())

	// Flood the queue.
	for i := 0; i < 50; i++ {
		inst.EnqueueEvent(Event{Name: "noop"})
	}

	inst.Stop()

	if got := inst.CurrentState(); got != "stopped" {
		t.Fatalf("expected 'stopped' (shutdown should take priority), got %q", got)
	}
}

func TestLifecycle_StopWithoutShutdownEvent(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name:        "go",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "idle"}},
	})

	inst := startInstance(t, "test", d)
	// Stop without a shutdown_event configured -- should exit cleanly.
	if err := inst.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}
