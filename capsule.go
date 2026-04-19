package fsm

import (
	"fmt"
	"reflect"

	"github.com/zclconf/go-cty/cty"
)

// FsmCapsuleType is the cty capsule type for FSM instances.
var FsmCapsuleType = cty.CapsuleWithOps("fsm", reflect.TypeOf((*Instance)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		inst := val.(*Instance)
		return fmt.Sprintf("fsm.%s(state=%s)", inst.name, inst.CurrentState())
	},
	TypeGoString: func(_ reflect.Type) string {
		return "FsmInstance"
	},
})

// NewFsmCapsule creates a cty capsule value wrapping an FSM instance and
// sets the instance's internal capsule reference so that hooks receive it
// as ctx.fsm.
func NewFsmCapsule(inst *Instance) cty.Value {
	val := cty.CapsuleVal(FsmCapsuleType, inst)
	inst.SetCapsuleVal(val)
	return val
}

// GetInstanceFromCapsule extracts an *Instance from a cty capsule value.
func GetInstanceFromCapsule(val cty.Value) (*Instance, error) {
	if val.Type() != FsmCapsuleType {
		return nil, fmt.Errorf("expected FSM capsule, got %s", val.Type().FriendlyName())
	}
	encapsulated := val.EncapsulatedValue()
	inst, ok := encapsulated.(*Instance)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an FSM Instance, got %T", encapsulated)
	}
	return inst, nil
}
