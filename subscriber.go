package fsm

import (
	"context"
	"errors"

	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/topicmatch"
	"github.com/zclconf/go-cty/cty"
)

// Ensure Instance implements bus.Subscriber at compile time.
var _ bus.Subscriber = (*Instance)(nil)

// OnSubscribe implements bus.Subscriber.
func (inst *Instance) OnSubscribe(_ context.Context, _ string) error {
	return nil
}

// OnUnsubscribe implements bus.Subscriber.
func (inst *Instance) OnUnsubscribe(_ context.Context, _ string) error {
	return nil
}

// ErrInstanceStopped reports an event that was not accepted because the
// instance has been stopped. It is a refusal rather than a failure: nothing
// ran, and nothing will.
var ErrInstanceStopped = errors.New("fsm instance is stopped")

// OnEvent implements bus.Subscriber. It maps the incoming topic to an event
// definition and enqueues the event for processing.
//
// The event is queued, not handled — see DeliveryDisposition. What this returns
// therefore says only whether the instance took the event, which is why a
// stopped instance has to report ErrInstanceStopped rather than nil: a caller
// that acknowledged a broker delivery on the strength of a nil return would be
// acknowledging an event that was dropped on the floor.
func (inst *Instance) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	// Convert the message to a cty value.
	var eventValue cty.Value
	if ctyVal, ok := message.(cty.Value); ok {
		eventValue = ctyVal
	} else {
		var err error
		eventValue, err = go2cty2go.AnyToCty(message)
		if err != nil {
			eventValue = cty.NullVal(cty.DynamicPseudoType)
		}
	}

	evt := Event{
		Ctx:    ctx,
		Value:  eventValue,
		Fields: fields,
		Topic:  topic,
	}

	// Match topic to an event definition.
	if eventName, topicParams := inst.matchTopic(topic); eventName != "" {
		evt.Name = eventName
		evt.TopicParams = topicParams
	} else {
		// No event matches this topic -- enqueue as unmatched so only
		// on_event fires (not an accidental EventByName lookup).
		evt.Name = topic
		evt.unmatched = true
	}

	if !inst.EnqueueEvent(evt) {
		return ErrInstanceStopped
	}

	return nil
}

// DeliveryDisposition reports that enqueueing an event is not handling it.
//
// OnEvent above returns as soon as the event is on the queue; the hooks run
// later, on the instance's own goroutine. So a caller settling a broker
// delivery on that return would acknowledge the message before any of the
// machine's work had happened — and the deferral is internal, with no
// downstream subscriber to settle it instead. The event loop settles, once the
// hooks for that event have run.
func (inst *Instance) DeliveryDisposition() bus.Disposition { return bus.Deferred }

// PassThrough implements bus.Subscriber.
func (inst *Instance) PassThrough(_ bus.EventBusMessage) error {
	return nil
}

// matchTopic maps an incoming topic string to an event name using the event
// definitions' topic patterns. Returns the event name and any extracted
// topic parameters, or empty string if no event matches.
//
// Matching rules:
//   - Events with a topic pattern are checked in declaration order; first match wins.
//   - Events with neither topic nor when match when topic equals the event name (literal).
//   - Events with when but no topic are reactive-only and skip topic matching.
func (inst *Instance) matchTopic(topic string) (string, map[string]string) {
	for _, evt := range inst.definition.Events {
		if evt.TopicPattern != "" {
			// Pattern match using topicmatch (MQTT-style wildcards).
			params := topicmatch.Exec(evt.TopicPattern, topic)
			if params != nil {
				return evt.Name, params
			}
			continue
		}

		if evt.HasWhen {
			// Reactive-only event: does not participate in topic matching.
			continue
		}

		// Literal name match.
		if evt.Name == topic {
			return evt.Name, nil
		}
	}

	return "", nil
}
