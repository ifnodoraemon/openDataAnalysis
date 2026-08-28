package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/config"
)

var llmDebugWriter = &debugWriter{
	traceSpanSequence: make(map[string]int),
	traceMetaReady:    make(map[string]bool),
	spanMetaReady:     make(map[string]bool),
}

type SpanInfo struct {
	Date         string
	TraceID      string
	SpanID       string
	ParentSpanID string
	Sequence     int
	Kind         string
	Name         string
	WorkspaceID  string
	SessionID    string
	RunID        string
	ToolCallID   string
}

type debugWriter struct {
	mu                sync.Mutex
	traceSpanSequence map[string]int
	traceMetaReady    map[string]bool
	spanMetaReady     map[string]bool
}

func (w *debugWriter) StartSpan(meta TraceMetadata, kind, name, parentSpanID, toolCallID string) SpanInfo {
	if !llmDebugEnabled() {
		return SpanInfo{}
	}

	now := time.Now()
	traceID := strings.TrimSpace(meta.TraceID)
	if traceID == "" {
		if meta.RunID != "" {
			traceID = meta.RunID
		} else {
			traceID = fmt.Sprintf("trace-%s-%s", now.Format("150405.000000000"), uuid.NewString()[:8])
		}
	}
	spanKind := sanitizeTracePart(kind)
	if spanKind == "" {
		spanKind = "span"
	}

	info := SpanInfo{
		Date:         now.Format("2006-01-02"),
		TraceID:      traceID,
		ParentSpanID: strings.TrimSpace(parentSpanID),
		Kind:         spanKind,
		Name:         strings.TrimSpace(name),
		WorkspaceID:  meta.WorkspaceID,
		SessionID:    meta.SessionID,
		RunID:        meta.RunID,
		ToolCallID:   strings.TrimSpace(toolCallID),
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	info.Sequence = w.traceSpanSequence[traceID] + 1
	w.traceSpanSequence[traceID] = info.Sequence
	info.SpanID = fmt.Sprintf("%s-%s-%03d", traceID, spanKind, info.Sequence)
	return info
}

func (w *debugWriter) WriteEvent(span SpanInfo, eventName string, payload map[string]interface{}) {
	if !llmDebugEnabled() || span.TraceID == "" || span.SpanID == "" {
		return
	}

	record := map[string]interface{}{
		"type":           "event",
		"ts":             time.Now().Format(time.RFC3339Nano),
		"trace_id":       span.TraceID,
		"span_id":        span.SpanID,
		"parent_span_id": span.ParentSpanID,
		"sequence":       span.Sequence,
		"span_kind":      span.Kind,
		"span_name":      span.Name,
		"workspace_id":   span.WorkspaceID,
		"session_id":     span.SessionID,
		"run_id":         span.RunID,
		"tool_call_id":   span.ToolCallID,
		"event": map[string]interface{}{
			"name": eventName,
			"data": payload,
		},
	}
	line, err := json.Marshal(record)
	if err != nil {
		log.Printf("debug trace: encode event: %v", err)
		return
	}

	debugDir := llmDebugDir()
	dailyPath := filepath.Join(debugDir, span.Date, "events.jsonl")
	tracePath := filepath.Join(debugDir, span.Date, span.TraceID, "events.jsonl")
	spanPath := filepath.Join(debugDir, span.Date, span.TraceID, "spans", span.SpanID, "events.jsonl")

	w.mu.Lock()
	defer w.mu.Unlock()

	w.ensureTraceMetaLocked(span)
	w.ensureSpanMetaLocked(span)
	for _, path := range []string{dailyPath, tracePath, spanPath} {
		if err := appendJSONL(path, line); err != nil {
			log.Printf("debug trace: append %s: %v", path, err)
		}
	}
}

func (w *debugWriter) WriteBlob(span SpanInfo, name string, payload []byte) string {
	if !llmDebugEnabled() || span.TraceID == "" || span.SpanID == "" {
		return ""
	}

	path := filepath.Join(llmDebugDir(), span.Date, span.TraceID, "spans", span.SpanID, name)

	w.mu.Lock()
	defer w.mu.Unlock()

	w.ensureTraceMetaLocked(span)
	w.ensureSpanMetaLocked(span)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("debug trace: create blob directory: %v", err)
		return ""
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		log.Printf("debug trace: write blob %s: %v", path, err)
		return ""
	}
	return path
}

func appendJSONL(path string, line []byte) (resultErr error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, f.Close())
	}()

	_, resultErr = fmt.Fprintln(f, string(line))
	return resultErr
}

func (w *debugWriter) ensureTraceMetaLocked(span SpanInfo) {
	if span.TraceID == "" || w.traceMetaReady[span.TraceID] {
		return
	}

	meta := map[string]interface{}{
		"type":         "trace",
		"trace_id":     span.TraceID,
		"workspace_id": span.WorkspaceID,
		"session_id":   span.SessionID,
		"run_id":       span.RunID,
		"date":         span.Date,
		"created_at":   time.Now().Format(time.RFC3339Nano),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("debug trace: encode trace metadata: %v", err)
		return
	}
	metaPath := filepath.Join(llmDebugDir(), span.Date, span.TraceID, "trace.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		log.Printf("debug trace: create trace metadata directory: %v", err)
		return
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		log.Printf("debug trace: write trace metadata %s: %v", metaPath, err)
		return
	}
	w.traceMetaReady[span.TraceID] = true
}

func (w *debugWriter) ensureSpanMetaLocked(span SpanInfo) {
	if span.SpanID == "" || w.spanMetaReady[span.SpanID] {
		return
	}

	meta := map[string]interface{}{
		"type":           "span",
		"trace_id":       span.TraceID,
		"span_id":        span.SpanID,
		"parent_span_id": span.ParentSpanID,
		"sequence":       span.Sequence,
		"kind":           span.Kind,
		"name":           span.Name,
		"workspace_id":   span.WorkspaceID,
		"session_id":     span.SessionID,
		"run_id":         span.RunID,
		"tool_call_id":   span.ToolCallID,
		"created_at":     time.Now().Format(time.RFC3339Nano),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		log.Printf("debug trace: encode span metadata: %v", err)
		return
	}
	metaPath := filepath.Join(llmDebugDir(), span.Date, span.TraceID, "spans", span.SpanID, "span.json")
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		log.Printf("debug trace: create span metadata directory: %v", err)
		return
	}
	if err := os.WriteFile(metaPath, metaBytes, 0o644); err != nil {
		log.Printf("debug trace: write span metadata %s: %v", metaPath, err)
		return
	}
	w.spanMetaReady[span.SpanID] = true
}

func sanitizeTracePart(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(" ", "-", "/", "-", "\\", "-", "_", "-")
	value = replacer.Replace(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-")
}

func blobSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func llmDebugEnabled() bool {
	return config.Cfg != nil && config.Cfg.LLMDebug
}

func llmDebugDir() string {
	if config.Cfg != nil && strings.TrimSpace(config.Cfg.LLMDebugDir) != "" {
		return config.Cfg.LLMDebugDir
	}
	return "./data/llm-debug"
}
