package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// seedOversizedHistory fills the engine with enough conversation to exceed a
// small context window.
func seedOversizedHistory(engine *Engine, items int, size int) {
	for i := 0; i < items; i++ {
		role := LLMRoleUser
		if i%2 == 1 {
			role = LLMRoleAssistant
		}
		engine.history = append(engine.history, ConversationItem{
			Role:    role,
			Content: strings.Repeat("a", size),
		})
	}
}

// Context pressure must be resolved before the request is sent, and the
// replaced conversation must be summarized rather than dropped.
func TestRunCompactsAndSummarizesBeforeRequest(t *testing.T) {
	server, snapshot := scriptedLLMServer(t, []string{
		responsesAPITextBody(t, "用户先确认了数据源，随后反复检查销售额口径。"),
		responsesAPITextBody(t, "结论：销售额为 100。"),
	})
	installTestLLMConfig(t, server)
	config.Cfg.LLMContextWindowTokens = 8000

	engine := &Engine{
		llm:      NewLLMClient(),
		registry: tools.NewRegistry(),
		policy:   "system",
	}
	seedOversizedHistory(engine, 20, 2000)

	var events []RuntimeEvent
	engine.Run(context.Background(), "分析数据", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	var compacted *HistoryCompactedData
	for _, ev := range events {
		if ev.Type == EventHistoryCompacted {
			data, ok := ev.Data.(HistoryCompactedData)
			if !ok {
				t.Fatalf("unexpected compaction payload %#v", ev.Data)
			}
			compacted = &data
		}
	}
	if compacted == nil {
		t.Fatalf("expected a history_compacted event, got %#v", events)
	}
	if compacted.Method != HistoryCompactionMethodSummary {
		t.Fatalf("expected a summary checkpoint, got method %q", compacted.Method)
	}
	if compacted.ShadowedItems != 8 {
		t.Fatalf("expected 8 shadowed items, got %d", compacted.ShadowedItems)
	}
	if compacted.ContextWindow != 8000 {
		t.Fatalf("expected the checkpoint to record the window, got %d", compacted.ContextWindow)
	}

	history := engine.History()
	// summary + the recent window, then the user task and the final answer that
	// the run appends after the request.
	if len(history) != 15 {
		t.Fatalf("expected summary plus the recent window plus this turn, got %d items", len(history))
	}
	if !strings.Contains(history[0].Content, "<compacted-summary>") {
		t.Fatalf("expected the summary item first, got %q", history[0].Content)
	}
	if !strings.Contains(history[0].Content, "销售额口径") {
		t.Fatalf("expected the model-written summary to be kept, got %q", history[0].Content)
	}
	if engine.omittedHistoryMessages != 0 {
		t.Fatalf("a summary replaces the omitted count, got %d", engine.omittedHistoryMessages)
	}

	// One request for the summary, one for the analysis itself.
	if got := len(snapshot()); got != 2 {
		t.Fatalf("expected the summarizer call plus the main request, got %d", got)
	}
}

// A failed summarization must not stall the run: the span is dropped instead and
// the checkpoint records that the facts were lost rather than summarized.
func TestRunFallsBackToTrimWhenSummarizationFails(t *testing.T) {
	server, snapshot := scriptedLLMServer(t, []string{
		responsesAPITextBody(t, ""),
		responsesAPITextBody(t, "结论：继续。"),
	})
	installTestLLMConfig(t, server)
	config.Cfg.LLMContextWindowTokens = 8000

	engine := &Engine{
		llm:      NewLLMClient(),
		registry: tools.NewRegistry(),
		policy:   "system",
	}
	seedOversizedHistory(engine, 20, 2000)

	var events []RuntimeEvent
	engine.Run(context.Background(), "分析数据", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	var compacted *HistoryCompactedData
	for _, ev := range events {
		if ev.Type == EventHistoryCompacted {
			data := ev.Data.(HistoryCompactedData)
			compacted = &data
		}
	}
	if compacted == nil {
		t.Fatalf("expected a history_compacted event, got %#v", events)
	}
	if compacted.Method != HistoryCompactionMethodTrim {
		t.Fatalf("expected a trim checkpoint after summarization failure, got %q", compacted.Method)
	}
	if compacted.ShadowedItems != 8 {
		t.Fatalf("expected 8 shadowed items, got %d", compacted.ShadowedItems)
	}

	history := engine.History()
	// The recent window survives, plus the user task and final answer appended
	// after the request.
	if len(history) != 14 {
		t.Fatalf("expected the recent window to survive, got %d items", len(history))
	}
	if strings.Contains(history[0].Content, "<compacted-summary>") {
		t.Fatalf("no summary may be inserted when summarization failed")
	}
	if engine.omittedHistoryMessages != 8 {
		t.Fatalf("the dropped items must be reported as omitted, got %d", engine.omittedHistoryMessages)
	}
	if got := len(snapshot()); got != 2 {
		t.Fatalf("expected two llm calls, got %d", got)
	}
}

// A prompt that fits the window must not trigger compaction or a summarizer
// call.
func TestRunSkipsCompactionBelowThreshold(t *testing.T) {
	server, snapshot := scriptedLLMServer(t, []string{
		responsesAPITextBody(t, "结论：无需压缩。"),
	})
	installTestLLMConfig(t, server)
	config.Cfg.LLMContextWindowTokens = 128000

	engine := &Engine{
		llm:      NewLLMClient(),
		registry: tools.NewRegistry(),
		policy:   "system",
	}
	seedOversizedHistory(engine, 6, 200)

	var events []RuntimeEvent
	engine.Run(context.Background(), "分析数据", nil, func(ev RuntimeEvent) { events = append(events, ev) })

	for _, ev := range events {
		if ev.Type == EventHistoryCompacted {
			t.Fatalf("unexpected compaction below the threshold: %#v", ev)
		}
	}
	if got := len(snapshot()); got != 1 {
		t.Fatalf("expected a single request, got %d", got)
	}
	// The seeded history, the user task and the final answer.
	if len(engine.History()) != 8 {
		t.Fatalf("history must be untouched below the threshold, got %d items", len(engine.History()))
	}
}
