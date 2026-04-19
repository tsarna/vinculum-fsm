package fsm

import (
	"context"
	"testing"
)

func TestSubscriber_LiteralTopicMatch(t *testing.T) {
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

	// OnEvent with topic matching the event name literally.
	err := inst.OnEvent(context.Background(), "open", "hello", nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "open" {
		t.Fatalf("expected 'open', got %q", got)
	}
}

func TestSubscriber_TopicPatternMatch(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "alerting"})
	d.AddEvent(&EventDef{
		Name:         "alert",
		TopicPattern: "sensors/+sensor/alert",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "alerting", Action: rec.record("alert:action")},
		},
	})

	inst := startInstance(t, "monitor", d)

	err := inst.OnEvent(context.Background(), "sensors/thermostat-3/alert", "high temp", nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "alerting" {
		t.Fatalf("expected 'alerting', got %q", got)
	}
}

func TestSubscriber_TopicPatternWithoutExtraction(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "alerting"})
	d.AddEvent(&EventDef{
		Name:         "alert",
		TopicPattern: "sensors/+/alert",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "alerting"},
		},
	})

	inst := startInstance(t, "monitor", d)

	err := inst.OnEvent(context.Background(), "sensors/thermostat-3/alert", "high temp", nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "alerting" {
		t.Fatalf("expected 'alerting', got %q", got)
	}
}

func TestSubscriber_TopicPatternNoMatch(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "alerting"})
	d.AddEvent(&EventDef{
		Name:         "alert",
		TopicPattern: "sensors/+sensor/alert",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "alerting"},
		},
	})

	inst := startInstance(t, "monitor", d)

	// Topic doesn't match pattern.
	err := inst.OnEvent(context.Background(), "devices/thermostat/status", "ok", nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "idle" {
		t.Fatalf("expected 'idle' (no match), got %q", got)
	}
}

func TestSubscriber_ReactiveOnlySkipsTopicMatch(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "hot"})
	d.AddEvent(&EventDef{
		Name:    "overheat",
		HasWhen: true, // reactive-only, should not match topic "overheat"
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "hot"},
		},
	})

	inst := startInstance(t, "test", d)

	// Topic "overheat" should NOT match because the event is reactive-only.
	err := inst.OnEvent(context.Background(), "overheat", nil, nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "idle" {
		t.Fatalf("expected 'idle' (reactive-only should not match topic), got %q", got)
	}
}

func TestSubscriber_CtyValuePassedThrough(t *testing.T) {
	var capturedValue string
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.AddEvent(&EventDef{
		Name: "go",
		Transitions: []*TransitionDef{
			{
				FromState: "idle", ToState: "active",
				Action: func(_ context.Context, hookCtx *HookContext) error {
					if hookCtx.EventValue.Type().FriendlyName() == "string" {
						capturedValue = hookCtx.EventValue.AsString()
					}
					return nil
				},
			},
		},
	})

	inst := startInstance(t, "test", d)
	err := inst.OnEvent(context.Background(), "go", "payload-data", nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	inst.Stop()

	if capturedValue != "payload-data" {
		t.Fatalf("expected 'payload-data', got %q", capturedValue)
	}
}

func TestSubscriber_FieldsPassedThrough(t *testing.T) {
	var capturedFields map[string]string
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.AddEvent(&EventDef{
		Name: "go",
		Transitions: []*TransitionDef{
			{
				FromState: "idle", ToState: "active",
				Action: func(_ context.Context, hookCtx *HookContext) error {
					capturedFields = hookCtx.EventFields
					return nil
				},
			},
		},
	})

	inst := startInstance(t, "test", d)
	fields := map[string]string{"user": "alice", "reason": "test"}
	err := inst.OnEvent(context.Background(), "go", "msg", fields)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	inst.Stop()

	if capturedFields["user"] != "alice" || capturedFields["reason"] != "test" {
		t.Fatalf("expected fields to pass through, got %v", capturedFields)
	}
}

func TestSubscriber_UnknownTopicFiresOnEvent(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{
		Name:    "idle",
		OnEvent: rec.record("idle:on_event"),
	})
	d.AddEvent(&EventDef{
		Name: "known",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "idle"},
		},
	})

	inst := startInstance(t, "test", d)
	err := inst.OnEvent(context.Background(), "unknown_topic", nil, nil)
	if err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}
	inst.Stop()

	calls := rec.getCalls()
	found := false
	for _, c := range calls {
		if c == "idle:on_event" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected on_event to fire for unknown topic")
	}
}
