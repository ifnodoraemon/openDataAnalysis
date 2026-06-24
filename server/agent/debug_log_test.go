package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/config"
)

func TestDebugWriterWritesMetaAndIndexes(t *testing.T) {
	previousCfg := config.Cfg
	config.Cfg = &config.Config{
		LLMDebug:    true,
		LLMDebugDir: t.TempDir(),
	}
	t.Cleanup(func() {
		config.Cfg = previousCfg
	})

	writer := &debugWriter{
		traceSpanSequence: make(map[string]int),
		traceMetaReady:    make(map[string]bool),
		spanMetaReady:     make(map[string]bool),
	}

	span := writer.StartSpan(TraceMetadata{
		WorkspaceID: "ws_1",
		SessionID:   "s_1",
		RunID:       "r_1",
		TraceID:     "trace_r_1",
	}, "llm", "openai", "", "")
	if span.TraceID == "" || span.SpanID == "" {
		t.Fatal("expected trace id")
	}

	payload := []byte(`{"ok":true}`)
	requestPath := writer.WriteBlob(span, "request.json", payload)
	if requestPath == "" {
		t.Fatal("expected request path")
	}

	writer.WriteEvent(span, "llm.request", map[string]interface{}{
		"request_path":  requestPath,
		"request_bytes": len(payload),
	})

	tracePath := filepath.Join(config.Cfg.LLMDebugDir, span.Date, span.TraceID, "trace.json")
	traceBytes, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace.json: %v", err)
	}

	var traceMeta map[string]interface{}
	if err := json.Unmarshal(traceBytes, &traceMeta); err != nil {
		t.Fatalf("unmarshal trace.json: %v", err)
	}
	if traceMeta["run_id"] != "r_1" {
		t.Fatalf("expected run_id r_1, got %#v", traceMeta["run_id"])
	}

	metaPath := filepath.Join(config.Cfg.LLMDebugDir, span.Date, span.TraceID, "spans", span.SpanID, "span.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read span.json: %v", err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("unmarshal span.json: %v", err)
	}
	if meta["span_id"] != span.SpanID {
		t.Fatalf("expected span_id %s, got %#v", span.SpanID, meta["span_id"])
	}

	traceIndexPath := filepath.Join(config.Cfg.LLMDebugDir, span.Date, span.TraceID, "events.jsonl")
	traceIndexBytes, err := os.ReadFile(traceIndexPath)
	if err != nil {
		t.Fatalf("read trace index: %v", err)
	}
	if !strings.Contains(string(traceIndexBytes), `"trace_id":"trace_r_1"`) {
		t.Fatalf("expected trace index to contain trace id, got %s", string(traceIndexBytes))
	}
	if !strings.Contains(string(traceIndexBytes), `"span_id":"trace_r_1-llm-001"`) {
		t.Fatalf("expected trace index to contain span id, got %s", string(traceIndexBytes))
	}

	dailyIndexPath := filepath.Join(config.Cfg.LLMDebugDir, span.Date, "events.jsonl")
	dailyIndexBytes, err := os.ReadFile(dailyIndexPath)
	if err != nil {
		t.Fatalf("read daily index: %v", err)
	}
	if !strings.Contains(string(dailyIndexBytes), `"request_bytes":11`) {
		t.Fatalf("expected daily index to contain compact payload summary, got %s", string(dailyIndexBytes))
	}
}

func TestDebugWriterWithoutConfigIsDisabled(t *testing.T) {
	previousCfg := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() {
		config.Cfg = previousCfg
	})

	writer := &debugWriter{
		traceSpanSequence: make(map[string]int),
		traceMetaReady:    make(map[string]bool),
		spanMetaReady:     make(map[string]bool),
	}
	span := writer.StartSpan(TraceMetadata{RunID: "r_1"}, "llm", "openai", "", "")
	if span.TraceID != "" || span.SpanID != "" {
		t.Fatalf("expected disabled debug writer to return empty span, got %#v", span)
	}
	writer.WriteEvent(SpanInfo{TraceID: "trace", SpanID: "span"}, "event", nil)
	if path := writer.WriteBlob(SpanInfo{TraceID: "trace", SpanID: "span"}, "request.json", []byte("{}")); path != "" {
		t.Fatalf("expected disabled debug writer to skip blob, got %s", path)
	}
}
