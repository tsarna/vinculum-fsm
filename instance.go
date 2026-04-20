package fsm

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"

	richcty "github.com/tsarna/rich-cty-types"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/trace"
)

// Instance is a running state machine: it holds the current state, storage,
// and event queue. It implements Gettable, Settable, Incrementable, Stateful,
// Countable, Lengthable, and Watchable from rich-cty-types, plus
// bus.Subscriber from vinculum-bus.
type Instance struct {
	richcty.WatchableMixin

	definition *Definition
	name       string

	// mu protects currentState and storage for concurrent reads.
	// The event processing goroutine acquires a write lock only when
	// updating state or storage; readers can proceed concurrently.
	mu           sync.RWMutex
	currentState string
	storage      map[string]cty.Value

	transitionCount atomic.Int64

	// capsuleVal is the cty capsule wrapping this instance, set after creation.
	capsuleVal cty.Value

	// tracer is set via SetTracerProvider. If nil, no spans are created.
	tracer trace.Tracer

	// eventCh and shutdownCh are created at Start time.
	eventCh    chan Event
	shutdownCh chan Event
	initCh     chan struct{}
	wg         *sync.WaitGroup
	stopped    atomic.Bool
}

// NewInstance creates a new FSM instance from a validated definition.
// The instance starts in the definition's initial state with empty storage.
// Call SetInitialStorage to pre-populate storage before starting.
func NewInstance(name string, def *Definition) *Instance {
	inst := &Instance{
		definition:   def,
		name:         name,
		currentState: def.InitialState,
		storage:      make(map[string]cty.Value),
	}
	return inst
}

// Configure replaces the placeholder definition with the real one and
// resets the initial state. Must be called before Start.
func (inst *Instance) Configure(def *Definition) {
	inst.definition = def
	inst.currentState = def.InitialState
}

// SetTracerProvider configures tracing for this instance. If tp is non-nil,
// each transition creates a span with FSM-specific attributes. Must be
// called before Start.
func (inst *Instance) SetTracerProvider(tp trace.TracerProvider) {
	if tp != nil {
		inst.tracer = tp.Tracer("vinculum/fsm")
	}
}

// Name returns the instance name.
func (inst *Instance) Name() string {
	return inst.name
}

// Definition returns the definition this instance was created from.
func (inst *Instance) Definition() *Definition {
	return inst.definition
}

// SetCapsuleVal sets the cty capsule value that wraps this instance.
// Must be called before Start so that hooks receive the correct ctx.fsm.
func (inst *Instance) SetCapsuleVal(val cty.Value) {
	inst.capsuleVal = val
}

// CapsuleVal returns the cty capsule wrapping this instance.
func (inst *Instance) CapsuleVal() cty.Value {
	return inst.capsuleVal
}

// SetInitialStorage sets a storage key before the instance is started.
// This is used for the config-time storage {} block.
func (inst *Instance) SetInitialStorage(key string, val cty.Value) {
	inst.storage[key] = val
}

// CurrentState returns the current state name. Thread-safe.
func (inst *Instance) CurrentState() string {
	inst.mu.RLock()
	defer inst.mu.RUnlock()
	return inst.currentState
}

// --- richcty.Gettable ---

// Get implements richcty.Gettable.
// get(fsm.x) returns a complete snapshot of the instance state.
// get(fsm.x, "key") returns the stored value for "key".
// get(fsm.x, "key", default) returns default if key is not found.
func (inst *Instance) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return inst.snapshot(), nil
	}

	key, err := stringArg(args[0], "key")
	if err != nil {
		return cty.NilVal, err
	}

	inst.mu.RLock()
	val, ok := inst.storage[key]
	inst.mu.RUnlock()

	if !ok {
		if len(args) > 1 {
			return args[1], nil
		}
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return val, nil
}

// --- richcty.Settable ---

// Set implements richcty.Settable.
// set(fsm.x, "key", value) stores value under key.
// set(fsm.x, snapshot) restores a previously captured snapshot.
func (inst *Instance) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
	// Single object arg = snapshot restore.
	if len(args) == 1 && args[0].Type().IsObjectType() {
		return inst.restoreFromSnapshot(ctx, args[0])
	}

	if len(args) < 2 {
		return cty.NilVal, fmt.Errorf("set(fsm.%s, key, value) requires a key and value", inst.name)
	}

	key, err := stringArg(args[0], "key")
	if err != nil {
		return cty.NilVal, err
	}

	value := args[1]

	inst.mu.Lock()
	if value.IsNull() {
		delete(inst.storage, key)
	} else {
		inst.storage[key] = value
	}
	inst.mu.Unlock()

	return value, nil
}

// --- richcty.Incrementable ---

// Increment implements richcty.Incrementable.
// increment(fsm.x, "key", delta) increments the numeric value at key by delta.
// delta defaults to 1 if omitted.
func (inst *Instance) Increment(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) < 1 {
		return cty.NilVal, fmt.Errorf("increment(fsm.%s, key[, delta]) requires at least a key", inst.name)
	}

	key, err := stringArg(args[0], "key")
	if err != nil {
		return cty.NilVal, err
	}

	delta := cty.NumberIntVal(1)
	if len(args) >= 2 {
		delta = args[1]
	}
	if delta.Type() != cty.Number {
		return cty.NilVal, fmt.Errorf("increment: delta must be a number, got %s", delta.Type().FriendlyName())
	}

	inst.mu.Lock()
	current, ok := inst.storage[key]
	if !ok {
		// Treat missing as zero.
		current = cty.NumberIntVal(0)
	}
	if current.Type() != cty.Number {
		inst.mu.Unlock()
		return cty.NilVal, fmt.Errorf("increment: current value for %q is not a number, got %s", key, current.Type().FriendlyName())
	}

	sum := new(big.Float).Add(current.AsBigFloat(), delta.AsBigFloat())
	newVal := cty.NumberVal(sum)
	inst.storage[key] = newVal
	inst.mu.Unlock()

	return newVal, nil
}

// --- richcty.Stateful ---

// State implements richcty.Stateful. Returns the current state name.
func (inst *Instance) State(_ context.Context) (string, error) {
	return inst.CurrentState(), nil
}

// --- richcty.Countable ---

// Count implements richcty.Countable. Returns the total number of transitions
// since startup.
func (inst *Instance) Count(_ context.Context) (int64, error) {
	return inst.transitionCount.Load(), nil
}

// --- richcty.Lengthable ---

// Length implements richcty.Lengthable. Returns the number of events
// currently queued for processing.
func (inst *Instance) Length(_ context.Context) (int64, error) {
	if inst.eventCh == nil {
		return 0, nil
	}
	return int64(len(inst.eventCh)), nil
}

// --- Snapshot / Restore ---

// snapshot returns a consistent cty object containing the instance's
// runtime state. State and storage are read under a single read lock.
func (inst *Instance) snapshot() cty.Value {
	inst.mu.RLock()
	state := inst.currentState
	storageCopy := make(map[string]cty.Value, len(inst.storage))
	for k, v := range inst.storage {
		storageCopy[k] = v
	}
	inst.mu.RUnlock()

	count := inst.transitionCount.Load()

	// Build storage object. Use EmptyObjectVal for empty storage so the
	// field is always an object, never null.
	var storageVal cty.Value
	if len(storageCopy) == 0 {
		storageVal = cty.EmptyObjectVal
	} else {
		storageVal = cty.ObjectVal(storageCopy)
	}

	return cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal(state),
		"transition_count": cty.NumberIntVal(count),
		"storage":          storageVal,
	})
}

// restoreFromSnapshot validates a snapshot and enqueues a restore event.
// Validation is synchronous; the actual state/storage swap is async
// (processed by the event goroutine like any other event).
func (inst *Instance) restoreFromSnapshot(_ context.Context, snap cty.Value) (cty.Value, error) {
	state, storage, err := inst.validateSnapshot(snap)
	if err != nil {
		return cty.NilVal, err
	}

	if !inst.EnqueueEvent(Event{
		Name:    restoreEventName,
		restore: &restoreData{state: state, storage: storage},
	}) {
		return cty.NilVal, fmt.Errorf("fsm %q is not running", inst.name)
	}
	return snap, nil
}

// validateSnapshot checks that a cty value is a valid FSM snapshot and
// extracts the state name and storage map.
func (inst *Instance) validateSnapshot(snap cty.Value) (string, map[string]cty.Value, error) {
	if !snap.Type().IsObjectType() {
		return "", nil, fmt.Errorf("snapshot must be an object")
	}

	// Check _type.
	if !snap.Type().HasAttribute("_type") {
		return "", nil, fmt.Errorf("snapshot missing _type field")
	}
	typeVal := snap.GetAttr("_type")
	if typeVal.Type() != cty.String || typeVal.AsString() != "fsm" {
		got := "non-string"
		if typeVal.Type() == cty.String {
			got = typeVal.AsString()
		}
		return "", nil, fmt.Errorf("expected _type \"fsm\", got %q", got)
	}

	// Check state.
	if !snap.Type().HasAttribute("state") {
		return "", nil, fmt.Errorf("snapshot missing state field")
	}
	stateVal := snap.GetAttr("state")
	if stateVal.Type() != cty.String {
		return "", nil, fmt.Errorf("state must be a string")
	}
	state := stateVal.AsString()
	if _, ok := inst.definition.States[state]; !ok {
		return "", nil, fmt.Errorf("state %q is not declared in this FSM", state)
	}

	// Extract storage.
	storage := make(map[string]cty.Value)
	if snap.Type().HasAttribute("storage") {
		storageVal := snap.GetAttr("storage")
		if !storageVal.IsNull() && storageVal.IsKnown() {
			if !storageVal.Type().IsObjectType() {
				return "", nil, fmt.Errorf("storage must be an object")
			}
			for k, v := range storageVal.AsValueMap() {
				storage[k] = v
			}
		}
	}

	return state, storage, nil
}

// applyRestore replaces the current state and storage, then notifies
// watchers. No hooks fire — this is a restore, not a transition.
func (inst *Instance) applyRestore(ctx context.Context, state string, storage map[string]cty.Value) {
	inst.mu.Lock()
	oldState := inst.currentState
	inst.currentState = state
	inst.storage = storage
	inst.mu.Unlock()

	inst.NotifyAll(ctx, cty.StringVal(oldState), cty.StringVal(state))
}

// stringArg extracts a string from a cty.Value, returning a descriptive error.
func stringArg(val cty.Value, name string) (string, error) {
	if val.Type() != cty.String {
		return "", fmt.Errorf("%s must be a string, got %s", name, val.Type().FriendlyName())
	}
	return val.AsString(), nil
}
