# Vinculum FSM

vinculum-fsm is a cty-native finite state machine library for Go. It
provides event-driven state machines with guarded transitions, key-value
storage, MQTT-style topic matching, and OpenTelemetry tracing. It
implements the [rich-cty-types](https://github.com/tsarna/rich-cty-types)
interfaces (Watchable, Gettable, Settable, Stateful, etc.) and the
[vinculum-bus](https://github.com/tsarna/vinculum-bus) Subscriber
interface, making it a natural building block for reactive systems.

## Features

- **States, events, and guarded transitions** with wildcard (`*`) support
- **Hook callbacks** for state entry/exit, transition actions, and machine-level change notifications
- **Key-value storage** per instance via `Get`/`Set`/`Increment`
- **Event queue** with single-goroutine processing for safe, serialized transitions
- **MQTT-style topic matching** with named parameter extraction (`+name`, `#name`)
- **Priority shutdown** via a dedicated shutdown event channel
- **OpenTelemetry tracing** with per-transition spans and hook error recording
- **Thread-safe reads** — state and storage queries use `RWMutex`, independent of the event goroutine
- **cty capsule type** for embedding in HCL-based configuration systems

## Quick Start

```go
package main

import (
    "context"
    "fmt"

    fsm "github.com/tsarna/vinculum-fsm"
)

func main() {
    // Define the state machine.
    def := fsm.NewDefinition("closed")
    def.AddState(&fsm.StateDef{Name: "closed"})
    def.AddState(&fsm.StateDef{Name: "open"})
    def.AddEvent(&fsm.EventDef{
        Name: "toggle",
        Transitions: []*fsm.TransitionDef{
            {FromState: "closed", ToState: "open"},
            {FromState: "open", ToState: "closed"},
        },
    })

    if err := def.Validate(); err != nil {
        panic(err)
    }

    // Create and start an instance.
    inst := fsm.NewInstance("door", def)
    fsm.NewFsmCapsule(inst) // sets the capsule back-reference for hooks
    inst.Start(context.Background())
    defer inst.Stop()

    fmt.Println(inst.CurrentState()) // "closed"

    inst.OnEvent(context.Background(), "toggle", nil, nil)
    inst.Stop()

    fmt.Println(inst.CurrentState()) // "open"
}
```

## Integration with Vinculum

In the [Vinculum](https://github.com/tsarna/vinculum) configuration
system, state machines are declared as `fsm` blocks in VCL. The config
handler in the main Vinculum repo wraps HCL expressions as hook
callbacks and wires reactive `when` expressions for event-driven
transitions. See the
[FSM documentation](https://github.com/tsarna/vinculum/blob/main/doc/fsm.md)
for the VCL-level reference.

## License

MIT License — see [LICENSE](LICENSE) for details.
