package agent

import (
	"fmt"
	"strings"
)

// ConversationItem 表示历史通讯的单轮记录
type ConversationItem struct {
	Role                     string // user, assistant, or tool
	Content                  string
	ReasoningContent         string
	ToolCalls                []LLMToolCall
	ToolCallID               string
	ToolCallName             string
	ToolCallThoughtSignature string
}

func validatePromptBundle(bundle *PromptBundle) error {
	if bundle == nil {
		return fmt.Errorf("prompt bundle is required")
	}
	for index, block := range bundle.RuntimeContext {
		if block.Name == "" || block.Name != strings.TrimSpace(block.Name) {
			return fmt.Errorf("runtime context block %d name must be a non-empty exact value", index)
		}
		if block.Role != LLMRoleUser {
			return fmt.Errorf("runtime context block %d must use the user transport role", index)
		}
	}
	for index, item := range bundle.History {
		switch item.Role {
		case LLMRoleUser, LLMRoleAssistant, LLMRoleTool:
		default:
			return fmt.Errorf("history item %d has unsupported role %q", index, item.Role)
		}
	}
	return nil
}

// RuntimeContextBlock 表示在会话过程中因为摘要、事实等被动态注入的上下文
type RuntimeContextBlock struct {
	Name    string
	Role    string
	Content string
}

// PromptBundle 内部抽象的分层 Prompt 语义模型
type PromptBundle struct {
	Policy         string
	PolicyAppendix string
	Task           string
	RuntimeContext []RuntimeContextBlock
	History        []ConversationItem
}
