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
	emit func(WSEvent)
}

func (t *runtimeAwareTestTool) Name() string { return "runtime_aware_test_tool" }
func (t *runtimeAwareTestTool) Description() string {
	return "test runtime-aware tool"
}
func (t *runtimeAwareTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *runtimeAwareTestTool) Execute(json.RawMessage) (string, error) {
	return `{"ok":true}`, nil
}
func (t *runtimeAwareTestTool) SetEventEmitter(emit func(WSEvent)) {
	t.emit = emit
}
func (t *runtimeAwareTestTool) SetExecutionContext(ctx context.Context) {
	t.ctx = ctx
}

func TestDelegateTaskToolReturnsStructuredFailureWhenAllowedToolsUnresolved(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		BaseRegistry: tools.NewRegistry(),
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
	if payload["error_code"] != "no_allowed_tools_resolved" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
	if payload["delegate_role"] != "researcher" {
		t.Fatalf("expected delegate_role in payload: %#v", payload)
	}
}

func TestDelegateTaskToolReturnsStructuredFailureForMissingRoleName(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		BaseRegistry: tools.NewRegistry(),
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

func TestDelegateChildToolFailureReturnsStructuredPayload(t *testing.T) {
	t.Parallel()

	result := delegateChildToolFailure("data_query_sql", "boom")

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
		BaseRegistry: tools.NewRegistry(),
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
	if payload["error_code"] != "no_allowed_tools_resolved" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateTaskToolRejectsContextDumpInPolicyAppendix(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		BaseRegistry: tools.NewRegistry(),
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
	if payload["error_code"] != "policy_appendix_invalid" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateTaskToolRejectsDisallowedDelegateTools(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{
		BaseRegistry: tools.NewRegistry(),
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
	if payload["error_code"] != "disallowed_delegate_tools" {
		t.Fatalf("unexpected error_code: %#v", payload["error_code"])
	}
}

func TestDelegateTaskToolContractDeclaresDisallowedTools(t *testing.T) {
	t.Parallel()

	tool := &DelegateTaskTool{}
	if got := tool.Description(); !strings.Contains(got, "user_request_input") || !strings.Contains(got, "report_finalize") {
		t.Fatalf("expected description to declare disallowed tools, got %q", got)
	}
	if got := string(tool.Parameters()); !strings.Contains(got, "user_request_input") || !strings.Contains(got, "report_finalize") {
		t.Fatalf("expected parameter schema to declare disallowed tools, got %q", got)
	}
}

func TestDelegateTaskToolRestoresParentRuntimeHooks(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}

	parentEvents := 0
	childEvents := 0
	parentEmit := func(WSEvent) { parentEvents++ }
	childEmit := func(WSEvent) { childEvents++ }
	parentCtx := context.WithValue(context.Background(), ctxKey{}, "parent")
	childCtx := context.WithValue(context.Background(), ctxKey{}, "child")

	runtimeTool := &runtimeAwareTestTool{}
	registry := tools.NewRegistry()
	registry.Register(runtimeTool)

	delegate := &DelegateTaskTool{BaseRegistry: registry}
	prepareRegistryRuntimeTools(registry, childCtx, childEmit)
	if got := runtimeTool.ctx.Value(ctxKey{}); got != "child" {
		t.Fatalf("expected child context before restore, got %#v", got)
	}
	runtimeTool.emit(WSEvent{})
	if childEvents != 1 || parentEvents != 0 {
		t.Fatalf("expected child emitter before restore, parent=%d child=%d", parentEvents, childEvents)
	}

	delegate.restoreParentRegistryRuntimeTools(parentCtx, parentEmit)
	if got := runtimeTool.ctx.Value(ctxKey{}); got != "parent" {
		t.Fatalf("expected parent context after restore, got %#v", got)
	}
	runtimeTool.emit(WSEvent{})
	if parentEvents != 1 || childEvents != 1 {
		t.Fatalf("expected parent emitter after restore, parent=%d child=%d", parentEvents, childEvents)
	}
}
