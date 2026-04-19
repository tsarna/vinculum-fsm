package fsm

import (
	"context"

	"github.com/zclconf/go-cty/cty"
)

// processEvent handles a single event: finds a matching transition, executes
// the hook sequence, updates state, and notifies watchers. Called only from
// the event processing goroutine, so no locking is needed for the dispatch
// logic itself -- only for state/storage mutations visible to readers.
func (inst *Instance) processEvent(ctx context.Context, evt Event) {
	def := inst.definition

	// If the event was already determined to be unmatched by topic matching,
	// go straight to on_event without looking up by name.
	if evt.unmatched {
		inst.fireOnEvent(ctx, evt)
		return
	}

	// Find the EventDef for this event name.
	eventDef := def.EventByName(evt.Name)
	if eventDef == nil {
		// No event definition matches -- fire on_event on the current state if present.
		inst.fireOnEvent(ctx, evt)
		return
	}

	// Build the base hook context (before we know old/new state for transition).
	currentState := inst.CurrentState()

	// Match a transition: explicit from-state first, then wildcard.
	tr := inst.matchTransition(eventDef, currentState)
	if tr == nil {
		// No transition matches -- fire on_event on the current state if present.
		inst.fireOnEvent(ctx, evt)
		return
	}

	// Build the full hook context.
	hookCtx := &HookContext{
		Event:       evt.Name,
		EventValue:  evt.Value,
		EventFields: evt.Fields,
		OldState:    currentState,
		NewState:    tr.ToState,
		Topic:       evt.Topic,
		TopicParams: evt.TopicParams,
		Fsm:         inst.capsuleVal,
	}

	oldStateDef := def.States[currentState]
	newStateDef := def.States[tr.ToState]

	// Hook sequence:
	// 1. on_exit of old state
	if oldStateDef != nil && oldStateDef.OnExit != nil {
		inst.callHook(ctx, hookCtx, "on_exit", oldStateDef.OnExit)
	}

	// 2. Transition action
	if tr.Action != nil {
		inst.callHook(ctx, hookCtx, "action", tr.Action)
	}

	// 3. Update current state (under write lock)
	inst.mu.Lock()
	inst.currentState = tr.ToState
	inst.mu.Unlock()
	inst.transitionCount.Add(1)

	// 4. on_entry of new state
	if newStateDef != nil && newStateDef.OnEntry != nil {
		inst.callHook(ctx, hookCtx, "on_entry", newStateDef.OnEntry)
	}

	// 5. Machine-level on_change
	if def.OnChange != nil {
		inst.callHook(ctx, hookCtx, "on_change", def.OnChange)
	}

	// 6. Notify watchers (after all locks released)
	inst.NotifyAll(ctx, cty.StringVal(currentState), cty.StringVal(tr.ToState))
}

// matchTransition finds the first matching transition for the given event and
// current state. Explicit from-state matches are checked first (in declaration
// order), then wildcard transitions.
func (inst *Instance) matchTransition(eventDef *EventDef, currentState string) *TransitionDef {
	var wildcards []*TransitionDef

	for _, tr := range eventDef.Transitions {
		if tr.FromState == "*" {
			wildcards = append(wildcards, tr)
			continue
		}
		if tr.FromState == currentState {
			if inst.evaluateGuard(tr) {
				return tr
			}
		}
	}

	// Check wildcards after explicit matches.
	for _, tr := range wildcards {
		if inst.evaluateGuard(tr) {
			return tr
		}
	}

	return nil
}

// evaluateGuard checks a transition's guard function. Returns true if the
// guard is nil (no guard means always match) or if the guard returns true.
func (inst *Instance) evaluateGuard(tr *TransitionDef) bool {
	if tr.Guard == nil {
		return true
	}

	// Build a minimal hook context for guard evaluation.
	hookCtx := &HookContext{
		Fsm: inst.capsuleVal,
	}

	result, err := tr.Guard(context.Background(), hookCtx)
	if err != nil {
		inst.handleHookError(context.Background(), hookCtx, "guard", err)
		return false
	}
	return result
}

// fireOnEvent calls the current state's on_event hook if it exists.
func (inst *Instance) fireOnEvent(ctx context.Context, evt Event) {
	currentState := inst.CurrentState()
	stateDef := inst.definition.States[currentState]
	if stateDef == nil || stateDef.OnEvent == nil {
		return
	}

	hookCtx := &HookContext{
		Event:       evt.Name,
		EventValue:  evt.Value,
		EventFields: evt.Fields,
		Topic:       evt.Topic,
		TopicParams: evt.TopicParams,
		Fsm:         inst.capsuleVal,
		OldState:    currentState,
		NewState:    currentState,
	}
	inst.callHook(ctx, hookCtx, "on_event", stateDef.OnEvent)
}

// callHook invokes a hook function and routes any error to the error handler.
func (inst *Instance) callHook(ctx context.Context, hookCtx *HookContext, hookName string, hook HookFunc) {
	if err := hook(ctx, hookCtx); err != nil {
		inst.handleHookError(ctx, hookCtx, hookName, err)
	}
}

// handleHookError routes a hook error to the on_error handler if configured.
// If no on_error handler exists, the error is silently discarded (the config
// handler in vinculum will wire on_error to log via zap).
func (inst *Instance) handleHookError(ctx context.Context, hookCtx *HookContext, hookName string, err error) {
	if inst.definition.OnError == nil {
		return
	}
	errCtx := *hookCtx
	errCtx.Error = err.Error()
	errCtx.Hook = hookName
	inst.definition.OnError(ctx, &errCtx)
}
