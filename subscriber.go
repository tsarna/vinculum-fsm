package fsm

import (
	"context"

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

// OnEvent implements bus.Subscriber. It maps the incoming topic to an event
// definition and enqueues the event for processing.
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

	// Match topic to an event definition.
	eventName, topicParams := inst.matchTopic(topic)
	if eventName == "" {
		// No event matches this topic -- enqueue as unmatched so only
		// on_event fires (not an accidental EventByName lookup).
		inst.EnqueueEvent(Event{
			Ctx:       ctx,
			Name:      topic,
			Value:     eventValue,
			Fields:    fields,
			Topic:     topic,
			unmatched: true,
		})
		return nil
	}

	inst.EnqueueEvent(Event{
		Ctx:         ctx,
		Name:        eventName,
		Value:       eventValue,
		Fields:      fields,
		Topic:       topic,
		TopicParams: topicParams,
	})
	return nil
}

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
