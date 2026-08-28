package agent

import "testing"

func TestValidatePromptBundleRejectsPolicyRoleInHistory(t *testing.T) {
	t.Parallel()

	err := validatePromptBundle(&PromptBundle{
		Policy:  "static policy",
		History: []ConversationItem{{Role: LLMRoleSystem, Content: "runtime facts"}},
	})
	if err == nil {
		t.Fatal("expected system role in history to be rejected")
	}
}

func TestValidatePromptBundleRequiresUserRuntimeTransport(t *testing.T) {
	t.Parallel()

	err := validatePromptBundle(&PromptBundle{
		Policy: "static policy",
		RuntimeContext: []RuntimeContextBlock{{
			Name:    "current_state",
			Role:    LLMRoleSystem,
			Content: `{"state":"observed"}`,
		}},
	})
	if err == nil {
		t.Fatal("expected runtime context system role to be rejected")
	}
}

func TestValidatePromptBundleAcceptsFourLayerContract(t *testing.T) {
	t.Parallel()

	err := validatePromptBundle(&PromptBundle{
		Policy: "static policy",
		Task:   "analyze the uploaded facts",
		RuntimeContext: []RuntimeContextBlock{{
			Name:    "current_state",
			Role:    LLMRoleUser,
			Content: `{"state":"observed"}`,
		}},
		History: []ConversationItem{
			{Role: LLMRoleUser, Content: "earlier task"},
			{Role: LLMRoleAssistant, Content: "earlier response"},
		},
	})
	if err != nil {
		t.Fatalf("expected valid layered prompt bundle, got %v", err)
	}
}
