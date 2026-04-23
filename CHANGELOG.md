# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.0] - 2026-04-22

### Added

- **`Event.Ctx` field**: Event now carries an optional `context.Context` that propagates from the originating caller (e.g. a bus-delivery goroutine or a reactive-expression callback) across the event-queue boundary into hook evaluation. Values (trace spans, auth, etc.) are preserved; cancellation is NOT — the event loop applies `context.WithoutCancel` before invoking hooks, so an upstream cancellation cannot interrupt FSM state transitions mid-flight. Events with a nil `Ctx` fall back to the event loop's own context (the one passed to `Start`). `Instance.OnEvent` (bus subscriber) now sets `Event.Ctx` automatically from the inbound ctx.

### Changed

- **FSM transition span is now a linked root, not a child of the caller's span**. Event loop processing is async relative to whatever enqueued an event; by the time an event is dequeued the enqueuing caller's span has typically ended, so a child relationship would violate OTel's parent-before-child discipline. Transitions now open a new root span with a Link back to the caller's span for causal correlation, matching the pattern used by vinculum-bus's `deliverAsync`. This is a behavior change visible in trace backends: transitions will appear in their own traces, linked to (not nested within) the publishing/triggering trace.

## [0.2.0] - 2026-04-19

### Added

- **Snapshot/restore**: `Get()` with no args returns a consistent snapshot of
  instance state (current state, storage, transition count) as a cty object.
  `Set()` with a single object arg validates and restores a snapshot, replacing
  state and storage atomically. Supports both same-goroutine calls (on_init,
  hooks) and concurrent calls from other goroutines via a priority channel.
  Watchers are notified; no hooks fire during restore.

## [0.1.0] - 2026-04-19

### Added

- Core state machine types: Definition, StateDef, EventDef, TransitionDef
- Instance with key-value storage (Get/Set/Increment)
- Event dispatch with guarded transitions and wildcard (`*`) support
- Hook callbacks: on_init, on_entry, on_exit, on_event, on_change, on_error
- Full hook execution order: on_exit, action, state update, on_entry, on_change, NotifyAll
- Self-transitions with full hook sequence
- Event queue with single-goroutine processing
- bus.Subscriber implementation with MQTT topic pattern matching
- Start/Stop lifecycle with priority shutdown event
- OpenTelemetry tracing with per-transition spans
- cty capsule type for HCL integration
- richcty interfaces: Gettable, Settable, Incrementable, Stateful, Countable, Lengthable, Watchable
