package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func testImagePart() MessagePart {
	return ImageMessagePart("image/png", []byte{0x01, 0x02, 0x03}, 1200, 960)
}

// TestTextOnlyRequestsKeepTheirWireFormat is the regression guard for the whole
// multimodal change: a bundle without parts must serialize exactly as it did
// before, down to the message JSON. Anything else silently changes every request
// the runtime has ever sent.
func TestTextOnlyRequestsKeepTheirWireFormat(t *testing.T) {
	client := &LLMClient{model: "test-model"}
	bundle := &PromptBundle{
		Task: "task",
		History: []ConversationItem{
			{Role: LLMRoleUser, Content: "hi"},
			{Role: LLMRoleAssistant, Content: "yo", Parts: nil},
			{Role: LLMRoleTool, ToolCallID: "call_1", Content: "{}"},
		},
		RuntimeContext: []RuntimeContextBlock{{Name: "facts", Role: LLMRoleUser, Content: "line"}},
	}

	req, err := client.buildChatCompletionsRequest(bundle, nil)
	if err != nil {
		t.Fatalf("build chat completions request: %v", err)
	}
	for index, message := range req.Messages {
		raw, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal message %d: %v", index, err)
		}
		if strings.Contains(string(raw), `"content":[`) {
			t.Fatalf("message %d used the array content form without images: %s", index, raw)
		}
	}
	raw, err := json.Marshal(req.Messages[1])
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	if string(raw) != `{"role":"user","content":"hi"}` {
		t.Fatalf("text-only user message changed shape: %s", raw)
	}

	responsesReq, err := client.buildResponsesRequest(bundle, nil)
	if err != nil {
		t.Fatalf("build responses request: %v", err)
	}
	for index, input := range responsesReq.Input {
		if content, ok := input["content"]; ok {
			if _, isString := content.(string); !isString {
				t.Fatalf("responses input %d used a non-string content: %#v", index, content)
			}
		}
	}
}

func TestChatCompletionsMapsImagePartsToArrayContent(t *testing.T) {
	message := chatCompletionMessage{
		Role:    LLMRoleUser,
		Content: "view",
		Parts:   []MessagePart{testImagePart()},
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"view"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]}`
	if string(raw) != want {
		t.Fatalf("unexpected image message shape:\ngot:  %s\nwant: %s", raw, want)
	}
}

func TestChatCompletionsKeepsToolFieldsWithImages(t *testing.T) {
	// Tool bookkeeping must survive the array-form switch, or replay breaks.
	message := chatCompletionMessage{
		Role:       LLMRoleTool,
		Content:    "{}",
		ToolCallID: "call_1",
		Parts:      []MessagePart{testImagePart()},
	}
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if !strings.Contains(string(raw), `"tool_call_id":"call_1"`) {
		t.Fatalf("tool_call_id was lost in the array form: %s", raw)
	}
}

func TestResponsesMapsImagePartsToInputImage(t *testing.T) {
	client := &LLMClient{model: "test-model"}
	bundle := &PromptBundle{
		RuntimeContext: []RuntimeContextBlock{{
			Name:    "report_view",
			Role:    LLMRoleUser,
			Content: "report v17",
			Parts:   []MessagePart{testImagePart()},
		}},
	}
	req, err := client.buildResponsesRequest(bundle, nil)
	if err != nil {
		t.Fatalf("build responses request: %v", err)
	}
	if len(req.Input) != 1 {
		t.Fatalf("expected one input item, got %d", len(req.Input))
	}
	raw, err := json.Marshal(req.Input[0])
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	for _, want := range []string{`"type":"input_text"`, `"type":"input_image"`, `data:image/png;base64,AQID`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("responses input missing %s: %s", want, raw)
		}
	}
}

func TestAnthropicMapsImagePartsToBase64Source(t *testing.T) {
	parts := anthropicParts("view", []MessagePart{testImagePart()})
	if len(parts) != 2 {
		t.Fatalf("expected text and image blocks, got %d", len(parts))
	}
	if parts[0].Type == "" || parts[1].Source == nil {
		t.Fatalf("image block has no source: %#v", parts[1])
	}
	if parts[1].Source.MediaType != "image/png" {
		t.Fatalf("unexpected media type %q", parts[1].Source.MediaType)
	}
	raw, err := json.Marshal(parts[1])
	if err != nil {
		t.Fatalf("marshal image block: %v", err)
	}
	for _, want := range []string{`"type":"base64"`, `"media_type":"image/png"`, `"data":"AQID"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("anthropic image block missing %s: %s", want, raw)
		}
	}
}

func TestGeminiMapsImagePartsToInlineData(t *testing.T) {
	parts := geminiParts("view", []MessagePart{testImagePart()})
	if len(parts) != 2 {
		t.Fatalf("expected text and image parts, got %d", len(parts))
	}
	raw, err := json.Marshal(parts[1])
	if err != nil {
		t.Fatalf("marshal gemini part: %v", err)
	}
	want := `{"inlineData":{"mimeType":"image/png","data":"AQID"}}`
	if string(raw) != want {
		t.Fatalf("unexpected gemini image part:\ngot:  %s\nwant: %s", raw, want)
	}
}

// TestEstimateImageTokensAppliesProviderResize keeps compaction honest: pricing a
// 1500px slice by raw area would overstate it by ~55%, compacting history early
// for no reason.
func TestEstimateImageTokensAppliesProviderResize(t *testing.T) {
	// 1200x960 = 1.152 Mpx, 0.2% over the per-image budget, so the resize is
	// negligible: the slice costs the full 1534 tokens instead of being cut down.
	if got := estimateImageTokens(1200, 960); got != 1534 {
		t.Fatalf("1200x960 should cost 1534 tokens, got %d", got)
	}
	// 1200x1500 exceeds the budget, so it is scaled down and costs roughly the
	// same as an 1200x960 slice instead of 2400 tokens.
	tall := estimateImageTokens(1200, 1500)
	if tall > 1600 || tall < 1500 {
		t.Fatalf("1200x1500 should be capped near the budget, got %d", tall)
	}
	// A huge image cannot cost more than one budget-sized image.
	huge := estimateImageTokens(4000, 4000)
	if huge > 1600 {
		t.Fatalf("4000x4000 should be capped near the budget, got %d", huge)
	}
	if estimateImageTokens(0, 100) != 0 {
		t.Fatal("degenerate dimensions must cost nothing")
	}
}

func TestEstimatePromptTokensCountsImages(t *testing.T) {
	base := &PromptBundle{
		RuntimeContext: []RuntimeContextBlock{{Name: "report_view", Role: LLMRoleUser, Content: "v17"}},
	}
	withImage := &PromptBundle{
		RuntimeContext: []RuntimeContextBlock{{
			Name:    "report_view",
			Role:    LLMRoleUser,
			Content: "v17",
			Parts:   []MessagePart{testImagePart()},
		}},
	}
	delta := EstimatePromptTokens(withImage, nil) - EstimatePromptTokens(base, nil)
	if delta < 1400 || delta > 1700 {
		t.Fatalf("one 1200x960 image should add ~1536 tokens, got %d", delta)
	}
}

func TestValidateMessagePartsRejectsBadPayloads(t *testing.T) {
	cases := map[string][]MessagePart{
		"unsupported mime": {ImageMessagePart("image/gif", []byte{1}, 10, 10)},
		"missing data":     {ImageMessagePart("image/png", nil, 10, 10)},
		"missing size":     {ImageMessagePart("image/png", []byte{1}, 10, 0)},
		"unknown kind":     {{Kind: "video"}},
		"too large side":   {ImageMessagePart("image/png", []byte{1}, 8192, 10)},
	}
	for name, parts := range cases {
		if err := validateMessageParts(parts); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	if err := validateMessageParts([]MessagePart{testImagePart()}); err != nil {
		t.Fatalf("valid image part was rejected: %v", err)
	}
	if err := validateMessageParts(nil); err != nil {
		t.Fatalf("empty parts must be valid: %v", err)
	}
}

func TestValidatePromptBundleRejectsBadParts(t *testing.T) {
	bundle := &PromptBundle{
		RuntimeContext: []RuntimeContextBlock{{
			Name:  "report_view",
			Role:  LLMRoleUser,
			Parts: []MessagePart{ImageMessagePart("image/bmp", []byte{1}, 10, 10)},
		}},
	}
	if err := validatePromptBundle(bundle); err == nil {
		t.Fatal("expected validation error for unsupported image MIME")
	}
}
