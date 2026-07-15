package agent

// PromptLayer 定义了 Prompt 的层级语义
type PromptLayer string

const (
	LayerPolicy         PromptLayer = "policy"
	LayerTask           PromptLayer = "task"
	LayerRuntimeContext PromptLayer = "runtime_context"
	LayerHistory        PromptLayer = "history"
)

// ConversationItem 表示历史通讯的单轮记录
type ConversationItem struct {
	Role             string // "user", "assistant", "system" (for tool)
	Content          string
	ReasoningContent string
	ToolCalls              []LLMToolCall
	ToolCallID             string
	ToolCallName           string
	ToolCallThoughtSignature string
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
