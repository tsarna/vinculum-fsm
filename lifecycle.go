package fsm

import (
	"context"
	"fmt"
	"sync"

	bus "github.com/tsarna/vinculum-bus"
)

// Start launches the event processing goroutine, starts reactive expressions
// (wired by the config handler), and fires the initial state's on_init hook.
// The provided context is used for the processing goroutine's lifetime.
func (inst *Instance) Start(ctx context.Context) error {
	queueSize := inst.definition.QueueSize
	if queueSize <= 0 {
		queueSize = defaultQueueSize
	}

	inst.eventCh = make(chan Event, queueSize)
	inst.shutdownCh = make(chan Event, 1)

	var wg sync.WaitGroup
	inst.wg = &wg

	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.eventLoop(ctx)
	}()

	// Fire on_init for the initial state by enqueueing a synthetic init event.
	// This runs through the event goroutine to ensure serialization.
	inst.initCh = make(chan struct{})
	inst.pending.Add(1)
	inst.eventCh <- Event{Name: initEventName, internal: internalInit}

	// Wait for on_init to complete before returning, so callers know the
	// FSM is fully initialized.
	<-inst.initCh

	return nil
}

// Stop shuts down the FSM gracefully. If a shutdown_event is configured, it is
// injected via the priority channel, the loop ends once it has been processed,
// and whatever is still queued is abandoned — at most one queued event can run
// ahead of it. Without one, the loop runs out what is queued and then exits.
// Stop is idempotent -- calling it multiple times is safe, as is calling it
// on an instance that was never Started (e.g. config validation that builds
// then tears down without starting): the channels are nil until Start, so
// there is nothing to shut down.
func (inst *Instance) Stop() error {
	if !inst.stopped.CompareAndSwap(false, true) {
		return nil
	}
	// Never started -- Start is what creates eventCh/shutdownCh. Sending the
	// shutdown event or closing eventCh below would otherwise block (nil send)
	// or panic (close of nil channel).
	if inst.eventCh == nil {
		return nil
	}
	if inst.definition.ShutdownEvent != "" {
		inst.shutdownCh <- Event{Name: inst.definition.ShutdownEvent}
	}
	close(inst.eventCh)
	if inst.wg != nil {
		inst.wg.Wait()
	}
	// Discard what the loop did not reach — a shutdown event ends it early —
	// and take each off the count. Not a reset: a producer between its raise
	// and its refused-send drop would leave the count at -1.
	for range inst.eventCh {
		inst.pending.Add(-1)
	}
	return nil
}

// EnqueueEvent adds an event to the processing queue. If the queue is full,
// the call blocks until space is available. Returns false if the instance
// has been stopped (the event is silently dropped).
func (inst *Instance) EnqueueEvent(evt Event) bool {
	if inst.eventCh == nil || inst.stopped.Load() {
		return false
	}

	// Counted before the send rather than after, so an event is never
	// unaccounted for: a blocked producer is work the process has taken on,
	// and between the send and an increment after it there would be a moment
	// where the mailbox holds an event nothing reports.
	inst.pending.Add(1)
	if !inst.send(evt) {
		inst.pending.Add(-1)
		return false
	}
	return true
}

// send puts evt on the mailbox, reporting false if Stop closed it underneath.
// The recover only contains tsarna/vinculum-fsm#22; the close is still a race.
func (inst *Instance) send(evt Event) bool {
	defer func() { recover() }()
	inst.eventCh <- evt
	return true
}

// processDelivered runs one event to completion and then settles whatever
// delivery carried it here.
//
// This is the instance's settle point, and it exists because the deferral
// declared by DefersDelivery is internal: nothing upstream may settle on
// OnEvent's return, and there is no downstream subscriber to settle instead.
// By the time processEvent returns, every hook for this event has run.
//
// It settles as handled, and that is honest rather than optimistic. A hook that
// fails is routed to the machine's own on_error handler and does not propagate
// (see callHook), so "the hooks ran" is the outcome the FSM has to report; a
// configuration that wants a hook failure to reach the broker says so with
// ack = "manual" and settles from the hook itself. Events with no settler on
// their context — most of them — cost one nil check here.
func (inst *Instance) processDelivered(ctx context.Context, evt Event) {
	// A panic must not leave the delivery unsettled, or the broker would hold
	// it until its lease lapsed with nothing anywhere saying why. Telling the
	// broker first changes what it hears, not what the process then does.
	defer func() {
		if r := recover(); r != nil {
			bus.SettleRefused(ctx, fmt.Sprintf("panic in fsm %s handling %s: %v", inst.Name(), evt.Name, r))
			panic(r)
		}
	}()

	inst.processEvent(ctx, evt)

	bus.SettleOnReturn(ctx, nil, nil)
}

// initEventName is the Name Start gives its init event. It is only a label: the
// loop dispatches on Event.internal, so a caller may send this name like any
// other.
const initEventName = "\x00__init__"

// eventLoop is the single goroutine that processes events sequentially.
// The shutdown channel has priority: after each event we check it before
// pulling the next regular event.
//
// Each event is processed under a context derived from evt.Ctx (the caller's
// context captured at enqueue time). context.WithoutCancel is applied so an
// upstream cancellation (e.g. an HTTP request completing before its enqueued
// event is dequeued) cannot interrupt hook processing. Values from the
// caller's context — trace spans, auth, etc. — are preserved. Events with a
// nil Ctx fall back to the eventLoop's own ctx.
func (inst *Instance) eventLoop(ctx context.Context) {
	process := func(evt Event) context.Context {
		c := evt.Ctx
		if c == nil {
			c = ctx
		}
		return context.WithoutCancel(c)
	}
	for {
		select {
		case evt, ok := <-inst.eventCh:
			if !ok {
				// Channel closed -- shutdown without shutdown_event.
				return
			}
			inst.runQueued(process(evt), evt)
			// After processing, give the shutdown channel priority
			// before pulling the next regular event.
			select {
			case evt := <-inst.shutdownCh:
				inst.processEvent(process(evt), evt)
				return
			default:
			}

		case evt := <-inst.shutdownCh:
			inst.processEvent(process(evt), evt)
			return
		}
	}
}

// runQueued dispatches one event taken off the mailbox, then drops it from the
// pending count once its hooks have run.
func (inst *Instance) runQueued(ctx context.Context, evt Event) {
	switch evt.internal {
	case internalInit:
		inst.processInit(ctx)
	case internalRestore:
		inst.applyRestore(ctx, evt.restore.state, evt.restore.storage)
	default:
		inst.processDelivered(ctx, evt)
	}

	inst.pending.Add(-1)

	// Start is waiting on the init event, and is released only after the drop
	// above, so it never wakes to a count still holding the event it waited for.
	if evt.internal == internalInit {
		close(inst.initCh)
	}
}

// processInit fires the initial state's on_init hook. runQueued releases Start
// once it returns.
func (inst *Instance) processInit(ctx context.Context) {
	initialState := inst.definition.States[inst.currentState]
	if initialState != nil && initialState.OnInit != nil {
		hookCtx := &HookContext{
			Fsm: inst.capsuleVal,
		}
		inst.callHook(ctx, hookCtx, "on_init", initialState.OnInit)
	}
}
