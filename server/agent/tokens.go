package agent

import (
	"unicode/utf8"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

const (
	// defaultContextWindowTokens is used when no model window is configured.
	defaultContextWindowTokens = 128000
	// contextCompactTriggerRatio is the fraction of the window at which history
	// is compacted, leaving room for the response and for estimation error.
	contextCompactTriggerRatio = 9
	// promptMessageOverheadTokens approximates the per-message framing cost that
	// providers add on top of the message text.
	promptMessageOverheadTokens = 4
)

// contextWindowTokens returns the configured model context window. The window
// belongs to the deployed model, not to the runtime, so it is configuration
// rather than a constant.
func contextWindowTokens() int {
	if config.Cfg != nil && config.Cfg.LLMContextWindowTokens > 0 {
		return config.Cfg.LLMContextWindowTokens
	}
	return defaultContextWindowTokens
}

// contextCompactTriggerTokens is the token pressure at which history is
// compacted.
func contextCompactTriggerTokens() int {
	return contextWindowTokens() * contextCompactTriggerRatio / 10
}

// estimateTextTokens approximates the token count of a text fragment.
//
// ASCII text is roughly four characters per token. Non-ASCII text, which for
// this product means mostly Chinese, is close to one token per rune; pricing it
// at four characters per token would understate the real footprint by a large
// factor. Provider-reported usage stays authoritative whenever it is available;
// this estimate exists so context pressure is still detected when a gateway
// omits usage.
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii, wide := 0, 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			wide++
		}
	}
	return (ascii+3)/4 + wide
}

// EstimatePromptTokens estimates the token footprint of one model request,
// including the tool schemas that travel with it.
func EstimatePromptTokens(bundle *PromptBundle, toolSpecs []tools.ToolSpec) int {
	if bundle == nil {
		return 0
	}
	total := estimateTextTokens(bundle.Policy) + estimateTextTokens(bundle.PolicyAppendix) + estimateTextTokens(bundle.Task)
	for _, block := range bundle.RuntimeContext {
		total += promptMessageOverheadTokens + estimateTextTokens(block.Name) + estimateTextTokens(block.Content)
	}
	for _, item := range bundle.History {
		total += promptMessageOverheadTokens + estimateTextTokens(item.Content)
		// Reasoning text is replayed to providers that accept it, so it is part
		// of the prompt footprint.
		total += estimateTextTokens(item.ReasoningContent)
		for _, call := range item.ToolCalls {
			total += estimateTextTokens(call.Function.Name) + estimateTextTokens(call.Function.Arguments)
		}
	}
	for _, spec := range toolSpecs {
		total += estimateTextTokens(spec.Function.Name) + estimateTextTokens(spec.Function.Description) + estimateTextTokens(string(spec.Function.Parameters))
	}
	return total
}
