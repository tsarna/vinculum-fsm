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
	inst.eventCh <- Event{Name: initEventName}

	// Wait for on_init to complete before returning, so callers know the
	// FSM is fully initialized.
	<-inst.initCh

	return nil
}

// Stop shuts down the FSM gracefully. If a shutdown_event is configured, it
// is injected via the priority channel and processed before remaining events.
// Then the event queue is closed and the processing goroutine exits.
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
	return nil
}

// EnqueueEvent adds an event to the processing queue. If the queue is full,
// the call blocks until space is available. Returns false if the instance
// has been stopped (the event is silently dropped).
func (inst *Instance) EnqueueEvent(evt Event) bool {
	if inst.eventCh == nil || inst.stopped.Load() {
		return false
	}
	// Use a recover guard: even with the stopped check above, a concurrent
	// Stop() could close the channel between our check and the send.
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

// initEventName is a sentinel used internally to trigger on_init processing.
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
			eventCtx := process(evt)
			switch evt.Name {
			case initEventName:
				inst.processInit(eventCtx)
			case restoreEventName:
				inst.applyRestore(eventCtx, evt.restore.state, evt.restore.storage)
			default:
				inst.processDelivered(eventCtx, evt)
			}
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

// processInit fires the initial state's on_init hook.
func (inst *Instance) processInit(ctx context.Context) {
	defer close(inst.initCh)

	initialState := inst.definition.States[inst.currentState]
	if initialState != nil && initialState.OnInit != nil {
		hookCtx := &HookContext{
			Fsm: inst.capsuleVal,
		}
		inst.callHook(ctx, hookCtx, "on_init", initialState.OnInit)
	}
}
