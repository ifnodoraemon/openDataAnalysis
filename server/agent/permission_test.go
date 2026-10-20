package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type gatedRunTestTool struct {
	calls int
}

func (t *gatedRunTestTool) Name() string        { return "gated_run_test_tool" }
func (t *gatedRunTestTool) Description() string { return "test tool that requires user authorization" }
func (t *gatedRunTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"confirmation_receipt_id":{"type":"string"}}}`)
}
func (t *gatedRunTestTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "action", RuntimeEnabled: true}
}
func (t *gatedRunTestTool) ApprovalReceiptID(args json.RawMessage) string {
	var params struct {
		ReceiptID string `json:"confirmation_receipt_id"`
	}
	if err := json.Unmarshal(args, &params); err != nil {
		return ""
	}
	return params.ReceiptID
}
func (t *gatedRunTestTool) ApprovalRequest(json.RawMessage) (tools.ApprovalRequest, error) {
	return tools.ApprovalRequest{Action: "gated_action", ResourceRef: "res_1", PayloadJSON: `{"a":1}`}, nil
}
func (t *gatedRunTestTool) Execute(json.RawMessage) (string, error) {
	t.calls++
	return `{"ok":true,"tool":"gated_run_test_tool"}`, nil
}

func newReceiptEngine(t *testing.T) (*Engine, *gatedRunTestTool) {
	t.Helper()
	tool := &gatedRunTestTool{}
	registry := tools.NewRegistry()
	registry.Register(tool)
	return &Engine{registry: registry, policy: "system", confirmationReceipts: map[string]*ConfirmationReceipt{}}, tool
}

func (e *Engine) putReceipt(receipt *ConfirmationReceipt) {
	e.confirmationReceipts[receipt.ID] = receipt
}

func validReceipt(overrides map[string]interface{}) *ConfirmationReceipt {
	hash, _ := authorizationPayloadHash(`{"a":1}`)
	receipt := &ConfirmationReceipt{
		ID:          "ucr_test",
		Action:      "gated_action",
		ResourceRef: "res_1",
		PayloadHash: hash,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(confirmationReceiptTTL),
	}
	for key, value := range overrides {
		switch key {
		case "id":
			receipt.ID = value.(string)
		case "action":
			receipt.Action = value.(string)
		case "resource":
			receipt.ResourceRef = value.(string)
		case "hash":
			receipt.PayloadHash = value.(string)
		case "expired":
			receipt.ExpiresAt = time.Now().Add(-time.Minute)
		case "consumed":
			now := time.Now()
			receipt.ConsumedAt = &now
		}
	}
	return receipt
}

func TestVerifyApprovalAcceptsMatchingReceipt(t *testing.T) {
	t.Parallel()

	engine, _ := newReceiptEngine(t)
	engine.putReceipt(validReceipt(nil))

	request := tools.ApprovalRequest{Action: "gated_action", ResourceRef: "res_1", PayloadJSON: `{"a":1}`}
	if err := engine.VerifyApproval("ucr_test", request); err != nil {
		t.Fatalf("expected a matching receipt to verify: %v", err)
	}
}

// Every mismatch fails closed: the call must not be admitted.
func TestVerifyApprovalRejectsEveryMismatch(t *testing.T) {
	t.Parallel()

	request := tools.ApprovalRequest{Action: "gated_action", ResourceRef: "res_1", PayloadJSON: `{"a":1}`}
	cases := []struct {
		name    string
		receipt *ConfirmationReceipt
		id      string
	}{
		{name: "unknown receipt", receipt: nil, id: "ucr_missing"},
		{name: "wrong action", receipt: validReceipt(map[string]interface{}{"action": "other_action"}), id: "ucr_test"},
		{name: "wrong resource", receipt: validReceipt(map[string]interface{}{"resource": "res_2"}), id: "ucr_test"},
		{name: "different payload", receipt: validReceipt(map[string]interface{}{"hash": "deadbeef"}), id: "ucr_test"},
		{name: "expired", receipt: validReceipt(map[string]interface{}{"expired": true}), id: "ucr_test"},
		{name: "already consumed", receipt: validReceipt(map[string]interface{}{"consumed": true}), id: "ucr_test"},
		{name: "empty id", receipt: validReceipt(nil), id: ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			engine, _ := newReceiptEngine(t)
			if tc.receipt != nil {
				engine.putReceipt(tc.receipt)
			}
			if err := engine.VerifyApproval(tc.id, request); err == nil {
				t.Fatalf("expected verification to fail for %s", tc.name)
			}
		})
	}
}

func TestVerifyApprovalRejectsWhenNoStoreExists(t *testing.T) {
	t.Parallel()

	engine := &Engine{registry: tools.NewRegistry(), policy: "system"}
	request := tools.ApprovalRequest{Action: "gated_action", ResourceRef: "res_1", PayloadJSON: `{"a":1}`}
	if err := engine.VerifyApproval("ucr_test", request); err == nil {
		t.Fatalf("an engine without a receipt store must not admit a gated call")
	}
}

func TestPurgeExpiredReceiptsDropsOnlyExpired(t *testing.T) {
	t.Parallel()

	engine, _ := newReceiptEngine(t)
	engine.putReceipt(validReceipt(nil))
	expired := validReceipt(map[string]interface{}{"expired": true, "id": "ucr_expired"})
	engine.putReceipt(expired)

	engine.mu.Lock()
	engine.purgeExpiredReceiptsLocked(time.Now())
	remaining := len(engine.confirmationReceipts)
	engine.mu.Unlock()

	if remaining != 1 {
		t.Fatalf("expected only the valid receipt to survive, got %d", remaining)
	}
	if _, ok := engine.confirmationReceipts["ucr_test"]; !ok {
		t.Fatalf("the valid receipt must be kept")
	}
}

// A gated call without a receipt must never reach the tool body, and the model
// must receive a structured, actionable failure.
func TestRunBlocksGatedToolWithoutReceipt(t *testing.T) {
	server, _ := scriptedLLMServer(t, []string{
		responsesAPIToolCallBody(t, "gated_run_test_tool"),
		responsesAPITextBody(t, "已请求授权后再试。"),
	})
	installTestLLMConfig(t, server)

	engine, tool := newReceiptEngine(t)
	engine.llm = NewLLMClient()

	var events []RuntimeEvent
	engine.Run(context.Background(), "改一下画像", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	if tool.calls != 0 {
		t.Fatalf("a gated call without a receipt must never execute, got %d calls", tool.calls)
	}

	var denied *ApprovalDeniedData
	for _, ev := range events {
		if ev.Type == EventApprovalDenied {
			data := ev.Data.(ApprovalDeniedData)
			denied = &data
		}
	}
	if denied == nil {
		t.Fatalf("expected an approval_denied event, got %#v", events)
	}
	if denied.Decision != string(tools.AdmissionAsk) {
		t.Fatalf("expected an ask decision, got %q", denied.Decision)
	}
	if denied.Tool != "gated_run_test_tool" {
		t.Fatalf("unexpected tool in the denial: %q", denied.Tool)
	}

	var blocked *ConversationItem
	for _, item := range engine.History() {
		if item.Role == LLMRoleTool {
			candidate := item
			blocked = &candidate
		}
	}
	if blocked == nil || !strings.Contains(blocked.Content, "approval_required") {
		t.Fatalf("expected an approval_required tool result in history, got %#v", engine.History())
	}
}

// With a verified receipt the same call executes normally.
func TestRunExecutesGatedToolWithVerifiedReceipt(t *testing.T) {
	server, _ := scriptedLLMServer(t, []string{
		responsesAPIToolCallBodyWithArgs(t, "gated_run_test_tool", `{"confirmation_receipt_id":"ucr_test"}`),
		responsesAPITextBody(t, "已完成。"),
	})
	installTestLLMConfig(t, server)

	engine, tool := newReceiptEngine(t)
	engine.llm = NewLLMClient()
	engine.putReceipt(validReceipt(nil))

	engine.Run(context.Background(), "改一下画像", nil, func(RuntimeEvent) {})

	if tool.calls != 1 {
		t.Fatalf("expected the authorized call to execute once, got %d", tool.calls)
	}
}

func responsesAPIToolCallBodyWithArgs(t *testing.T, toolName, arguments string) string {
	t.Helper()
	payload := map[string]interface{}{
		"id":     "resp_tool",
		"status": "completed",
		"model":  "gpt-test",
		"output": []map[string]interface{}{
			{
				"type":      "function_call",
				"id":        "fc_1",
				"call_id":   "call_1",
				"name":      toolName,
				"arguments": arguments,
			},
		},
		"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode responses api body: %v", err)
	}
	return string(encoded)
}
