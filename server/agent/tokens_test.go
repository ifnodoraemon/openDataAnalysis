package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func TestEstimateTextTokensPricesWideText(t *testing.T) {
	t.Parallel()

	// ASCII is priced at roughly four characters per token.
	if got := estimateTextTokens(strings.Repeat("a", 400)); got != 100 {
		t.Fatalf("expected 100 tokens for 400 ASCII characters, got %d", got)
	}
	// Non-ASCII text (Chinese) is close to one token per rune, so pricing it at
	// four characters per token would understate it by roughly 4x.
	chinese := strings.Repeat("销售额", 100)
	if got := estimateTextTokens(chinese); got != 300 {
		t.Fatalf("expected 300 tokens for 300 Chinese runes, got %d", got)
	}
	if estimateTextTokens("") != 0 {
		t.Fatalf("empty text must estimate to zero tokens")
	}
}

func TestEstimatePromptTokensIncludesHistoryAndTools(t *testing.T) {
	t.Parallel()

	bundle := &PromptBundle{
		Policy: strings.Repeat("p", 40),
		Task:   strings.Repeat("t", 40),
		RuntimeContext: []RuntimeContextBlock{
			{Name: "ctx", Role: LLMRoleUser, Content: strings.Repeat("c", 40)},
		},
		History: []ConversationItem{
			{Role: LLMRoleUser, Content: strings.Repeat("h", 40)},
			{
				Role: LLMRoleAssistant,
				ToolCalls: []LLMToolCall{{
					ID:       "call_1",
					Type:     LLMToolTypeFunction,
					Function: LLMFunctionCall{Name: "data_query_sql", Arguments: strings.Repeat("a", 40)},
				}},
			},
		},
	}
	specs := []tools.ToolSpec{{
		Type: "function",
		Function: tools.FunctionSpec{
			Name:        "data_query_sql",
			Description: strings.Repeat("d", 40),
			Parameters:  []byte(strings.Repeat("s", 40)),
		},
	}}

	withTools := EstimatePromptTokens(bundle, specs)
	withoutTools := EstimatePromptTokens(bundle, nil)
	if withTools <= withoutTools {
		t.Fatalf("tool schemas must count toward the estimate: %d vs %d", withTools, withoutTools)
	}

	empty := EstimatePromptTokens(&PromptBundle{}, nil)
	if empty != 0 {
		t.Fatalf("expected zero tokens for an empty bundle, got %d", empty)
	}
	if EstimatePromptTokens(nil, specs) != 0 {
		t.Fatalf("nil bundle must estimate to zero")
	}
}

func TestPruneToolResultForHistoryBoundsOversizedResults(t *testing.T) {
	t.Parallel()

	small := `{"ok":true,"tool":"data_query_sql","rows":[]}`
	if got := pruneToolResultForHistory("data_query_sql", small); got != small {
		t.Fatalf("small results must pass through unchanged, got %q", got)
	}

	large := `{"ok":true,"tool":"data_query_sql","rows":["` + strings.Repeat("x", 20000) + `"]}`
	pruned := pruneToolResultForHistory("data_query_sql", large)

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(pruned), &payload); err != nil {
		t.Fatalf("pruned result must stay valid JSON: %v", err)
	}
	if payload["ok"] != true || payload["tool"] != "data_query_sql" {
		t.Fatalf("the result contract fields must survive pruning: %#v", payload)
	}
	if payload["truncated"] != true {
		t.Fatalf("expected an explicit truncation flag: %#v", payload)
	}
	if _, ok := payload["notice"].(string); !ok {
		t.Fatalf("expected an omission notice: %#v", payload)
	}
	if len([]rune(pruned)) >= len([]rune(large)) {
		t.Fatalf("pruned result must be smaller than the original")
	}
}

func TestPruneToolResultForHistoryPreservesFailureFlag(t *testing.T) {
	t.Parallel()

	large := `{"ok":false,"tool":"code_run_python","error_code":"execution_error","message":"` + strings.Repeat("e", 20000) + `"}`
	pruned := pruneToolResultForHistory("code_run_python", large)

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(pruned), &payload); err != nil {
		t.Fatalf("pruned result must stay valid JSON: %v", err)
	}
	if payload["ok"] != false {
		t.Fatalf("a failed result must not be reported as successful: %#v", payload)
	}
}

func TestContextWindowFollowsConfiguration(t *testing.T) {
	// Not parallel: this swaps the process-wide config, which parallel tests
	// also read.
	previous := config.Cfg
	t.Cleanup(func() { config.Cfg = previous })

	config.Cfg = &config.Config{LLMContextWindowTokens: 64000}
	if got := contextWindowTokens(); got != 64000 {
		t.Fatalf("expected the configured window, got %d", got)
	}
	if got := contextCompactTriggerTokens(); got != 57600 {
		t.Fatalf("expected the trigger at 90%% of the window, got %d", got)
	}

	config.Cfg = &config.Config{}
	if got := contextWindowTokens(); got != defaultContextWindowTokens {
		t.Fatalf("expected the default window when unset, got %d", got)
	}
}
