package agent

const (
	LLMRoleSystem    = "system"
	LLMRoleUser      = "user"
	LLMRoleAssistant = "assistant"
	LLMRoleTool      = "tool"
)

const (
	LLMFinishReasonStop      = "stop"
	LLMFinishReasonToolCalls = "tool_calls"
)

const (
	LLMToolTypeFunction = "function"
)

type LLMFunctionCall struct {
	Name             string
	Arguments        string
	ThoughtSignature string
}

type LLMToolCall struct {
	ID       string
	Type     string
	Function LLMFunctionCall
}

type LLMMessage struct {
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        []LLMToolCall
}

type LLMChoice struct {
	Index        int
	Message      LLMMessage
	FinishReason string
}

type LLMUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type LLMResponse struct {
	Choices []LLMChoice
	Usage   LLMUsage
}
