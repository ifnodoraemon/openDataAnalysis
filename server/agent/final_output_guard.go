package agent

import "regexp"

// maxFinalMarkupRejects bounds how many times the engine may reject a final
// answer that leaks raw tool-call markup before falling back to accepting the
// content as-is (today's behavior) instead of looping.
const maxFinalMarkupRejects = 2

var rawToolMarkupPatterns = []*regexp.Regexp{
	// Fullwidth-pipe vendor tags, e.g. DeepSeek DSML: <｜DSML｜tool_calls>
	regexp.MustCompile(`</?｜[^>]{0,64}>`),
	// Halfwidth pipe tags naming tools, e.g. ChatML <|tool_calls|>
	regexp.MustCompile(`(?i)<\|[^|>]{0,32}(?:tool|invoke|dsml)[^|>]{0,32}\|>`),
	// XML-style tool invocation tags: <tool_call>, </invoke>, <function_calls>
	regexp.MustCompile(`(?i)</?(?:tool_calls?|function_calls?|invoke)\b[^>]*>`),
}

// containsRawToolMarkup reports whether content leaks inline tool-call markup.
// Some models occasionally emit their native tool-call dialect as visible text
// when they intend a structured tool call; such content is never a valid final
// answer. This is a thin final-output guardrail, not a dialect parser: the
// model is asked to restate cleanly or issue a proper tool call.
func containsRawToolMarkup(content string) bool {
	for _, re := range rawToolMarkupPatterns {
		if re.MatchString(content) {
			return true
		}
	}
	return false
}

// finalMarkupCorrectionMessage is appended to history after a rejected final
// answer so the model can correct itself on the next turn.
const finalMarkupCorrectionMessage = "[system_feedback] Your previous message contained raw tool-call markup (e.g. <｜…｜> tags or tool_call/invoke blocks) instead of a structured tool call, so it was rejected as a final answer. If you still need a tool, call it through the normal tool-call mechanism. Otherwise restate your final answer as plain text without any tool-call markup."

// finalMarkupGuardDecision returns whether a candidate final answer should be
// rejected for raw tool markup, given how many rejections already happened.
// After the bound is reached the content is accepted as-is rather than
// looping forever.
func finalMarkupGuardDecision(content string, rejects int) (reject bool, rejectsAfter int) {
	if rejects >= maxFinalMarkupRejects || !containsRawToolMarkup(content) {
		return false, rejects
	}
	return true, rejects + 1
}
