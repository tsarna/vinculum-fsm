package fsm

import (
	"context"
	"sync"
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
	inst.restoreCh = make(chan *restoreRequest, 1)

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
// Stop is idempotent -- calling it multiple times is safe.
func (inst *Instance) Stop() error {
	if !inst.stopped.CompareAndSwap(false, true) {
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
	if inst.stopped.Load() {
		return false
	}
	// Use a recover guard: even with the stopped check above, a concurrent
	// Stop() could close the channel between our check and the send.
	defer func() { recover() }()
	inst.eventCh <- evt
	return true
}

// initEventName is a sentinel used internally to trigger on_init processing.
const initEventName = "\x00__init__"

// eventLoop is the single goroutine that processes events sequentially.
// The shutdown channel has priority: after each event we check it before
// pulling the next regular event.
func (inst *Instance) eventLoop(ctx context.Context) {
	inst.eventGoroutineID.Store(goroutineID())
	defer inst.eventGoroutineID.Store(0)

	for {
		select {
		case evt, ok := <-inst.eventCh:
			if !ok {
				// Channel closed -- shutdown without shutdown_event.
				return
			}
			if evt.Name == initEventName {
				inst.processInit(ctx)
			} else {
				inst.processEvent(ctx, evt)
			}
			// After processing, give priority channels precedence
			// before pulling the next regular event.
			if inst.checkPriority(ctx) {
				return
			}

		case req := <-inst.restoreCh:
			inst.applyRestore(ctx, req.state, req.storage)
			req.result <- nil

		case evt := <-inst.shutdownCh:
			inst.processEvent(ctx, evt)
			return
		}
	}
}

// checkPriority handles priority channels between regular events.
// Returns true if the event loop should exit (shutdown).
func (inst *Instance) checkPriority(ctx context.Context) bool {
	// Check restore first, then shutdown.
	select {
	case req := <-inst.restoreCh:
		inst.applyRestore(ctx, req.state, req.storage)
		req.result <- nil
	default:
	}

	select {
	case evt := <-inst.shutdownCh:
		inst.processEvent(ctx, evt)
		return true
	default:
	}

	return false
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
