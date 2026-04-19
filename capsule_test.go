package fsm

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestCapsule_RoundTrip(t *testing.T) {
	def := newTestDefinition()
	inst := NewInstance("door", def)

	capsule := NewFsmCapsule(inst)

	// Verify capsule type
	if capsule.Type() != FsmCapsuleType {
		t.Fatalf("expected FsmCapsuleType, got %s", capsule.Type().FriendlyName())
	}

	// Extract instance back
	got, err := GetInstanceFromCapsule(capsule)
	if err != nil {
		t.Fatalf("GetInstanceFromCapsule error: %v", err)
	}
	if got != inst {
		t.Fatal("extracted instance does not match original")
	}

	// Instance should have its capsule val set
	if inst.CapsuleVal() != capsule {
		t.Fatal("instance capsule val not set")
	}
}

func TestCapsule_GoString(t *testing.T) {
	def := newTestDefinition()
	inst := NewInstance("door", def)
	capsule := NewFsmCapsule(inst)

	s := capsule.GoString()
	if s != "fsm.door(state=idle)" {
		t.Fatalf("unexpected GoString: %q", s)
	}
}

func TestCapsule_WrongType(t *testing.T) {
	_, err := GetInstanceFromCapsule(cty.StringVal("not a capsule"))
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
}
