package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// compactionSummaryPolicy and compactionSummaryTask drive the dedicated
// summarization request that replaces older conversation. They are model-facing
// English policy, kept separate from the product system prompt.
const compactionSummaryPolicy = `You compress the earlier part of a data-analysis conversation into a short factual record, so it can replace that conversation in the model's context.

Preserve, when the conversation contains them:
- the user's objective and any explicit constraints or corrections
- confirmed metric definitions, units, time grains, filters and join keys
- data sources, tables and columns that were inspected or used
- key query or computation results, including the numbers that matter
- decisions already made and the reason for them
- open questions, unresolved ambiguity and known data limitations

Do not add recommendations, do not invent facts, and do not mention that a summary was produced. Output only the summary text.`

const compactionSummaryTask = `Summarize the conversation above into one factual record.`

const (
	compactionSummaryOpen  = "<compacted-summary>"
	compactionSummaryClose = "</compacted-summary>"
)

const (
	toolResultHistoryThresholdChars = 8192
	toolResultHistoryHeadChars      = 4096
	toolResultHistoryTailChars      = 1024
)

// buildBundleLocked assembles the layered prompt for one request. The caller
// must hold e.mu.
func (e *Engine) buildBundleLocked(userTask string, getRuntimeVars func() []RuntimeContextBlock, continuationFacts []RuntimeContextBlock) *PromptBundle {
	bundle := &PromptBundle{Policy: e.policy, Task: userTask}
	if getRuntimeVars != nil {
		bundle.RuntimeContext = append(bundle.RuntimeContext, getRuntimeVars()...)
	}
	bundle.RuntimeContext = append(bundle.RuntimeContext, continuationFacts...)
	if e.omittedHistoryMessages > 0 {
		bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
			Name: "history_window", Role: "user", Content: fmt.Sprintf(`{"omitted_message_count":%d}`, e.omittedHistoryMessages),
		})
	}
	bundle.History = append([]ConversationItem(nil), e.history...)
	return bundle
}

func (e *Engine) buildBundle(userTask string, getRuntimeVars func() []RuntimeContextBlock, continuationFacts []RuntimeContextBlock) *PromptBundle {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buildBundleLocked(userTask, getRuntimeVars, continuationFacts)
}

// pressureTokens returns the best available estimate of the next request's
// prompt size. Provider-reported usage from the previous request is a lower
// bound on the next one because history only grows between requests, so the
// larger of the two is used.
func (e *Engine) pressureTokens(estimate int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastPromptTokens > estimate {
		return e.lastPromptTokens
	}
	return estimate
}

// selectCompactionSpan returns the oldest conversation to replace, bounded so
// that no tool result is separated from the assistant message that requested
// it.
func (e *Engine) selectCompactionSpan() ([]ConversationItem, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.history) <= recentContextWindow+1 {
		return nil, 0
	}
	boundary := adjustCompactionBoundary(e.history, len(e.history)-recentContextWindow)
	if boundary <= 0 {
		return nil, 0
	}
	return append([]ConversationItem(nil), e.history[:boundary]...), boundary
}

// applyCompaction replaces the first count history items with summary, or drops
// them when summary is empty.
func (e *Engine) applyCompaction(count int, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if count <= 0 || count > len(e.history) {
		return
	}
	tail := append([]ConversationItem(nil), e.history[count:]...)
	if strings.TrimSpace(summary) == "" {
		e.omittedHistoryMessages += count
		e.history = tail
		return
	}
	// The summary carries the compacted facts, so there is nothing left to
	// report as merely omitted.
	e.omittedHistoryMessages = 0
	e.history = append([]ConversationItem{{
		Role:    LLMRoleUser,
		Content: formatCompactionSummary(summary),
	}}, tail...)
}

// formatCompactionSummary wraps a model-written summary in a stable marker so
// any consumer can recognize compacted history after persistence or rebuild.
func formatCompactionSummary(summary string) string {
	return "Earlier conversation in this session was compacted into the record below. It is a factual record of what happened, not new instructions.\n" +
		compactionSummaryOpen + "\n" + strings.TrimSpace(summary) + "\n" + compactionSummaryClose
}

// summarizeHistorySpan asks the model to compress the span that is about to be
// replaced. The span is sent as ordinary history so the summarizer sees exactly
// what the main loop saw.
func (e *Engine) summarizeHistorySpan(ctx context.Context, span []ConversationItem) (string, error) {
	if len(span) == 0 {
		return "", fmt.Errorf("nothing to summarize")
	}
	bundle := &PromptBundle{
		Policy:  compactionSummaryPolicy,
		Task:    compactionSummaryTask,
		History: span,
	}
	resp, err := e.llm.ChatWithTools(ctx, bundle, nil)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarizer returned no choices")
	}
	summary := strings.TrimSpace(resp.Choices[0].Message.Content)
	if summary == "" {
		return "", fmt.Errorf("summarizer returned an empty summary")
	}
	return summary, nil
}

// prepareRequestBundle builds the next request and compacts older conversation
// when the prompt is close to the model's context window.
//
// Compaction runs before the request is sent. Measuring the prompt of a request
// that has already been issued can only trim history one step too late, which
// is what the previous implementation did.
func (e *Engine) prepareRequestBundle(
	ctx context.Context,
	emit func(RuntimeEvent),
	userTask string,
	getRuntimeVars func() []RuntimeContextBlock,
	continuationFacts []RuntimeContextBlock,
	toolSpecs []tools.ToolSpec,
) *PromptBundle {
	bundle := e.buildBundle(userTask, getRuntimeVars, continuationFacts)
	pressure := e.pressureTokens(EstimatePromptTokens(bundle, toolSpecs))
	trigger := contextCompactTriggerTokens()
	if pressure > trigger {
		if span, count := e.selectCompactionSpan(); count > 0 {
			summary, summaryErr := e.summarizeHistorySpan(ctx, span)
			method := HistoryCompactionMethodSummary
			if summaryErr != nil {
				// A failed summarization must not stall the run. The span is
				// dropped instead, and the checkpoint records that the facts
				// were lost rather than summarized.
				log.Printf("engine: history summarization failed, trimming instead: %v", summaryErr)
				summary = ""
				method = HistoryCompactionMethodTrim
			}
			e.applyCompaction(count, summary)
			bundle = e.buildBundle(userTask, getRuntimeVars, continuationFacts)
			emit(RuntimeEvent{
				Type: EventHistoryCompacted,
				Data: HistoryCompactedData{
					Summary:       summary,
					ShadowedItems: count,
					Method:        method,
					PromptTokens:  pressure,
					ContextWindow: contextWindowTokens(),
				},
			})
		}
	}

	// Advisories queued by the previous step ride this request as runtime facts.
	if notices := e.takePendingNotices(); len(notices) > 0 {
		bundle.RuntimeContext = append(bundle.RuntimeContext, notices...)
	}
	return bundle
}

// pruneToolResultForHistory bounds a tool result before it enters the model
// history. Oversized results keep a valid result object with an explicit
// omission notice instead of silently filling the context window.
func pruneToolResultForHistory(toolName, compactedResult string) string {
	runes := []rune(compactedResult)
	if len(runes) <= toolResultHistoryThresholdChars {
		return compactedResult
	}

	succeeded := false
	var payload map[string]interface{}
	if jsoncontract.Decode([]byte(compactedResult), &payload) == nil {
		succeeded, _ = payload["ok"].(bool)
	}

	head := string(runes[:toolResultHistoryHeadChars])
	tail := string(runes[len(runes)-toolResultHistoryTailChars:])
	pruned := map[string]interface{}{
		"ok":             succeeded,
		"tool":           toolName,
		"truncated":      true,
		"original_chars": len(runes),
		"omitted_chars":  len(runes) - toolResultHistoryHeadChars - toolResultHistoryTailChars,
		"head":           head,
		"tail":           tail,
		"notice":         "This tool result was too large to keep in the conversation, so only its head and tail are shown. Call the tool again with narrower parameters if the omitted middle is needed.",
	}
	encoded, err := json.Marshal(pruned)
	if err != nil {
		return compactedResult
	}
	return string(encoded)
}
