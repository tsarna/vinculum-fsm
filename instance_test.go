package fsm

import (
	"context"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func newTestDefinition() *Definition {
	d := NewDefinition("idle")
	d.AddState(&StateDef{Name: "idle"})
	d.AddState(&StateDef{Name: "active"})
	d.AddEvent(&EventDef{
		Name: "activate",
		Transitions: []*TransitionDef{
			{FromState: "idle", ToState: "active"},
		},
	})
	d.AddEvent(&EventDef{
		Name: "deactivate",
		Transitions: []*TransitionDef{
			{FromState: "active", ToState: "idle"},
		},
	})
	return d
}

func TestInstance_InitialState(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	if got := inst.CurrentState(); got != "idle" {
		t.Fatalf("expected initial state 'idle', got %q", got)
	}
}

func TestInstance_Name(t *testing.T) {
	inst := NewInstance("door", newTestDefinition())
	if got := inst.Name(); got != "door" {
		t.Fatalf("expected name 'door', got %q", got)
	}
}

func TestInstance_State(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	state, err := inst.State(ctx)
	if err != nil {
		t.Fatalf("State() error: %v", err)
	}
	if state != "idle" {
		t.Fatalf("expected 'idle', got %q", state)
	}
}

func TestInstance_Count_Initial(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	count, err := inst.Count(ctx)
	if err != nil {
		t.Fatalf("Count() error: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 transitions, got %d", count)
	}
}

func TestInstance_Length_BeforeStart(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	length, err := inst.Length(ctx)
	if err != nil {
		t.Fatalf("Length() error: %v", err)
	}
	if length != 0 {
		t.Fatalf("expected queue length 0, got %d", length)
	}
}

func TestInstance_Storage_GetSet(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	// Get missing key without default returns null
	val, err := inst.Get(ctx, []cty.Value{cty.StringVal("missing")})
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if !val.IsNull() {
		t.Fatalf("expected null for missing key, got %#v", val)
	}

	// Get missing key with default returns default
	val, err = inst.Get(ctx, []cty.Value{cty.StringVal("missing"), cty.StringVal("fallback")})
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val.AsString() != "fallback" {
		t.Fatalf("expected 'fallback', got %q", val.AsString())
	}

	// Set a value
	_, err = inst.Set(ctx, []cty.Value{cty.StringVal("color"), cty.StringVal("blue")})
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	// Get returns the set value
	val, err = inst.Get(ctx, []cty.Value{cty.StringVal("color")})
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val.AsString() != "blue" {
		t.Fatalf("expected 'blue', got %q", val.AsString())
	}

	// Set to null removes the key
	_, err = inst.Set(ctx, []cty.Value{cty.StringVal("color"), cty.NullVal(cty.String)})
	if err != nil {
		t.Fatalf("Set(null) error: %v", err)
	}
	val, err = inst.Get(ctx, []cty.Value{cty.StringVal("color")})
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if !val.IsNull() {
		t.Fatalf("expected null after delete, got %#v", val)
	}
}

func TestInstance_Storage_Increment(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	// Increment missing key treats as zero
	val, err := inst.Increment(ctx, []cty.Value{cty.StringVal("counter"), cty.NumberIntVal(5)})
	if err != nil {
		t.Fatalf("Increment() error: %v", err)
	}
	if val.IsNull() || val.AsBigFloat().String() != "5" {
		t.Fatalf("expected 5, got %#v", val)
	}

	// Increment again
	val, err = inst.Increment(ctx, []cty.Value{cty.StringVal("counter"), cty.NumberIntVal(3)})
	if err != nil {
		t.Fatalf("Increment() error: %v", err)
	}
	if val.AsBigFloat().String() != "8" {
		t.Fatalf("expected 8, got %s", val.AsBigFloat().String())
	}

	// Increment with default delta (1)
	val, err = inst.Increment(ctx, []cty.Value{cty.StringVal("counter")})
	if err != nil {
		t.Fatalf("Increment() default delta error: %v", err)
	}
	if val.AsBigFloat().String() != "9" {
		t.Fatalf("expected 9, got %s", val.AsBigFloat().String())
	}

	// Increment non-numeric value fails
	inst.Set(ctx, []cty.Value{cty.StringVal("name"), cty.StringVal("alice")})
	_, err = inst.Increment(ctx, []cty.Value{cty.StringVal("name"), cty.NumberIntVal(1)})
	if err == nil {
		t.Fatal("expected error incrementing non-numeric value")
	}
}

func TestInstance_Snapshot_NoArgs(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	// Get() with no args returns a snapshot.
	snap, err := inst.Get(ctx, nil)
	if err != nil {
		t.Fatalf("Get() snapshot error: %v", err)
	}
	if snap.Type().HasAttribute("_type") {
		typeVal := snap.GetAttr("_type")
		if typeVal.AsString() != "fsm" {
			t.Fatalf("expected _type 'fsm', got %q", typeVal.AsString())
		}
	} else {
		t.Fatal("snapshot missing _type field")
	}
}

func TestInstance_Storage_SetTooFewArgs(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	ctx := context.Background()

	_, err := inst.Set(ctx, []cty.Value{cty.StringVal("key")})
	if err == nil {
		t.Fatal("expected error for Set() with only key")
	}
}

func TestInstance_InitialStorage(t *testing.T) {
	inst := NewInstance("test", newTestDefinition())
	inst.SetInitialStorage("count", cty.NumberIntVal(0))
	inst.SetInitialStorage("label", cty.StringVal("default"))

	ctx := context.Background()
	val, _ := inst.Get(ctx, []cty.Value{cty.StringVal("count")})
	if val.AsBigFloat().String() != "0" {
		t.Fatalf("expected 0, got %s", val.AsBigFloat().String())
	}
	val, _ = inst.Get(ctx, []cty.Value{cty.StringVal("label")})
	if val.AsString() != "default" {
		t.Fatalf("expected 'default', got %q", val.AsString())
	}
}
