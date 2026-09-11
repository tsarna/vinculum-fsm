package fsm

import (
	"context"

	"github.com/zclconf/go-cty/cty"
)

// Event represents an incoming event to be processed by the FSM instance.
// Events are enqueued and processed sequentially by the event goroutine.
type Event struct {
	// Ctx carries the originating caller's context across the event-queue
	// boundary. Values (trace span, auth, etc.) are preserved into hook
	// evaluation; cancellation is NOT — the event loop applies
	// context.WithoutCancel before invoking hooks, so an upstream
	// cancellation (e.g. HTTP request completion) cannot interrupt FSM
	// state transitions mid-flight.
	//
	// May be nil; treated as context.Background() by the event loop.
	Ctx context.Context

	// Name is the event name, used to match against EventDef declarations.
	Name string

	// Value is the event payload (from send() message argument or OnEvent).
	// cty.NilVal for reactive events.
	Value cty.Value

	// Fields is the optional string metadata from send()/OnEvent.
	Fields map[string]string

	// Topic is the raw topic string from OnEvent. Empty for reactive events.
	Topic string

	// TopicParams contains named captures from MQTT pattern matching.
	TopicParams map[string]string

	// unmatched is true when the event came from OnEvent but no event
	// definition matched the topic. In this case, processEvent should
	// only fire on_event on the current state, not look up by name.
	unmatched bool

	// restore carries validated snapshot data for a restore event.
	restore *restoreData

	// internal marks an event the instance put on its own mailbox, and is what
	// the event loop dispatches those on. Name cannot be trusted for that: it
	// is spelled by whoever sends the event, and an unmatched topic becomes the
	// name verbatim — so a message from outside the process could otherwise
	// arrive dressed as an init or a restore.
	internal internalKind
}

// internalKind marks the events an instance puts on its own mailbox; everything
// else, including the shutdown event Stop sends on its priority channel, is
// external.
type internalKind uint8

const (
	external        internalKind = iota // the zero value: every event but init and restore
	internalInit                        // Start's synthetic on_init event
	internalRestore                     // a validated snapshot restore
)

// restoreData holds the validated state and storage for a restore.
type restoreData struct {
	state   string
	storage map[string]cty.Value
}

// restoreEventName is the Name a restore event carries. It is only a label: the
// loop dispatches on Event.internal, so a caller may send this name like any
// other.
const restoreEventName = "\x00__restore__"
