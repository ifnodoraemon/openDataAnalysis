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
func (e *Engine) buildBundleLocked(userTask string, getRuntimeVars func() []RuntimeContextBlock, continuationFacts []RuntimeContextBlock, view *ReportView) *PromptBundle {
	bundle := &PromptBundle{Policy: e.policy, Task: userTask}
	if getRuntimeVars != nil {
		bundle.RuntimeContext = append(bundle.RuntimeContext, getRuntimeVars()...)
	}
	bundle.RuntimeContext = append(bundle.RuntimeContext, continuationFacts...)
	if e.historySummary != "" {
		bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
			Name:    "history_summary",
			Role:    LLMRoleUser,
			Content: formatCompactionSummary(e.historySummary),
		})
	}
	if e.omittedHistoryMessages > 0 {
		bundle.RuntimeContext = append(bundle.RuntimeContext, RuntimeContextBlock{
			Name: "history_window", Role: "user", Content: fmt.Sprintf(`{"omitted_message_count":%d}`, e.omittedHistoryMessages),
		})
	}
	// Finished background jobs ride the request as facts, so a job that ends
	// between steps is noticed without the model having to poll for it. Taking
	// them here clears the queue: each completion is announced exactly once.
	if e.jobs != nil {
		bundle.RuntimeContext = append(bundle.RuntimeContext, e.jobs.TakeCompletedNotices()...)
	}
	// The report view goes last: it changes on every capture, so placing it
	// earlier would invalidate the provider's prefix cache for everything after
	// it. A nil view is the normal state before the first capture completes.
	if view != nil {
		bundle.RuntimeContext = append(bundle.RuntimeContext, view.RuntimeContextBlocks()...)
	}
	bundle.History = append([]ConversationItem(nil), e.history...)
	return bundle
}

func (e *Engine) buildBundle(userTask string, getRuntimeVars func() []RuntimeContextBlock, continuationFacts []RuntimeContextBlock, view *ReportView) *PromptBundle {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.buildBundleLocked(userTask, getRuntimeVars, continuationFacts, view)
}

// takeReportView fetches the view for one request.
//
// buildBundle runs twice when history is compacted (before and after), and the
// provider hands views out with delivery semantics: calling it twice would
// consume the view on the build that is thrown away, so the request that is
// actually sent would carry no images. Taking it once, up front, avoids that.
func (e *Engine) takeReportView() *ReportView {
	e.mu.Lock()
	provider := e.reportView
	e.mu.Unlock()
	if provider == nil {
		return nil
	}
	return provider()
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
// it. The leading user task is pinned: it is never part of the span, so the
// original request survives every compaction, including the trim fallback.
func (e *Engine) selectCompactionSpan() (span []ConversationItem, shadowed int, pinned int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	pinned = pinnedHistoryItems(e.history)
	if len(e.history) <= recentContextWindow+pinned {
		return nil, 0, pinned
	}
	boundary := adjustCompactionBoundary(e.history, len(e.history)-recentContextWindow)
	if boundary <= pinned {
		return nil, 0, pinned
	}
	return append([]ConversationItem(nil), e.history[pinned:boundary]...), boundary - pinned, pinned
}

// pinnedHistoryItems counts the leading conversation items that compaction must
// keep: the user's original task. It is the first user turn of the session.
func pinnedHistoryItems(history []ConversationItem) int {
	for i, item := range history {
		if item.Role == LLMRoleUser {
			return i + 1
		}
		if item.Role == LLMRoleAssistant && len(item.ToolCalls) > 0 {
			// A task always precedes its tool calls; anything else is not a
			// pinnable opening turn.
			return 0
		}
	}
	return 0
}

// applyCompaction replaces history[pinned:pinned+count] with the digest. The
// digest is not written into history: it is a runtime-context fact, so history
// stays a pure record of what was actually said.
func (e *Engine) applyCompaction(count int, pinned int, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if count <= 0 || pinned < 0 || pinned+count > len(e.history) {
		return
	}
	head := append([]ConversationItem(nil), e.history[:pinned]...)
	tail := append([]ConversationItem(nil), e.history[pinned+count:]...)
	e.history = append(head, tail...)
	if strings.TrimSpace(summary) == "" {
		// Nothing was summarized, so these items are genuinely gone.
		e.omittedHistoryMessages += count
		return
	}
	e.historySummary = summary
}

// formatCompactionSummary wraps a model-written summary in a stable marker so a
// reader can recognize compacted conversation as a record, not as instructions.
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
	e.mu.Lock()
	previous := e.historySummary
	e.mu.Unlock()
	if previous != "" {
		// The earlier digest is no longer in history, so fold it back into the
		// material being summarized. Otherwise each compaction would overwrite
		// the facts of the previous one.
		span = append([]ConversationItem{{
			Role:    LLMRoleUser,
			Content: formatCompactionSummary(previous),
		}}, span...)
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
	view := e.takeReportView()
	bundle := e.buildBundle(userTask, getRuntimeVars, continuationFacts, view)
	pressure := e.pressureTokens(EstimatePromptTokens(bundle, toolSpecs))
	trigger := contextCompactTriggerTokens()
	if pressure > trigger {
		if span, shadowed, pinned := e.selectCompactionSpan(); shadowed > 0 {
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
			e.applyCompaction(shadowed, pinned, summary)
			bundle = e.buildBundle(userTask, getRuntimeVars, continuationFacts, view)
			emit(RuntimeEvent{
				Type: EventHistoryCompacted,
				Data: HistoryCompactedData{
					Summary:       summary,
					ShadowedItems: shadowed,
					PinnedItems:   pinned,
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
// omission notice, and the full payload is spilled to disk so the omitted
// middle stays recoverable by id.
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
		"notice":         "This tool result was too large to keep in the conversation, so only its head and tail are shown here.",
	}
	// The full result stays addressable. The id is content-addressed, so the
	// live run and any later history rebuild resolve to the same payload.
	if id, spillErr := spillToolResult(compactedResult); spillErr == nil {
		pruned["full_result_id"] = id
		pruned["full_result_retrieval"] = "state_tool_result_inspect"
	} else {
		log.Printf("engine: could not spill oversized result of %s: %v", toolName, spillErr)
	}
	encoded, err := json.Marshal(pruned)
	if err != nil {
		return compactedResult
	}
	return string(encoded)
}
