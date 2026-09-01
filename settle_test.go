package fsm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	bus "github.com/tsarna/vinculum-bus"
)

// recordingOps counts what reached "the broker", so a test can say *when* the
// settle happened rather than only that it did.
type recordingOps struct {
	mu      sync.Mutex
	acks    int
	nacks   int
	reasons []string
}

func (o *recordingOps) Ack(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.acks++
	return nil
}

func (o *recordingOps) Nack(_ context.Context, reason string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.nacks++
	o.reasons = append(o.reasons, reason)
	return nil
}

func (o *recordingOps) Keepalive(context.Context) (bool, error) { return false, nil }
func (o *recordingOps) Valid() (bool, string)                   { return true, "" }

func (o *recordingOps) counts() (acks, nacks int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.acks, o.nacks
}

func autoSettled(ops bus.SettleOps) context.Context {
	return bus.WithSettler(context.Background(), bus.NewSettler(ops, bus.AutoSettle()))
}

func gateFSM(entered chan<- struct{}, release <-chan struct{}) *Definition {
	d := NewDefinition("closed")
	d.AddState(&StateDef{Name: "closed"})
	d.AddState(&StateDef{
		Name: "open",
		OnEntry: func(context.Context, *HookContext) error {
			entered <- struct{}{}
			<-release
			return nil
		},
	})
	d.AddEvent(&EventDef{
		Name:        "open",
		Transitions: []*TransitionDef{{FromState: "closed", ToState: "open"}},
	})
	return d
}

// The whole reason an FSM declares itself deferring. OnEvent returns as soon as
// the event is queued; if that return were taken as the outcome, the broker
// would be told the machine had handled the message before a single hook ran.
func TestSettleWaitsForTheHooksToRun(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	releaseOnce := sync.Once{}
	releaseGate := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseGate()

	inst := startInstance(t, "door", gateFSM(entered, release))

	ops := &recordingOps{}
	if err := inst.OnEvent(autoSettled(ops), "open", "hello", nil); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	<-entered
	if acks, nacks := ops.counts(); acks != 0 || nacks != 0 {
		t.Fatalf("the on_entry hook is still running; settled %d/%d already", acks, nacks)
	}

	releaseGate()

	waitFor(t, func() bool { acks, _ := ops.counts(); return acks == 1 },
		"the acknowledgement should follow the hooks")

	if _, nacks := ops.counts(); nacks != 0 {
		t.Fatalf("expected no nack, got %d", nacks)
	}
}

func TestInstanceDefersDelivery(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	inst := startInstance(t, "machine", d)

	if got := bus.DispositionOf(inst); got != bus.Deferred {
		t.Fatalf("an fsm queues the event and runs it later, so a caller must not "+
			"settle on that return; got %v", got)
	}
}

// A stopped instance drops the event. Reporting nil would tell a caller the
// machine took it, and a caller acknowledging a broker delivery on that would
// be acknowledging something that never happened.
func TestAStoppedInstanceRefusesTheEvent(t *testing.T) {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddEvent(&EventDef{Name: "go"})

	inst := NewInstance("machine", d)
	NewFsmCapsule(inst)
	if err := inst.Start(context.Background()); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	inst.Stop()

	ops := &recordingOps{}
	ctx := autoSettled(ops)

	err := inst.OnEvent(ctx, "go", "payload", nil)
	if !errors.Is(err, ErrInstanceStopped) {
		t.Fatalf("expected ErrInstanceStopped, got %v", err)
	}

	// The refusal reaches the broker through the ordinary rule, applied by
	// whoever called OnEvent — nothing in this package nacks by hand.
	bus.SettleOnReturn(ctx, inst, err)

	acks, nacks := ops.counts()
	if acks != 0 || nacks != 1 {
		t.Fatalf("a dropped event should nack once and never ack; got %d acks, %d nacks", acks, nacks)
	}
}

// An event that arrived over a transport with nothing to acknowledge — most of
// them — must cost nothing and settle nothing.
func TestAnEventWithNoSettlerIsUnaffected(t *testing.T) {
	rec := &hookRecorder{}
	inst := startInstance(t, "door", doorFSM(rec))

	if err := inst.OnEvent(context.Background(), "open", "hello", nil); err != nil {
		t.Fatalf("OnEvent error: %v", err)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "open" {
		t.Fatalf("expected 'open', got %q", got)
	}
}

// A panic in a hook must reach the broker before it unwinds, or the message
// sits unsettled until its lease lapses with nothing saying why.
//
// The panic is deliberately left to propagate — this changes what the broker
// hears, not what the process does — so the scenario runs in a child process.
// The assertions are that the nack was made *and* that the panic still killed
// the child, which together are what "nack, then let it continue" means.
func TestAPanicInTheEventLoopNacksBeforeItUnwinds(t *testing.T) {
	if os.Getenv("FSM_PANIC_CHILD") == "1" {
		runEventLoopPanicChild()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestAPanicInTheEventLoopNacksBeforeItUnwinds")
	cmd.Env = append(os.Environ(), "FSM_PANIC_CHILD=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("the panic must still bring the process down")
	}
	if want := "NACKED: panic in fsm door handling open: hook exploded"; !strings.Contains(string(out), want) {
		t.Fatalf("expected the broker to be told what happened before the process died\nwant: %s\ngot:\n%s", want, out)
	}
}

// printingOps reports the nack from inside the settle itself. Observing it any
// other way is a race the observer loses: the repanic follows immediately, so
// nothing that polls for it ever gets to run.
type printingOps struct{ recordingOps }

func (o *printingOps) Nack(ctx context.Context, reason string) error {
	fmt.Println("NACKED:", reason)
	return o.recordingOps.Nack(ctx, reason)
}

func runEventLoopPanicChild() {
	d := NewDefinition("closed")
	d.AddState(&StateDef{Name: "closed"})
	d.AddState(&StateDef{
		Name:    "open",
		OnEntry: func(context.Context, *HookContext) error { panic("hook exploded") },
	})
	d.AddEvent(&EventDef{
		Name:        "open",
		Transitions: []*TransitionDef{{FromState: "closed", ToState: "open"}},
	})

	inst := NewInstance("door", d)
	NewFsmCapsule(inst)
	if err := inst.Start(context.Background()); err != nil {
		fmt.Println("start failed:", err)
		return
	}

	ctx := bus.WithSettler(context.Background(), bus.NewSettler(&printingOps{}, bus.AutoSettle()))
	_ = inst.OnEvent(ctx, "open", "payload", nil)

	time.Sleep(2 * time.Second)
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
