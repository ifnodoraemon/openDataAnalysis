package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

	retryable := []string{
		"context deadline exceeded: connection timeout",
		"connection refused",
		"TLS handshake timeout",
		"i/o timeout",
		"EOF",
		"read tcp xxx: connection reset by peer",
	}
	for _, msg := range retryable {
		if !isRetryableToolError(errors.New(msg)) {
			t.Errorf("expected retryable for: %q", msg)
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
	if len(bundle.RuntimeContext) == 0 || bundle.RuntimeContext[0].Name != "digest" {
		t.Fatalf("expected history digest in runtime context")
	}
	if bundle.RuntimeContext[0].Role != "user" {
		t.Fatalf("expected digest role=user, got %q", bundle.RuntimeContext[0].Role)
	}
}

func TestCompactWorkerBundlePreservesExistingDigest(t *testing.T) {
	t.Parallel()

	existDigest := historyDigestPrefix + "\n- user: early task\n- tool result: ok"
	bundle := &PromptBundle{
		Policy: "system",
		Task:   "user task",
		RuntimeContext: []RuntimeContextBlock{
			{Name: "digest", Role: "user", Content: existDigest},
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
		t.Fatal("expected digest block")
	}
	if bundle.RuntimeContext[0].Role != "user" {
		t.Fatalf("expected preserved digest role=user, got %q", bundle.RuntimeContext[0].Role)
	}
	if !strings.Contains(bundle.RuntimeContext[0].Content, "early task") {
		t.Fatalf("expected new digest to preserve previous content")
	}
}

// ——————————————————————————————————————————————
// sanitizeReportHTML 测试
// ——————————————————————————————————————————————

func TestSanitizeReportHTMLStripsEventAttributes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input    string
		mustDrop string
	}{
		{`<img src="x" onerror="alert(1)">`, `onerror`},
		{`<div onclick="steal()" class="foo">`, `onclick`},
		{`<body onload='evil()'>`, `onload`},
		{`<a href="#" onmouseover="x=1">link</a>`, `onmouseover`},
	}

	for _, tc := range cases {
		result := sanitizeReportHTML(tc.input)
		if strings.Contains(strings.ToLower(result), strings.ToLower(tc.mustDrop)) {
			t.Errorf("sanitizeReportHTML failed to strip %q from %q, got %q", tc.mustDrop, tc.input, result)
		}
	}
}

func TestSanitizeReportHTMLStripsJavascriptHref(t *testing.T) {
	t.Parallel()

	cases := []string{
		`<a href="javascript:alert(1)">click</a>`,
		`<a href="javascript:void(0)">click</a>`,
		`<img src="javascript:evil()">`,
	}
	for _, input := range cases {
		result := sanitizeReportHTML(input)
		lower := strings.ToLower(result)
		if strings.Contains(lower, "javascript:") {
			t.Errorf("sanitizeReportHTML failed to remove javascript: in %q, got %q", input, result)
		}
	}
}

func TestSanitizeReportHTMLPreservesNormalContent(t *testing.T) {
	t.Parallel()

	input := `<div class="chart"><script>var x=1;</script><style>.a{color:red}</style><a href="https://example.com">link</a></div>`
	result := sanitizeReportHTML(input)

	if strings.Contains(strings.ToLower(result), "<script") || strings.Contains(result, "var x=1") {
		t.Errorf("sanitizeReportHTML should remove script tags; ECharts loads through report runtime assets, got %q", result)
	}
	if !strings.Contains(result, "<style>") {
		t.Error("sanitizeReportHTML should preserve style tags")
	}
	if !strings.Contains(result, `href="https://example.com"`) {
		t.Error("sanitizeReportHTML should preserve legitimate https links")
	}
	if !strings.Contains(result, `class="chart"`) {
		t.Error("sanitizeReportHTML should preserve class attributes")
	}
}

func TestApplyReportHTMLGuardrailSanitizesHTMLField(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		"ok":   true,
		"tool": "report_finalize",
		"html": `<div onclick="evil()"><a href="javascript:void(0)">bad</a></div>`,
	}
	raw, _ := json.Marshal(payload)
	result := applyReportHTMLGuardrail(string(raw))

	var out map[string]interface{}
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		t.Fatalf("expected valid JSON, got: %v", err)
	}
	htmlOut, ok := out["html"].(string)
	if !ok {
		t.Fatal("expected html field in result")
	}
	if strings.Contains(strings.ToLower(htmlOut), "onclick") {
		t.Errorf("expected onclick to be stripped, got: %q", htmlOut)
	}
	if strings.Contains(strings.ToLower(htmlOut), "javascript:") {
		t.Errorf("expected javascript: to be stripped, got: %q", htmlOut)
	}
}

func TestApplyReportHTMLGuardrailPassthroughOnInvalidJSON(t *testing.T) {
	t.Parallel()

	invalid := `not json`
	result := applyReportHTMLGuardrail(invalid)
	if result != invalid {
		t.Fatalf("expected passthrough for invalid JSON, got: %q", result)
	}
}
