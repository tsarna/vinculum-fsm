package fsm

import "github.com/zclconf/go-cty/cty"

// HookContext carries event and transition information into hook and guard
// callbacks. Fields are set according to which hook is executing -- see the
// per-field documentation for availability.
type HookContext struct {
	// Event is the event name that triggered this transition.
	// Available in all hooks except on_init.
	Event string

	// EventValue is the message payload from send() or OnEvent.
	// Null for reactive events and on_init.
	EventValue cty.Value

	// EventFields is the optional string-keyed metadata from send()/OnEvent.
	// Nil when not provided.
	EventFields map[string]string

	// OldState is the state before the transition.
	// Available in on_exit, transition action, on_entry, on_change.
	OldState string

	// NewState is the state after the transition.
	// Available in transition action, on_entry, on_change.
	NewState string

	// Topic is the raw topic string from OnEvent. Empty for reactive events.
	Topic string

	// TopicParams contains named captures from MQTT pattern matching.
	// Nil when no topic pattern or no captures.
	TopicParams map[string]string

	// Fsm is the state machine instance as a cty capsule value.
	// Available in all hooks.
	Fsm cty.Value

	// Error is the error message, available only in on_error hooks.
	Error string

	// Hook is the name of the hook that produced an error, available only in on_error.
	Hook string

	// UserData is an opaque field for use by hook implementations. The config
	// handler uses it to cache the built HCL eval context across multiple hook
	// invocations within the same transition, avoiding redundant construction.
	UserData interface{}
}
