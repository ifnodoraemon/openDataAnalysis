package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// ——————————————————————————————————————————————
// retryableToolExec 测试
// ——————————————————————————————————————————————

type mockRegistry struct {
	calls   int
	results []struct {
		result string
		err    error
	}
}

func (m *mockRegistry) Execute(name string, args json.RawMessage) (string, error) {
	if m.calls < len(m.results) {
		r := m.results[m.calls]
		m.calls++
		return r.result, r.err
	}
	m.calls++
	return "", errors.New("unexpected call")
}

// newMockRegistryWithSeq 创建一个按顺序返回结果的 mock（直接用 tools.Registry 包装）
// 为避免依赖 tools.Registry 内部，改用一个简单的 adapter
type retryTestRegistry struct {
	calls   int
	results []struct {
		result string
		err    error
	}
}

func (r *retryTestRegistry) toToolsRegistry() *tools.Registry {
	return nil // 占位，不实际用于此测试
}

// 由于 retryableToolExec 依赖 *tools.Registry，我们需要一个可控版本。
// 直接测试 isRetryableToolError 和公共逻辑，registry 集成测试留给 worker_test.go。

func TestIsRetryableToolError(t *testing.T) {
	t.Parallel()

	retryable := []error{
		io.EOF,
		&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
		&net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
	}
	for _, err := range retryable {
		if !isRetryableToolError(err) {
			t.Errorf("expected retryable for: %v", err)
		}
	}

	notRetryable := []string{
		"invalid arguments: missing field",
		"tool not found",
		"permission denied",
		"json unmarshal error",
	}
	for _, msg := range notRetryable {
		if isRetryableToolError(errors.New(msg)) {
			t.Errorf("expected non-retryable for: %q", msg)
		}
	}

	if isRetryableToolError(nil) {
		t.Fatal("nil error should not be retryable")
	}
}

func TestRetryableToolExecAbortsOnCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	reg := tools.NewRegistry()
	_, err := retryableToolExec(ctx, reg, "some_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

// ——————————————————————————————————————————————
// compactWorkerMessages 测试
// ——————————————————————————————————————————————

func TestCompactWorkerBundleNoOpBelowThreshold(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy: "system",
		Task:   "user task",
		History: []ConversationItem{
			{Role: LLMRoleAssistant, Content: "ok"},
		},
	}
	compactWorkerBundle(bundle, contextCompactTriggerTokens-1)
	if len(bundle.History) != 1 {
		t.Fatalf("expected no compaction below threshold, got %d messages in history", len(bundle.History))
	}
}

func TestCompactWorkerBundleNoOpShortHistory(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy:  "system",
		Task:    "user task",
		History: []ConversationItem{},
	}
	compactWorkerBundle(bundle, contextCompactTriggerTokens+1)
	if len(bundle.History) != 0 {
		t.Fatalf("expected empty history to be untouched, got %d", len(bundle.History))
	}
}

func TestCompactWorkerBundleCompactsLongHistory(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy:  "system",
		Task:    "user task instruction",
		History: []ConversationItem{},
	}
	for i := 0; i < 20; i++ {
		bundle.History = append(bundle.History,
			ConversationItem{Role: LLMRoleAssistant, Content: strings.Repeat("a", 200)},
			ConversationItem{Role: LLMRoleTool, Content: "tool result"},
		)
	}
	originalLen := len(bundle.History)

	compactWorkerBundle(bundle, contextCompactTriggerTokens+1)

	if len(bundle.History) >= originalLen {
		t.Fatalf("expected history to be compacted, got %d (original %d)", len(bundle.History), originalLen)
	}
	if len(bundle.RuntimeContext) == 0 || bundle.RuntimeContext[0].Name != "history_window" {
		t.Fatalf("expected structural history-window fact in runtime context")
	}
	if bundle.RuntimeContext[0].Role != "user" {
		t.Fatalf("expected history-window role=user, got %q", bundle.RuntimeContext[0].Role)
	}
}

func TestCompactWorkerBundlePreservesExistingRuntimeContext(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy: "system",
		Task:   "user task",
		RuntimeContext: []RuntimeContextBlock{
			{Name: "existing_fact", Role: "user", Content: `{"fact":"retained"}`},
		},
		History: []ConversationItem{},
	}
	for i := 0; i < 15; i++ {
		bundle.History = append(bundle.History,
			ConversationItem{Role: LLMRoleAssistant, Content: "assistant turn"},
			ConversationItem{Role: LLMRoleTool, Content: "tool result"},
		)
	}
	originalLen := len(bundle.History)

	compactWorkerBundle(bundle, contextCompactTriggerTokens+1)

	if len(bundle.History) >= originalLen {
		t.Fatalf("expected compaction with existing digest, got %d", len(bundle.History))
	}
	if len(bundle.RuntimeContext) == 0 {
		t.Fatal("expected runtime context blocks")
	}
	if bundle.RuntimeContext[0].Role != "user" {
		t.Fatalf("expected preserved runtime-context role=user, got %q", bundle.RuntimeContext[0].Role)
	}
	if bundle.RuntimeContext[0].Content != `{"fact":"retained"}` || bundle.RuntimeContext[len(bundle.RuntimeContext)-1].Name != "history_window" {
		t.Fatalf("expected existing facts plus structural compaction fact, got %#v", bundle.RuntimeContext)
	}
}
