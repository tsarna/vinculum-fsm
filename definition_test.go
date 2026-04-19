package fsm

import (
	"strings"
	"testing"
)

func TestDefinition_Validate_MinimalValid(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.AddEvent(&EventDef{
		Name: "activate",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "active"},
		},
	})

	if err := d.Validate(); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestDefinition_Validate_NoStates(t *testing.T) {
	d := NewDefinition("idle")
	d.AddEvent(&EventDef{
		Name:        "x",
		Transitions: []*TransitionDef{{FromState: "a", ToState: "b"}},
	})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one state") {
		t.Fatalf("expected 'at least one state' error, got: %v", err)
	}
}

func TestDefinition_Validate_InitialStateNotDeclared(t *testing.T) {
	d := NewDefinition("missing")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name:        "x",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "idle"}},
	})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "initial state") {
		t.Fatalf("expected initial state error, got: %v", err)
	}
}

func TestDefinition_Validate_NoEvents(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one event") {
		t.Fatalf("expected 'at least one event' error, got: %v", err)
	}
}

func TestDefinition_Validate_EmptyTransitions(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{Name: "x"})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "at least one transition") {
		t.Fatalf("expected transition error, got: %v", err)
	}
}

func TestDefinition_Validate_UndeclaredToState(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name: "x",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "missing"},
		},
	})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "to-state \"missing\" is not declared") {
		t.Fatalf("expected undeclared to-state error, got: %v", err)
	}
}

func TestDefinition_Validate_UndeclaredFromState(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name: "x",
		Transitions: []*TransitionDef{
			{FromState: "missing", ToState: "idle"},
		},
	})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "from-state \"missing\" is not declared") {
		t.Fatalf("expected undeclared from-state error, got: %v", err)
	}
}

func TestDefinition_Validate_WildcardFromStateAllowed(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name: "reset",
		Transitions: []*TransitionDef{
			{FromState: "*", ToState: "idle"},
		},
	})

	if err := d.Validate(); err != nil {
		t.Fatalf("wildcard from-state should be valid, got: %v", err)
	}
}

func TestDefinition_Validate_WildcardToStateRejected(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name: "x",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "*"},
		},
	})

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("expected wildcard to-state error, got: %v", err)
	}
}

func TestDefinition_Validate_ShutdownEventMissing(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name:        "x",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "idle"}},
	})
	d.ShutdownEvent = "missing"

	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "shutdown_event") {
		t.Fatalf("expected shutdown_event error, got: %v", err)
	}
}

func TestDefinition_DuplicateState(t *testing.T) {
	d := NewDefinition("idle")
	if err := d.AddState(&StateDef{Name: "idle"}); err != nil {
		t.Fatalf("first add should succeed: %v", err)
	}
	if err := d.AddState(&StateDef{Name: "idle"}); err == nil {
		t.Fatal("expected duplicate state error")
	}
}

func TestDefinition_DuplicateEvent(t *testing.T) {
	d := NewDefinition("idle")
	evt := &EventDef{
		Name:        "x",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "idle"}},
	}
	if err := d.AddEvent(evt); err != nil {
		t.Fatalf("first add should succeed: %v", err)
	}
	if err := d.AddEvent(evt); err == nil {
		t.Fatal("expected duplicate event error")
	}
}

func TestDefinition_EventByName(t *testing.T) {
	d := NewDefinition("idle")
	evt := &EventDef{Name: "foo"}
	d.AddEvent(evt)

	if got := d.EventByName("foo"); got != evt {
		t.Fatal("expected to find event 'foo'")
	}
	if got := d.EventByName("bar"); got != nil {
		t.Fatal("expected nil for missing event")
	}
}

func TestDefinition_DefaultQueueSize(t *testing.T) {
	d := NewDefinition("idle")
	if d.QueueSize != 64 {
		t.Fatalf("expected default queue size 64, got %d", d.QueueSize)
	}
}
