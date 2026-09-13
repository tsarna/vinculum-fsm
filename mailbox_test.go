package fsm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zclconf/go-cty/cty"
)

// blockingFSM is a machine whose every event parks in its transition action
// until the test lets it go, so the mailbox behind it is provably still full
// when a depth is read. entered receives once per event that reaches the hook,
// and must be buffered: once the gate opens, the events behind the first run
// with nobody left reading it, and an unbuffered send there would park the
// event loop that Stop is waiting for.
func blockingFSM(entered chan<- struct{}, release <-chan struct{}) *Definition {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name: "work",
		Transitions: []*TransitionDef{{
			FromState: "idle",
			ToState:   "idle",
			Action: func(context.Context, *HookContext) error {
				entered <- struct{}{}
				<-release
				return nil
			},
		}},
	})
	return d
}

// rawPending is the count QueueDepth reports, before its clamp. Assertions that
// expect zero read both: this, so a count driven negative fails them instead of
// reading as a clean zero, and QueueDepth, so an accessor that never reads zero
// cannot pass on the strength of the count behind it.
func rawPending(inst *Instance) int64 { return inst.pending.Load() }

// QueueDepth counts the event in its hooks as well as the ones waiting; Length
// counts only the ones waiting.
func TestQueueDepthCountsTheMailboxAndTheEventInFlight(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	defer close(release)

	inst := startInstance(t, "worker", blockingFSM(entered, release))

	if got := rawPending(inst); got != 0 {
		t.Fatalf("an idle machine carries nothing; got a count of %d", got)
	}
	if got := inst.QueueDepth(); got != 0 {
		t.Fatalf("an idle machine reports nothing; QueueDepth is %d", got)
	}

	const events = 3
	for i := 0; i < events; i++ {
		if err := inst.EnqueueEvent(Event{Name: "work"}); err != nil {
			t.Fatalf("event %d was refused: %v", i, err)
		}
	}

	<-entered // the first event is inside its hook; the other two are waiting

	if got := inst.QueueDepth(); got != events {
		t.Fatalf("the machine is carrying %d events; got depth %d", events, got)
	}

	// The event being processed is the difference between the two accessors,
	// and it is the one a depth of zero would let a shutdown walk away from.
	length, err := inst.Length(context.Background())
	if err != nil {
		t.Fatalf("Length() error: %v", err)
	}
	if length != events-1 {
		t.Fatalf("length reports what is waiting, so %d; got %d", events-1, length)
	}
}

// Zero has to mean idle, or a shutdown waiting on this would never proceed.
func TestQueueDepthReturnsToZeroWhenTheMachineIsIdle(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})

	inst := startInstance(t, "worker", blockingFSM(entered, release))

	const events = 3
	for i := 0; i < events; i++ {
		inst.EnqueueEvent(Event{Name: "work"})
	}
	<-entered
	close(release)

	waitFor(t, func() bool { return rawPending(inst) == 0 },
		"the count never came back to zero after every event had run")
	if got := inst.QueueDepth(); got != 0 {
		t.Fatalf("an idle machine must report zero, or a shutdown waiting on it never proceeds; QueueDepth is %d", got)
	}

	if got, _ := inst.Count(context.Background()); got != events {
		t.Fatalf("expected %d transitions, got %d", events, got)
	}
}

// Start's init event is counted like any other: one while on_init runs, zero
// once Start returns, because runQueued drops it before releasing Start. Start
// is bounded so that an init event not dispatched as init fails here by name
// when this test runs alone. The order of the drop and the release is not
// pinned: getting it wrong reads one high for an instant, the safe direction.
func TestTheInitEventIsCountedUntilStartReturns(t *testing.T) {
	var countDuringInit int64

	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})

	inst := NewInstance("worker", d)

	// Wired after NewInstance because the hook reads the instance, which does
	// not exist while the definition is being built. Start's handshake orders
	// the write against the read below: the hook runs, then initCh closes, then
	// Start returns.
	d.States["idle"].OnInit = func(context.Context, *HookContext) error {
		countDuringInit = rawPending(inst)
		return nil
	}

	NewFsmCapsule(inst)
	started := make(chan error, 1)
	go func() { started <- inst.Start(context.Background()) }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start never returned, so the init event was not run as one")
	}
	t.Cleanup(func() { inst.Stop() })

	if countDuringInit != 1 {
		t.Fatalf("on_init runs with its own event in flight, so the count is 1; got %d", countDuringInit)
	}
	if got := rawPending(inst); got != 0 {
		t.Fatalf("a machine Start has returned from carries nothing; got a count of %d", got)
	}
	if got := inst.QueueDepth(); got != 0 {
		t.Fatalf("a machine Start has returned from reports nothing; QueueDepth is %d", got)
	}
}

// Counted before the send, not after. A producer blocked on a full mailbox is
// work the process has taken on, and a count raised only once the send
// completes reads one short for as long as the producer stays blocked — and,
// whenever the loop wins the race to a freshly sent event, reads zero from a
// busy machine. A full one-slot mailbox makes the first of those deterministic:
// the third producer below cannot get onto the channel, so the only way it is
// in the count is if it was counted first.
func TestAProducerBlockedOnAFullMailboxIsCounted(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseGate()

	d := blockingFSM(entered, release)
	d.QueueSize = 1
	inst := startInstance(t, "worker", d)

	inst.EnqueueEvent(Event{Name: "work"}) // taken by the loop and parked in its hook
	<-entered
	inst.EnqueueEvent(Event{Name: "work"}) // fills the one slot

	accepted := make(chan error, 1)
	go func() { accepted <- inst.EnqueueEvent(Event{Name: "work"}) }() // nowhere to go: blocks

	waitFor(t, func() bool { return rawPending(inst) == 3 },
		"a producer blocked on a full mailbox is not in the count")

	// And it genuinely is blocked rather than queued: the slot is still taken
	// by the second event, which cannot move until the first one's hook returns.
	if length, _ := inst.Length(context.Background()); length != 1 {
		t.Fatalf("the mailbox should hold exactly the one event that fits; length %d", length)
	}

	// Let it through before the test ends. Stopping the machine while the
	// producer is still blocked would close the mailbox under a live send — a
	// data race of its own (tsarna/vinculum-fsm#22), and not this test's
	// subject.
	releaseGate()
	if err := <-accepted; err != nil {
		t.Fatalf("the blocked producer's event was refused once there was room for it: %v", err)
	}
}

// A hook that sends onto its own machine is running on the only goroutine that
// empties the mailbox, so it cannot wait for room: once the mailbox is full the
// send fails instead of parking the loop for good. The events that fit are
// still taken, the transition completes, and nothing is left on the count. A
// restore from a hook is a send like any other.
func TestAHookCannotWaitForRoomOnItsOwnMailbox(t *testing.T) {
	type outcome struct {
		sends   []error
		restore error
	}
	done := make(chan outcome, 1)

	snap := cty.ObjectVal(map[string]cty.Value{
		"_type": cty.StringVal("fsm"),
		"state": cty.StringVal("busy"),
	})

	var inst *Instance // assigned before any event can reach the hook
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "busy"})
	d.QueueSize = 2
	d.AddEvent(&EventDef{
		Name: "burst",
		Transitions: []*TransitionDef{{
			FromState: "idle",
			ToState:   "busy",
			Action: func(ctx context.Context, _ *HookContext) error {
				var o outcome
				for i := 0; i < 3; i++ {
					o.sends = append(o.sends, inst.EnqueueEvent(Event{Ctx: ctx, Name: "noop"}))
				}
				_, o.restore = inst.Set(ctx, []cty.Value{snap})
				done <- o
				return nil
			},
		}},
	})
	inst = startInstance(t, "worker", d)

	if err := inst.EnqueueEvent(Event{Name: "burst"}); err != nil {
		t.Fatalf("burst was refused: %v", err)
	}

	var o outcome
	select {
	case o = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hook never returned: it is waiting for room only its own machine can make")
	}

	for i, err := range o.sends[:2] {
		if err != nil {
			t.Fatalf("send %d fits in the mailbox and should be taken; got %v", i, err)
		}
	}
	if !errors.Is(o.sends[2], ErrMailboxFull) {
		t.Fatalf("the third send has no room and cannot wait for it; got %v", o.sends[2])
	}
	if !errors.Is(o.restore, ErrMailboxFull) {
		t.Fatalf("a restore from the hook has no room either; got %v", o.restore)
	}

	waitFor(t, func() bool { return rawPending(inst) == 0 },
		"the events that were taken never finished, or a refused one stayed on the count")
	if got, _ := inst.Count(context.Background()); got != 1 {
		t.Fatalf("the burst transition should have completed exactly once; got %d transitions", got)
	}
}

// A guard runs on the event loop like any other hook, so it cannot wait for
// room on its own machine either. The refusal names the machine, which is the
// only thing that tells an action which of several machines rejected its event.
func TestAGuardCannotWaitForRoomOnItsOwnMailbox(t *testing.T) {
	done := make(chan []error, 1)

	var inst *Instance // assigned before any event can reach the guard
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "done"})
	d.QueueSize = 1
	d.AddEvent(&EventDef{
		Name: "check",
		Transitions: []*TransitionDef{{
			FromState: "idle",
			ToState:   "done",
			Guard: func(ctx context.Context, _ *HookContext) (bool, error) {
				var errs []error
				for i := 0; i < 2; i++ {
					errs = append(errs, inst.EnqueueEvent(Event{Ctx: ctx, Name: "noop"}))
				}
				done <- errs
				return true, nil
			},
		}},
	})
	inst = startInstance(t, "worker", d)

	if err := inst.EnqueueEvent(Event{Name: "check"}); err != nil {
		t.Fatalf("check was refused: %v", err)
	}

	var errs []error
	select {
	case errs = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the guard never returned: it is waiting for room only its own machine can make")
	}

	if errs[0] != nil {
		t.Fatalf("the first send fits in the mailbox and should be taken; got %v", errs[0])
	}
	if !errors.Is(errs[1], ErrMailboxFull) {
		t.Fatalf("the second send has no room and cannot wait for it; got %v", errs[1])
	}
	if !contains(errs[1].Error(), `fsm "worker"`) {
		t.Fatalf("a refusal names the machine that refused; got %q", errs[1].Error())
	}
}

// The mark is narrower than the context carrying it, which can outlive the hook
// it was made for. Kept past the end of its event, that context is an ordinary
// producer again and waits for room like one — the loop has moved on, and will
// make the room.
func TestAHookContextKeptPastItsEventWaitsForRoom(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseGate()

	kept := make(chan context.Context, 1)
	d := blockingFSM(entered, release)
	d.QueueSize = 1
	d.AddEvent(&EventDef{
		Name: "grab",
		Transitions: []*TransitionDef{{
			FromState: "idle",
			ToState:   "idle",
			Action: func(ctx context.Context, _ *HookContext) error {
				kept <- ctx
				return nil
			},
		}},
	})
	inst := startInstance(t, "worker", d)

	inst.EnqueueEvent(Event{Name: "grab"})
	ctx := <-kept
	inst.EnqueueEvent(Event{Name: "work"}) // parked in its hook, so grab's event is over
	<-entered
	inst.EnqueueEvent(Event{Name: "work"}) // fills the one slot

	assertWaitsForRoom(t, inst, ctx, releaseGate)
}

// A hook waiting on another machine's full mailbox is ordinary backpressure:
// that machine's loop is free to make room, so the mark does not apply there.
func TestAHookWaitsForRoomOnAnotherMachine(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseGate()

	target := startInstance(t, "target", func() *Definition {
		d := blockingFSM(entered, release)
		d.QueueSize = 1
		return d
	}())
	target.EnqueueEvent(Event{Name: "work"}) // parked in its hook
	<-entered
	target.EnqueueEvent(Event{Name: "work"}) // fills the one slot

	hookCtx := make(chan context.Context, 1)
	hold := make(chan struct{})
	defer close(hold)
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{
		Name: "go",
		Transitions: []*TransitionDef{{
			FromState: "idle",
			ToState:   "idle",
			Action: func(ctx context.Context, _ *HookContext) error {
				hookCtx <- ctx
				<-hold // keep the hook, and so its mark, live while the offer is made
				return nil
			},
		}},
	})
	source := startInstance(t, "source", d)
	source.EnqueueEvent(Event{Name: "go"})

	assertWaitsForRoom(t, target, <-hookCtx, releaseGate)
}

// assertWaitsForRoom offers an event under ctx to inst, whose one-slot mailbox
// the caller has filled behind an event parked in its hook, and fails unless
// the offer blocks rather than being refused. It then opens the gate, and the
// offer must go through.
func assertWaitsForRoom(t *testing.T, inst *Instance, ctx context.Context, releaseGate func()) {
	t.Helper()

	accepted := make(chan error, 1)
	go func() { accepted <- inst.EnqueueEvent(Event{Ctx: ctx, Name: "work"}) }()

	waitFor(t, func() bool { return rawPending(inst) == 3 },
		"the offer never reached the machine")
	select {
	case err := <-accepted:
		t.Fatalf("the offer should wait for room; it returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseGate()
	if err := <-accepted; err != nil {
		t.Fatalf("the offer was refused once there was room for it: %v", err)
	}
}

// A restore goes through the mailbox like any other event, so it has to come
// off the count like one. If it did not, a single `set(fsm.x, snapshot)` would
// leave the machine reporting a backlog for the rest of its life, and the
// shutdown would spend its whole quiesce budget waiting on nothing.
//
// The state is the barrier, and it only moves once: it says the restore ran.
// The count is then allowed to catch up, since the drop follows the apply.
func TestARestoreComesOffTheCount(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "other"})
	inst := startInstance(t, "worker", d)

	snap := cty.ObjectVal(map[string]cty.Value{
		"_type": cty.StringVal("fsm"),
		"state": cty.StringVal("other"),
	})
	if _, err := inst.Set(context.Background(), []cty.Value{snap}); err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	waitFor(t, func() bool { return inst.CurrentState() == "other" }, "the restore never ran")
	waitFor(t, func() bool { return rawPending(inst) == 0 },
		"the restore ran and never came off the count")
}

// Nothing a caller spells can make an event look like one the instance made for
// itself. An unmatched topic becomes the event name verbatim, so a message from
// outside the process chooses its own name; the instance's own init and restore
// events are recognised by a field no caller can set, and those names are
// delivered to on_event like any other.
//
// Both names arrive by both routes. on_init must have run exactly once — at
// Start — and on_event must have seen all four. Dispatching by name shows up as
// a second on_init or a missing delivery, or, where it reaches the init
// handshake or a restore with no snapshot behind it, as a crash of the test
// binary.
func TestSentinelNamesAreDeliveredLikeAnyOther(t *testing.T) {
	var mu sync.Mutex
	inits := 0
	var delivered []string

	d := NewDefinition("idle")
	d.AddState(&StateDef{
		Name: "idle",
		OnInit: func(context.Context, *HookContext) error {
			mu.Lock()
			defer mu.Unlock()
			inits++
			return nil
		},
		OnEvent: func(_ context.Context, h *HookContext) error {
			mu.Lock()
			defer mu.Unlock()
			delivered = append(delivered, h.Event)
			return nil
		},
	})
	inst := startInstance(t, "worker", d)

	for _, name := range []string{initEventName, restoreEventName} {
		if err := inst.OnEvent(context.Background(), name, "payload", nil); err != nil {
			t.Fatalf("%q through OnEvent: %v", name, err)
		}
		if err := inst.EnqueueEvent(Event{Name: name}); err != nil {
			t.Fatalf("%q through EnqueueEvent was refused: %v", name, err)
		}
	}

	waitFor(t, func() bool { return rawPending(inst) == 0 },
		"the events were never finished")

	mu.Lock()
	defer mu.Unlock()
	if inits != 1 {
		t.Fatalf("on_init runs once, at Start; it ran %d times", inits)
	}
	if len(delivered) != 4 {
		t.Fatalf("each name should reach on_event by both routes, so 4 deliveries; got %d: %q",
			len(delivered), delivered)
	}
}

// A shutdown_event jumps the queue and ends the loop, so events still on the
// mailbox are abandoned rather than run, and Stop must leave the count at zero,
// not at the number of events it abandoned.
//
// Stop is started while the first event is still parked, and the gate opens
// only once the shutdown event is waiting on its channel. That is what makes
// the abandonment certain rather than likely: the loop's priority check after
// the first event finds the shutdown event already there, so the four behind it
// cannot run however the scheduler falls.
func TestStopDropsTheCountForWhatItAbandoned(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})

	d := blockingFSM(entered, release)
	d.AddState(&StateDef{Name: "stopped"})
	d.AddEvent(&EventDef{
		Name:        "halt",
		Transitions: []*TransitionDef{{FromState: "*", ToState: "stopped"}},
	})
	d.ShutdownEvent = "halt"

	inst := startInstance(t, "worker", d)

	for i := 0; i < 5; i++ {
		inst.EnqueueEvent(Event{Name: "work"})
	}
	<-entered

	stopErr := make(chan error, 1)
	go func() { stopErr <- inst.Stop() }()
	waitFor(t, func() bool { return len(inst.shutdownCh) == 1 },
		"Stop never delivered the shutdown event")

	close(release)
	if err := <-stopErr; err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if got, _ := inst.Count(context.Background()); got != 2 {
		t.Fatalf("only the event in flight and the shutdown event should have run, so 2 "+
			"transitions; got %d, so the backlog was not abandoned", got)
	}
	if got, depth := rawPending(inst), inst.QueueDepth(); got != 0 || depth != 0 {
		t.Fatalf("a stopped machine is carrying nothing; got a count of %d, QueueDepth %d", got, depth)
	}
	if got := inst.CurrentState(); got != "stopped" {
		t.Fatalf("the shutdown event should have run; state is %q", got)
	}
}
