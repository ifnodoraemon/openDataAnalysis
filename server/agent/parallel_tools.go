package agent

import (
	"context"
	"encoding/json"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// defaultMaxParallelToolCalls bounds how many parallel-safe calls may overlap.
const defaultMaxParallelToolCalls = 10

func maxParallelToolCalls() int {
	if config.Cfg != nil && config.Cfg.MaxParallelToolCalls > 0 {
		return config.Cfg.MaxParallelToolCalls
	}
	return defaultMaxParallelToolCalls
}

// ConcurrencySafeTool is implemented by tools that may run at the same time as
// another tool call in the same batch.
//
// This is an explicit opt-in and is never inferred from a tool's mode: a
// read-only tool can still be unsafe to overlap when it shares a single
// connection or another mutable resource. Only an exact true opts in; a missing
// implementation, a false result, or a panic keeps the call exclusive.
type ConcurrencySafeTool interface {
	IsConcurrencySafe(args json.RawMessage) bool
}

func toolIsConcurrencySafe(tool tools.Tool, args json.RawMessage) (safe bool) {
	classifier, ok := tool.(ConcurrencySafeTool)
	if !ok {
		return false
	}
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	return classifier.IsConcurrencySafe(args)
}

// plannedToolCall is one tool call of a batch with its admission and
// concurrency decisions already resolved.
type plannedToolCall struct {
	call       LLMToolCall
	tool       tools.Tool
	capability tools.ToolCapability
	admission  tools.Admission
	safe       bool
}

// planToolCalls resolves admission and concurrency for a whole batch in the
// model's order, so the execution loop only consumes decisions.
//
// Availability is decided here, at the point of dispatch, with the same check
// that decides whether the tool is advertised. A tool that went down between
// the request and the call fails with a structured result instead of running.
func (e *Engine) planToolCalls(ctx context.Context, calls []LLMToolCall) ([]plannedToolCall, error) {
	plan := make([]plannedToolCall, 0, len(calls))
	for _, call := range calls {
		tool, err := e.registry.Get(call.Function.Name)
		if err != nil {
			return nil, err
		}
		args := json.RawMessage(call.Function.Arguments)
		capability := toolCapability(tool)
		admission := tools.DecideToolCall(tool, args, e)
		if admission.Kind == tools.AdmissionAllow {
			if checker, ok := tool.(tools.AvailabilityTool); ok {
				if checkErr := probeToolAvailability(ctx, checker); checkErr != nil {
					admission = tools.Admission{
						Kind:   tools.AdmissionDeny,
						Code:   "tool_unavailable",
						Reason: "tool " + call.Function.Name + " is currently unavailable: " + checkErr.Error(),
					}
				}
			}
		}
		plan = append(plan, plannedToolCall{
			call:       call,
			tool:       tool,
			capability: capability,
			admission:  admission,
			safe: admission.Kind == tools.AdmissionAllow &&
				capability.RunControl != "suspend" &&
				toolIsConcurrencySafe(tool, args),
		})
	}
	return plan, nil
}

// toolFuture is the result of a tool call that was started ahead of its turn.
type toolFuture struct {
	done   chan struct{}
	result string
	err    error
}

func (f *toolFuture) wait() (string, error) {
	<-f.done
	return f.result, f.err
}

// startParallelRun starts every call in the maximal run of parallel-safe calls
// that begins at start. The run stops at the first exclusive call, so an
// exclusive call remains an ordering barrier: nothing after it is dispatched
// before the loop reaches it.
func startParallelRun(plan []plannedToolCall, start int, futures map[string]*toolFuture, budget chan struct{}, ctx context.Context, registry *tools.Registry) {
	for i := start; i < len(plan) && plan[i].safe; i++ {
		planned := plan[i]
		if _, exists := futures[planned.call.ID]; exists {
			continue
		}
		future := &toolFuture{done: make(chan struct{})}
		futures[planned.call.ID] = future
		go func() {
			defer close(future.done)
			select {
			case budget <- struct{}{}:
				defer func() { <-budget }()
			case <-ctx.Done():
				future.err = ctx.Err()
				return
			}
			future.result, future.err = retryableToolExec(
				ctx, registry, planned.call.Function.Name, json.RawMessage(planned.call.Function.Arguments))
		}()
	}
}

// runPlannedToolCall consumes a prefetched result when the call was started
// ahead of its turn, and otherwise executes it inline.
func runPlannedToolCall(ctx context.Context, registry *tools.Registry, call LLMToolCall, futures map[string]*toolFuture) (string, error) {
	if future, ok := futures[call.ID]; ok {
		return future.wait()
	}
	return retryableToolExec(ctx, registry, call.Function.Name, json.RawMessage(call.Function.Arguments))
}
