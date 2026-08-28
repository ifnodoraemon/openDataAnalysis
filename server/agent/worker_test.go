package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type runtimeAwareTestTool struct {
	ctx  context.Context
	emit func(RuntimeEvent)
}

func (t *runtimeAwareTestTool) Name() string { return "runtime_aware_test_tool" }
func (t *runtimeAwareTestTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}
func (t *runtimeAwareTestTool) Description() string {
	return "test runtime-aware tool"
}
func (t *runtimeAwareTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *runtimeAwareTestTool) Execute(json.RawMessage) (string, error) {
	return `{"ok":true}`, nil
}
func (t *runtimeAwareTestTool) SetEventEmitter(emit func(RuntimeEvent)) {
	t.emit = emit
}
func (t *runtimeAwareTestTool) SetExecutionContext(ctx context.Context) {
	t.ctx = ctx
}

func fixedRegistryFactory(registry *tools.Registry) tools.RegistryFactory {
	return func(allowed []string) *tools.Registry {
		return registry.CloneFiltered(allowed)
	}
}

func TestDelegateTaskToolReturnsStructuredFailureWhenAllowedToolsUnresolved(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(tools.NewRegistry()),
	}

	result, err := tool.Execute(json.RawMessage(`{
		"role_name":"researcher",
		"task_instruction":"检查销售异常",
		"allowed_tools":["missing_tool"]
	}`))
	if err != nil {
		t.Fatalf("expected structured failure instead of error, got %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["tool"] != "task_delegate" || payload["ok"] != false {
		t.Fatalf("unexpected delegate failure payload: %#v", payload)
	}
	if payload["error_code"] != "tools_not_delegable" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
	if payload["delegate_role"] != "researcher" {
		t.Fatalf("expected delegate_role in payload: %#v", payload)
	}
}

func TestDelegateTaskToolReturnsStructuredFailureForMissingRoleName(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(tools.NewRegistry()),
	}

	result, err := tool.Execute(json.RawMessage(`{
		"task_instruction":"检查销售异常",
		"allowed_tools":["data_query_sql"]
	}`))
	if err != nil {
		t.Fatalf("expected structured failure instead of error, got %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["error_code"] != "missing_role_name" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
	if payload["tool"] != "task_delegate" {
		t.Fatalf("unexpected tool: %#v", payload["tool"])
	}
}

func TestDelegateTaskToolReturnsStructuredFailureForMissingRegistryResult(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		RegistryFactory: func([]string) *tools.Registry { return nil },
	}
	result, err := tool.Execute(json.RawMessage(`{
		"role_name":"researcher",
		"task_instruction":"检查数据事实",
		"allowed_tools":["data_query_sql"]
	}`))
	if err != nil {
		t.Fatalf("expected structured failure instead of error, got %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected JSON payload: %v", err)
	}
	if payload["error_code"] != "delegate_registry_unavailable" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateChildToolFailureReturnsStructuredPayload(t *testing.T) {
	t.Parallel()

	result, err := delegateChildToolFailure("data_query_sql", "boom")
	if err != nil {
		t.Fatalf("encode child tool failure: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["tool"] != "data_query_sql" || payload["ok"] != false {
		t.Fatalf("unexpected child tool failure payload: %#v", payload)
	}
	if payload["error_code"] != "execution_error" || payload["message"] != "boom" {
		t.Fatalf("unexpected child tool failure fields: %#v", payload)
	}
}

func TestDelegateTaskToolParsesPolicyAppendix(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(tools.NewRegistry()),
	}
	result, err := tool.Execute(json.RawMessage(`{
		"role_name":"researcher",
		"task_instruction":"检查销售异常",
		"allowed_tools":["missing_tool"],
		"policy_appendix":"仅检查国内数据"
	}`))
	if err != nil {
		t.Fatalf("expected structured failure instead of error, got %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["tool"] != "task_delegate" || payload["ok"] != false {
		t.Fatalf("unexpected delegate failure payload: %#v", payload)
	}
	if payload["error_code"] != "tools_not_delegable" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateTaskToolDoesNotGuessPolicyAppendixSemantics(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(tools.NewRegistry()),
	}
	result, err := tool.Execute(json.RawMessage(`{
		"role_name":"researcher",
		"task_instruction":"检查销售异常",
		"allowed_tools":["missing_tool"],
		"policy_appendix":"背景: 已知事实如下\nschema: sales(amount, region)"
	}`))
	if err != nil {
		t.Fatalf("expected structured failure instead of error, got %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["error_code"] != "tools_not_delegable" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateTaskToolUsesCapabilityContractForNonDelegableTools(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		RegistryFactory: fixedRegistryFactory(tools.NewRegistry()),
	}
	result, err := tool.Execute(json.RawMessage(`{
		"role_name":"researcher",
		"task_instruction":"需要确认指标口径",
		"allowed_tools":["user_request_input"]
	}`))
	if err != nil {
		t.Fatalf("expected structured failure instead of error, got %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("expected json payload: %v", err)
	}
	if payload["error_code"] != "tools_not_delegable" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateTaskToolContractUsesCapabilityDiscovery(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{}
	if got := tool.Description(); !strings.Contains(got, "delegable=true") {
		t.Fatalf("expected description to declare capability discovery, got %q", got)
	}
	if got := string(tool.Parameters()); strings.Contains(got, "user_request_input") || strings.Contains(got, "report_finalize") {
		t.Fatalf("parameter schema must not encode tool-name special cases, got %q", got)
	}
}

func TestDelegateTaskToolRejectsMissingRuntimeInfrastructure(t *testing.T) {
	t.Parallel()

	registry := tools.NewRegistry()
	registry.Register(&runtimeAwareTestTool{})
	tool := &DelegateTaskTool{RegistryFactory: fixedRegistryFactory(registry)}
	args := json.RawMessage(`{
		"role_name":"observer",
		"task_instruction":"Inspect the available facts.",
		"allowed_tools":["runtime_aware_test_tool"]
	}`)
	assertCode := func(expected string) {
		result, err := tool.Execute(args)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(result), &payload); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		if payload["error_code"] != expected {
			t.Fatalf("expected %s, got %#v", expected, payload)
		}
	}

	assertCode("execution_context_missing")
	tool.SetExecutionContext(context.Background())
	assertCode("event_emitter_missing")
	tool.SetEventEmitter(func(RuntimeEvent) {})
	assertCode("child_run_persistence_missing")
}

func TestDelegateRuntimeHooksStayOnIndependentRegistryInstances(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}

	childEvents := 0
	childEmit := func(RuntimeEvent) { childEvents++ }
	childCtx := context.WithValue(context.Background(), ctxKey{}, "child")

	parentTool := &runtimeAwareTestTool{}
	childTool := &runtimeAwareTestTool{}
	childRegistry := tools.NewRegistry()
	childRegistry.Register(childTool)

	prepareRegistryRuntimeTools(childRegistry, childCtx, childEmit)
	if got := childTool.ctx.Value(ctxKey{}); got != "child" {
		t.Fatalf("expected child context before restore, got %#v", got)
	}
	childTool.emit(RuntimeEvent{})
	if childEvents != 1 {
		t.Fatalf("expected child emitter, got %d events", childEvents)
	}
	if parentTool.ctx != nil || parentTool.emit != nil {
		t.Fatalf("child hook preparation mutated independent parent tool: %#v", parentTool)
	}
}

func TestGlobalToolBuildersRejectInvalidAgentStateTypes(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("expected invalid agent state type to panic")
		}
	}()
	registry := tools.NewRegistry()
	registry.LoadGlobalTools(tools.ToolContext{Subgoals: mismatchedSubgoalChecker{}})
}

type mismatchedSubgoalChecker struct{}

func (mismatchedSubgoalChecker) CanFinalize() (bool, []string) { return false, nil }
