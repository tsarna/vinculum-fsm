package fsm

import (
	"context"
	"sync"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestSnapshot_Basic(t *testing.T) {
	def := newTestDefinition()
	inst := NewInstance("door", def)
	inst.SetInitialStorage("count", cty.NumberIntVal(42))
	inst.SetInitialStorage("label", cty.StringVal("hello"))

	snap, err := inst.Get(context.Background(), nil)
	if err != nil {
		t.Fatalf("snapshot error: %v", err)
	}

	// Check _type.
	if got := snap.GetAttr("_type").AsString(); got != "fsm" {
		t.Fatalf("expected _type 'fsm', got %q", got)
	}

	// Check state.
	if got := snap.GetAttr("state").AsString(); got != "idle" {
		t.Fatalf("expected state 'idle', got %q", got)
	}

	// Check transition_count.
	countVal := snap.GetAttr("transition_count")
	if countVal.IsNull() || countVal.Type() != cty.Number {
		t.Fatal("expected transition_count to be a number")
	}
	bf := countVal.AsBigFloat()
	n, _ := bf.Int64()
	if n != 0 {
		t.Fatalf("expected transition_count 0, got %d", n)
	}

	// Check storage.
	storageVal := snap.GetAttr("storage")
	if storageVal.IsNull() {
		t.Fatal("expected non-null storage")
	}
	if got := storageVal.GetAttr("count"); got.AsBigFloat().String() != "42" {
		t.Fatalf("expected storage.count=42, got %s", got.AsBigFloat().String())
	}
	if got := storageVal.GetAttr("label").AsString(); got != "hello" {
		t.Fatalf("expected storage.label='hello', got %q", got)
	}
}

func TestSnapshot_EmptyStorage(t *testing.T) {
	def := newTestDefinition()
	inst := NewInstance("test", def)

	snap, err := inst.Get(context.Background(), nil)
	if err != nil {
		t.Fatalf("snapshot error: %v", err)
	}

	storageVal := snap.GetAttr("storage")
	if storageVal.IsNull() {
		t.Fatal("expected non-null storage (should be empty object)")
	}
	// Empty object should have no attributes.
	if storageVal.LengthInt() != 0 {
		t.Fatalf("expected empty storage, got length %d", storageVal.LengthInt())
	}
}

func TestRestore_Basic(t *testing.T) {
	def := newTestDefinition()
	inst := startInstance(t, "door", def)
	ctx := context.Background()

	// Set some storage and transition to "active".
	inst.Set(ctx, []cty.Value{cty.StringVal("color"), cty.StringVal("blue")})
	inst.EnqueueEvent(Event{Name: "activate"})

	// Take a snapshot while active.
	inst.Stop()
	snap, _ := inst.Get(ctx, nil)
	if snap.GetAttr("state").AsString() != "active" {
		t.Fatal("expected state 'active' in snapshot")
	}

	// Create a new instance in the initial state.
	inst2 := startInstance(t, "door2", def)
	if inst2.CurrentState() != "idle" {
		t.Fatal("new instance should start in 'idle'")
	}

	// Restore the snapshot.
	_, err := inst2.Set(ctx, []cty.Value{snap})
	if err != nil {
		t.Fatalf("restore error: %v", err)
	}

	// State and storage should be restored.
	inst2.Stop()
	if got := inst2.CurrentState(); got != "active" {
		t.Fatalf("expected restored state 'active', got %q", got)
	}
	val, _ := inst2.Get(ctx, []cty.Value{cty.StringVal("color")})
	if val.AsString() != "blue" {
		t.Fatalf("expected restored storage.color='blue', got %q", val.AsString())
	}
}

func TestRestore_FromHook(t *testing.T) {
	// Restore called from on_init (same-goroutine path).
	snap := cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal("active"),
		"transition_count": cty.NumberIntVal(0),
		"storage":          cty.ObjectVal(map[string]cty.Value{"key": cty.StringVal("restored")}),
	})

	var restoreErr error
	def := NewDefinition("idle")
	def.AddState(&StateDef{
		Name: "idle",
		OnInit: func(ctx context.Context, hookCtx *HookContext) error {
			_, restoreErr = hookCtx.Fsm.EncapsulatedValue().(*Instance).Set(ctx, []cty.Value{snap})
			return nil
		},
	})
	def.AddState(&StateDef{Name: "active"})
	def.AddEvent(&EventDef{
		Name:        "activate",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "active"}},
	})

	inst := NewInstance("test", def)
	NewFsmCapsule(inst)
	if err := inst.Start(context.Background()); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer inst.Stop()

	if restoreErr != nil {
		t.Fatalf("restore from on_init error: %v", restoreErr)
	}

	// State should be restored to "active" after on_init.
	if got := inst.CurrentState(); got != "active" {
		t.Fatalf("expected 'active' after on_init restore, got %q", got)
	}

	val, _ := inst.Get(context.Background(), []cty.Value{cty.StringVal("key")})
	if val.AsString() != "restored" {
		t.Fatalf("expected storage.key='restored', got %q", val.AsString())
	}
}

func TestRestore_Concurrent(t *testing.T) {
	// Restore called from another goroutine (channel path).
	def := newTestDefinition()
	inst := startInstance(t, "test", def)

	snap := cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal("active"),
		"transition_count": cty.NumberIntVal(0),
		"storage":          cty.EmptyObjectVal,
	})

	// Restore from a different goroutine.
	var restoreErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, restoreErr = inst.Set(context.Background(), []cty.Value{snap})
	}()
	wg.Wait()

	if restoreErr != nil {
		t.Fatalf("concurrent restore error: %v", restoreErr)
	}

	inst.Stop()
	if got := inst.CurrentState(); got != "active" {
		t.Fatalf("expected 'active' after concurrent restore, got %q", got)
	}
}

func TestRestore_Validation(t *testing.T) {
	def := newTestDefinition()
	inst := startInstance(t, "test", def)
	ctx := context.Background()

	tests := []struct {
		name string
		snap cty.Value
		want string
	}{
		{
			name: "missing _type",
			snap: cty.ObjectVal(map[string]cty.Value{
				"state":   cty.StringVal("idle"),
				"storage": cty.EmptyObjectVal,
			}),
			want: "missing _type",
		},
		{
			name: "wrong _type",
			snap: cty.ObjectVal(map[string]cty.Value{
				"_type":   cty.StringVal("not_fsm"),
				"state":   cty.StringVal("idle"),
				"storage": cty.EmptyObjectVal,
			}),
			want: "expected _type",
		},
		{
			name: "missing state",
			snap: cty.ObjectVal(map[string]cty.Value{
				"_type":   cty.StringVal("fsm"),
				"storage": cty.EmptyObjectVal,
			}),
			want: "missing state",
		},
		{
			name: "undeclared state",
			snap: cty.ObjectVal(map[string]cty.Value{
				"_type":   cty.StringVal("fsm"),
				"state":   cty.StringVal("nonexistent"),
				"storage": cty.EmptyObjectVal,
			}),
			want: "not declared",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inst.Set(ctx, []cty.Value{tc.snap})
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}

	inst.Stop()
}

func TestRestore_NotifiesWatchers(t *testing.T) {
	def := newTestDefinition()
	inst := startInstance(t, "test", def)

	var mu sync.Mutex
	var oldVal, newVal cty.Value
	notified := make(chan struct{}, 1)

	watcher := &testWatcher{
		fn: func(_ context.Context, old, new_ cty.Value) {
			mu.Lock()
			oldVal = old
			newVal = new_
			mu.Unlock()
			select {
			case notified <- struct{}{}:
			default:
			}
		},
	}
	inst.Watch(watcher)
	defer inst.Unwatch(watcher)

	snap := cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal("active"),
		"transition_count": cty.NumberIntVal(0),
		"storage":          cty.EmptyObjectVal,
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.Set(context.Background(), []cty.Value{snap})
	}()
	wg.Wait()

	inst.Stop()

	mu.Lock()
	defer mu.Unlock()
	if oldVal.AsString() != "idle" {
		t.Fatalf("expected old='idle', got %q", oldVal.AsString())
	}
	if newVal.AsString() != "active" {
		t.Fatalf("expected new='active', got %q", newVal.AsString())
	}
}

func TestRestore_NoHooksFire(t *testing.T) {
	rec := &hookRecorder{}
	d := NewDefinition("idle")
	d.AddState(&StateDef{
		Name:    "idle",
		OnExit:  rec.record("idle:on_exit"),
		OnEntry: rec.record("idle:on_entry"),
	})
	d.AddState(&StateDef{
		Name:    "active",
		OnEntry: rec.record("active:on_entry"),
		OnExit:  rec.record("active:on_exit"),
	})
	d.OnChange = rec.record("on_change")
	d.AddEvent(&EventDef{
		Name:        "go",
		Transitions: []*TransitionDef{{FromState: "idle", ToState: "active"}},
	})

	inst := startInstance(t, "test", d)

	snap := cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal("active"),
		"transition_count": cty.NumberIntVal(0),
		"storage":          cty.EmptyObjectVal,
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.Set(context.Background(), []cty.Value{snap})
	}()
	wg.Wait()

	inst.Stop()

	// No hooks should have fired — restore is not a transition.
	calls := rec.getCalls()
	for _, c := range calls {
		t.Errorf("unexpected hook call during restore: %s", c)
	}
}

func TestRestore_StorageReplaced(t *testing.T) {
	def := newTestDefinition()
	inst := startInstance(t, "test", def)
	ctx := context.Background()

	// Set some storage.
	inst.Set(ctx, []cty.Value{cty.StringVal("old_key"), cty.StringVal("old_val")})
	inst.Set(ctx, []cty.Value{cty.StringVal("keep"), cty.StringVal("should_vanish")})

	// Restore with only "new_key".
	snap := cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal("idle"),
		"transition_count": cty.NumberIntVal(0),
		"storage": cty.ObjectVal(map[string]cty.Value{
			"new_key": cty.StringVal("new_val"),
		}),
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		inst.Set(ctx, []cty.Value{snap})
	}()
	wg.Wait()

	inst.Stop()

	// old_key and keep should be gone.
	val, _ := inst.Get(ctx, []cty.Value{cty.StringVal("old_key")})
	if !val.IsNull() {
		t.Fatal("expected old_key to be removed after restore")
	}
	val, _ = inst.Get(ctx, []cty.Value{cty.StringVal("keep")})
	if !val.IsNull() {
		t.Fatal("expected keep to be removed after restore")
	}
	// new_key should exist.
	val, _ = inst.Get(ctx, []cty.Value{cty.StringVal("new_key")})
	if val.AsString() != "new_val" {
		t.Fatalf("expected new_key='new_val', got %q", val.AsString())
	}
}

func TestRestore_NotStarted(t *testing.T) {
	def := newTestDefinition()
	inst := NewInstance("test", def)
	NewFsmCapsule(inst)

	snap := cty.ObjectVal(map[string]cty.Value{
		"_type":            cty.StringVal("fsm"),
		"state":            cty.StringVal("active"),
		"transition_count": cty.NumberIntVal(0),
		"storage":          cty.EmptyObjectVal,
	})

	// Restore on a non-started instance fails because the event channel is nil.
	_, err := inst.Set(context.Background(), []cty.Value{snap})
	if err == nil {
		t.Fatal("expected error for restore on non-started instance")
	}
	if !contains(err.Error(), "not running") {
		t.Fatalf("expected 'not running' error, got %q", err.Error())
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
