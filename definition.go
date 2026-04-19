package fsm

import (
	"context"
	"fmt"
)

// HookFunc is a callback invoked during state transitions. The return value
// is ignored by the dispatch engine (hooks are fire-and-forget for side
// effects); a non-nil error is routed to the on_error handler if configured.
type HookFunc func(ctx context.Context, hookCtx *HookContext) error

// GuardFunc evaluates whether a transition should fire. It returns true to
// allow the transition or false to skip it and try the next candidate.
type GuardFunc func(ctx context.Context, hookCtx *HookContext) (bool, error)

// ErrorHookFunc is called when a hook produces an error.
type ErrorHookFunc func(ctx context.Context, hookCtx *HookContext)

// StateDef defines a named state and its optional lifecycle hooks.
type StateDef struct {
	Name    string
	OnInit  HookFunc // called once at startup, only on the initial state
	OnEntry HookFunc // called on each transition into this state
	OnExit  HookFunc // called on each transition out of this state
	OnEvent HookFunc // called when an event is received but no transition matches
}

// TransitionDef defines a single transition within an event.
type TransitionDef struct {
	FromState string    // source state name, or "*" for wildcard
	ToState   string    // destination state name (must be explicit)
	Guard     GuardFunc // optional: transition only if true
	Action    HookFunc  // optional: executed during the transition
}

// EventDef defines a named event with its transitions and optional topic pattern.
type EventDef struct {
	Name         string
	TopicPattern string           // MQTT-style pattern for topic matching; empty for literal/reactive
	Transitions  []*TransitionDef // evaluated in declaration order
	HasWhen      bool             // true if this event has a reactive "when" expression
}

// Definition is the complete specification of a state machine: its states,
// events, transitions, and machine-level hooks.
type Definition struct {
	States        map[string]*StateDef
	Events        []*EventDef // ordered: declaration order matters for matching
	InitialState  string
	OnChange      HookFunc
	OnError       ErrorHookFunc
	ShutdownEvent string
	QueueSize     int // event channel buffer size; 0 means use default (64)
}

const defaultQueueSize = 64

// NewDefinition creates a new Definition with the given initial state.
func NewDefinition(initialState string) *Definition {
	return &Definition{
		States:       make(map[string]*StateDef),
		InitialState: initialState,
		QueueSize:    defaultQueueSize,
	}
}

// AddState adds a state to the definition. Returns an error if the state
// name is already defined.
func (d *Definition) AddState(s *StateDef) error {
	if _, exists := d.States[s.Name]; exists {
		return fmt.Errorf("duplicate state: %q", s.Name)
	}
	d.States[s.Name] = s
	return nil
}

// AddEvent adds an event to the definition. Returns an error if an event
// with the same name already exists.
func (d *Definition) AddEvent(e *EventDef) error {
	for _, existing := range d.Events {
		if existing.Name == e.Name {
			return fmt.Errorf("duplicate event: %q", e.Name)
		}
	}
	d.Events = append(d.Events, e)
	return nil
}

// Validate checks the definition for consistency. It verifies that:
//   - The initial state is declared
//   - At least one state is declared
//   - At least one event is declared
//   - Each event has at least one transition
//   - All transition from/to states reference declared states (except "*" as from)
//   - "*" is not used as a to-state
//   - The shutdown event (if set) references a declared event
func (d *Definition) Validate() error {
	if len(d.States) == 0 {
		return fmt.Errorf("at least one state must be declared")
	}
	if _, ok := d.States[d.InitialState]; !ok {
		return fmt.Errorf("initial state %q is not declared", d.InitialState)
	}
	if len(d.Events) == 0 {
		return fmt.Errorf("at least one event must be declared")
	}

	for _, evt := range d.Events {
		if len(evt.Transitions) == 0 {
			return fmt.Errorf("event %q must have at least one transition", evt.Name)
		}
		for _, tr := range evt.Transitions {
			if tr.ToState == "*" {
				return fmt.Errorf("event %q: wildcard \"*\" is not valid as a to-state", evt.Name)
			}
			if _, ok := d.States[tr.ToState]; !ok {
				return fmt.Errorf("event %q: to-state %q is not declared", evt.Name, tr.ToState)
			}
			if tr.FromState != "*" {
				if _, ok := d.States[tr.FromState]; !ok {
					return fmt.Errorf("event %q: from-state %q is not declared", evt.Name, tr.FromState)
				}
			}
		}
	}

	if d.ShutdownEvent != "" {
		found := false
		for _, evt := range d.Events {
			if evt.Name == d.ShutdownEvent {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("shutdown_event %q does not reference a declared event", d.ShutdownEvent)
		}
	}

	return nil
}

// EventByName returns the EventDef with the given name, or nil if not found.
func (d *Definition) EventByName(name string) *EventDef {
	for _, evt := range d.Events {
		if evt.Name == name {
			return evt
		}
	}
	return nil
}
