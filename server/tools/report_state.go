package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

type ReportState struct {
	mu              sync.RWMutex              `json:"-"`
	Blocks          []ReportBlock             `json:"blocks"`
	Charts          []ChartData               `json:"charts"`
	FinalTitle      string                    `json:"finalTitle,omitempty"`
	FinalAuthor     string                    `json:"finalAuthor,omitempty"`
	Layout          ReportLayout              `json:"layout,omitempty"`
	NeedsFinalize   bool                      `json:"needsFinalize,omitempty"`
	MutationVersion uint64                    `json:"mutationVersion"`
	Results         map[string]AnalysisResult `json:"results,omitempty"`
	Artifacts       map[string]ArtifactRecord `json:"artifacts,omitempty"`
}

func (s *ReportState) Lock()    { s.mu.Lock() }
func (s *ReportState) Unlock()  { s.mu.Unlock() }
func (s *ReportState) RLock()   { s.mu.RLock() }
func (s *ReportState) RUnlock() { s.mu.RUnlock() }

// EvidenceRef 报告 block 的来源引用，记录结论基于哪次查询/哪张图/哪一步分析
type EvidenceRef struct {
	Kind       string `json:"kind"` // result | artifact | chart
	ResultID   string `json:"result_id,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	ChartID    string `json:"chart_id,omitempty"`
	Label      string `json:"label,omitempty"`
}

type AnalysisResult struct {
	ID        string                   `json:"id"`
	ToolName  string                   `json:"tool_name"`
	Operation string                   `json:"operation"`
	Columns   []string                 `json:"columns"`
	Rows      []map[string]interface{} `json:"rows"`
	RowCount  int                      `json:"row_count"`
	SourceID  string                   `json:"source_id,omitempty"`
	Dialect   string                   `json:"dialect,omitempty"`
	CreatedAt string                   `json:"created_at"`
}

type ArtifactRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func (s *ReportState) RecordResult(result AnalysisResult) error {
	if s == nil {
		return fmt.Errorf("report state is not initialized")
	}
	if result.ID == "" || result.ID != strings.TrimSpace(result.ID) {
		return fmt.Errorf("analysis result ID must be a non-empty exact value")
	}
	s.Lock()
	defer s.Unlock()
	if s.Results == nil {
		s.Results = make(map[string]AnalysisResult)
	}
	s.Results[result.ID] = result
	return nil
}

func (s *ReportState) RecordArtifact(artifact ArtifactRecord) error {
	if s == nil {
		return fmt.Errorf("report state is not initialized")
	}
	if artifact.ID == "" || artifact.ID != strings.TrimSpace(artifact.ID) {
		return fmt.Errorf("artifact ID must be a non-empty exact value")
	}
	s.Lock()
	defer s.Unlock()
	if s.Artifacts == nil {
		s.Artifacts = make(map[string]ArtifactRecord)
	}
	s.Artifacts[artifact.ID] = artifact
	return nil
}

func DecodeAnalysisResults(raw json.RawMessage) (map[string]AnalysisResult, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var results map[string]AnalysisResult
	if err := jsoncontract.Decode(raw, &results); err != nil {
		return nil, err
	}
	if results == nil {
		return nil, fmt.Errorf("analysis result ledger must be a JSON object")
	}
	for key, result := range results {
		if key == "" || key != strings.TrimSpace(key) || result.ID != key {
			return nil, fmt.Errorf("analysis result ledger key %q does not match an exact record ID", key)
		}
	}
	return results, nil
}

func DecodeArtifactRecords(raw json.RawMessage) (map[string]ArtifactRecord, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var artifacts map[string]ArtifactRecord
	if err := jsoncontract.Decode(raw, &artifacts); err != nil {
		return nil, err
	}
	if artifacts == nil {
		return nil, fmt.Errorf("artifact ledger must be a JSON object")
	}
	for key, artifact := range artifacts {
		if key == "" || key != strings.TrimSpace(key) || artifact.ID != key {
			return nil, fmt.Errorf("artifact ledger key %q does not match an exact record ID", key)
		}
	}
	return artifacts, nil
}

func DecodeEvidenceRefs(raw json.RawMessage) ([]EvidenceRef, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var refs []EvidenceRef
	if err := jsoncontract.Decode(raw, &refs); err != nil {
		return nil, err
	}
	if refs == nil {
		return nil, fmt.Errorf("evidence references must be a JSON array")
	}
	for index, ref := range refs {
		if err := validateEvidenceRefShape(ref); err != nil {
			return nil, fmt.Errorf("evidence reference %d: %w", index, err)
		}
	}
	return refs, nil
}

func validateEvidenceRefShape(ref EvidenceRef) error {
	for field, value := range map[string]string{"kind": ref.Kind, "result_id": ref.ResultID, "artifact_id": ref.ArtifactID, "chart_id": ref.ChartID} {
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("%s must be exact", field)
		}
	}
	setCount := 0
	for _, value := range []string{ref.ResultID, ref.ArtifactID, ref.ChartID} {
		if value != "" {
			setCount++
		}
	}
	if setCount != 1 {
		return fmt.Errorf("exactly one result_id, artifact_id, or chart_id is required")
	}
	switch ref.Kind {
	case "result":
		if ref.ResultID == "" {
			return fmt.Errorf("kind=result requires result_id")
		}
	case "artifact":
		if ref.ArtifactID == "" {
			return fmt.Errorf("kind=artifact requires artifact_id")
		}
	case "chart":
		if ref.ChartID == "" {
			return fmt.Errorf("kind=chart requires chart_id")
		}
	default:
		return fmt.Errorf("unsupported evidence kind %q", ref.Kind)
	}
	return nil
}

type ReportBlock struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	Title   string        `json:"title,omitempty"`
	Content string        `json:"content,omitempty"`
	ChartID string        `json:"chartId,omitempty"`
	Sources []EvidenceRef `json:"sources,omitempty"`
}

type ReportLayout struct {
	CustomCSS string `json:"customCss,omitempty"`
	BodyClass string `json:"bodyClass,omitempty"`
}

type ReportEditState struct {
	mu                 sync.RWMutex        `json:"-"`
	ScopeKindValue     string              `json:"scopeKind"`
	TargetRunID        string              `json:"targetRunId,omitempty"`
	TargetBlockID      string              `json:"targetBlockId,omitempty"`
	TargetBlockLabel   string              `json:"targetBlockLabel,omitempty"`
	TargetChartID      string              `json:"targetChartId,omitempty"`
	SelectionText      string              `json:"selectionText,omitempty"`
	SelectionStart     int                 `json:"selectionStart,omitempty"`
	SelectionEnd       int                 `json:"selectionEnd,omitempty"`
	SelectionRangeSet  bool                `json:"selectionRangeSet,omitempty"`
	AllowedChartIDs    map[string]struct{} `json:"-"`
	TargetBlockContent string              `json:"-"`
	TargetBlockKind    string              `json:"-"`
	TargetBlockTitle   string              `json:"-"`
	TargetBlockChartID string              `json:"-"`
	TargetBlockSources []EvidenceRef       `json:"-"`
}

type ReportDeliveryState struct {
	HasContent    bool   `json:"has_content"`
	IsFinalized   bool   `json:"is_finalized"`
	NeedsFinalize bool   `json:"needs_finalize"`
	DeliveryState string `json:"delivery_state"`
	BlockCount    int    `json:"block_count"`
	ChartCount    int    `json:"chart_count"`
	FinalTitle    string `json:"final_title,omitempty"`
	FinalAuthor   string `json:"final_author,omitempty"`
}

func (s *ReportEditState) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ScopeKindValue = ""
	s.TargetRunID = ""
	s.TargetBlockID = ""
	s.TargetBlockLabel = ""
	s.TargetChartID = ""
	s.SelectionText = ""
	s.SelectionStart = 0
	s.SelectionEnd = 0
	s.SelectionRangeSet = false
	s.AllowedChartIDs = nil
	s.TargetBlockContent = ""
	s.TargetBlockKind = ""
	s.TargetBlockTitle = ""
	s.TargetBlockChartID = ""
	s.TargetBlockSources = nil
}

func (s *ReportEditState) Active() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scopeKindLocked() != ""
}

func (s *ReportEditState) ActiveLocked() bool {
	if s == nil {
		return false
	}
	return s.scopeKindLocked() != ""
}

func (s *ReportEditState) ScopeKind() string {
	if s == nil {
		return "inactive"
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.scopeKindLocked() == "" {
		return "inactive"
	}
	return s.scopeKindLocked()
}

func (s *ReportEditState) ScopeKindLocked() string {
	if s == nil {
		return "inactive"
	}
	if s.scopeKindLocked() == "" {
		return "inactive"
	}
	return s.scopeKindLocked()
}

func (s *ReportEditState) scopeKindLocked() string {
	return s.ScopeKindValue
}

func (s *ReportEditState) Snapshot() map[string]interface{} {
	if s == nil {
		return map[string]interface{}{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	charts := make([]string, 0, len(s.AllowedChartIDs))
	for chartID := range s.AllowedChartIDs {
		charts = append(charts, chartID)
	}
	sort.Strings(charts)
	return map[string]interface{}{
		"scope_kind":          s.ScopeKindValue,
		"target_run_id":       s.TargetRunID,
		"target_block_id":     s.TargetBlockID,
		"target_block_label":  s.TargetBlockLabel,
		"target_chart_id":     s.TargetChartID,
		"selection_text":      s.SelectionText,
		"selection_start":     s.SelectionStart,
		"selection_end":       s.SelectionEnd,
		"selection_range_set": s.SelectionRangeSet,
		"allowed_chart_ids":   charts,
		"active":              s.ActiveLocked(),
	}
}

func DescribeReportDeliveryState(state *ReportState) ReportDeliveryState {
	if state == nil {
		return ReportDeliveryState{DeliveryState: "empty"}
	}
	state.RLock()
	defer state.RUnlock()
	return describeReportDeliveryStateLocked(state)
}

func DescribeReportDeliveryStateLocked(state *ReportState) ReportDeliveryState {
	if state == nil {
		return ReportDeliveryState{DeliveryState: "empty"}
	}
	return describeReportDeliveryStateLocked(state)
}

func describeReportDeliveryStateLocked(state *ReportState) ReportDeliveryState {
	delivery := ReportDeliveryState{
		DeliveryState: "empty",
	}
	delivery.BlockCount = len(state.Blocks)
	delivery.ChartCount = len(state.Charts)
	delivery.FinalTitle = state.FinalTitle
	delivery.FinalAuthor = state.FinalAuthor
	delivery.HasContent = delivery.BlockCount > 0 || delivery.ChartCount > 0
	delivery.NeedsFinalize = state.NeedsFinalize
	delivery.IsFinalized = delivery.HasContent && !state.NeedsFinalize && strings.TrimSpace(delivery.FinalTitle) != ""
	if delivery.HasContent {
		if delivery.IsFinalized {
			delivery.DeliveryState = "finalized"
		} else {
			delivery.DeliveryState = "draft"
		}
	}
	return delivery
}
