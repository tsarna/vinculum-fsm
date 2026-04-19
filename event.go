package fsm

import "github.com/zclconf/go-cty/cty"

// Event represents an incoming event to be processed by the FSM instance.
// Events are enqueued and processed sequentially by the event goroutine.
type Event struct {
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
}
