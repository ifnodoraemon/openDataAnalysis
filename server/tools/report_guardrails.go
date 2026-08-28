package tools

import (
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode"
)

func (s *ReportEditState) RefreshFromReportState(state *ReportState) {
	if s == nil {
		return
	}
	var blocks []ReportBlock
	if state != nil {
		state.RLock()
		blocks = make([]ReportBlock, len(state.Blocks))
		copy(blocks, state.Blocks)
		state.RUnlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AllowedChartIDs = collectEditableChartIDs(blocks, s.TargetBlockID, s.TargetChartID)
	s.TargetBlockContent = ""
	s.TargetBlockKind = ""
	s.TargetBlockTitle = ""
	s.TargetBlockChartID = ""
	s.TargetBlockSources = nil
	if state != nil && s.TargetBlockID != "" {
		if index := findReportBlockIndex(blocks, s.TargetBlockID); index >= 0 {
			block := blocks[index]
			s.TargetBlockContent = block.Content
			s.TargetBlockKind = block.Kind
			s.TargetBlockTitle = block.Title
			s.TargetBlockChartID = block.ChartID
			s.TargetBlockSources = append([]EvidenceRef(nil), block.Sources...)
		}
	}
}

func (s *ReportEditState) BlockMutationAllowed(action, blockID string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch s.scopeKindLocked() {
	case "", "whole_report":
		return true
	case "partial_block", "partial_selection":
		// Continue with the exact target check below.
	default:
		return false
	}
	target := s.TargetBlockID
	id := blockID
	switch action {
	case "upsert":
		return target != "" && id == target
	default:
		return false
	}
}

func (s *ReportEditState) ChartMutationAllowed(chartID string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch s.scopeKindLocked() {
	case "", "whole_report":
		return true
	case "layout":
		return false
	}
	_, ok := s.AllowedChartIDs[chartID]
	return ok
}

func (s *ReportEditState) LayoutMutationAllowed() bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch s.scopeKindLocked() {
	case "", "whole_report", "layout":
		return true
	default:
		return false
	}
}

func (s *ReportEditState) SelectionMutationAllowed(blockID, newContent string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.scopeKindLocked() != "partial_selection" {
		return true
	}
	if blockID == "" || blockID != s.TargetBlockID {
		return false
	}
	original := s.TargetBlockContent
	if original == "" {
		return false
	}
	start, end, ok := selectionBoundsLocked(s, original)
	if !ok {
		return false
	}
	prefix := string([]rune(original)[:start])
	suffix := string([]rune(original)[end:])
	return strings.HasPrefix(newContent, prefix) && strings.HasSuffix(newContent, suffix)
}

func (s *ReportEditState) SelectionBlockMutationAllowed(block ReportBlock) bool {
	if s == nil {
		return true
	}
	if !s.SelectionMutationAllowed(block.ID, block.Content) {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.scopeKindLocked() != "partial_selection" {
		return true
	}
	kindUnchanged := s.TargetBlockKind == "" || block.Kind == s.TargetBlockKind
	return kindUnchanged &&
		block.Title == s.TargetBlockTitle &&
		block.ChartID == s.TargetBlockChartID &&
		evidenceRefsEqual(block.Sources, s.TargetBlockSources)
}

func evidenceRefsEqual(a, b []EvidenceRef) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func selectionBoundsLocked(s *ReportEditState, original string) (int, int, bool) {
	return selectionBoundsFromProjectedText(s, original)
}

type projectedRune struct {
	value    rune
	rawStart int
	rawEnd   int
}

func selectionBoundsFromProjectedText(s *ReportEditState, original string) (int, int, bool) {
	projected := projectReportContentText(original)
	if len(projected) == 0 {
		return 0, 0, false
	}
	needle := normalizeSelectionCompareText(s.SelectionText)
	if needle == "" {
		return 0, 0, false
	}
	if !s.SelectionRangeSet || s.SelectionEnd <= s.SelectionStart || s.SelectionStart < 0 || s.SelectionEnd > len(projected) {
		return 0, 0, false
	}
	selected := projectedText(projected[s.SelectionStart:s.SelectionEnd])
	if normalizeSelectionCompareText(selected) != needle {
		return 0, 0, false
	}
	return projectedRawBounds(projected, s.SelectionStart, s.SelectionEnd)
}

func projectReportContentText(content string) []projectedRune {
	runes := []rune(content)
	projected := make([]projectedRune, 0, len(runes))
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if ch == '&' {
			if decoded, rawEnd, ok := decodeHTMLEntityAt(runes, i); ok {
				for _, decodedRune := range decoded {
					projected = append(projected, projectedRune{value: decodedRune, rawStart: i, rawEnd: rawEnd})
				}
				i = rawEnd - 1
				continue
			}
		}
		if ch == '<' {
			if end := findRuneForward(runes, i+1, '>'); end >= 0 {
				i = end
				continue
			}
		}
		if ch == '!' && i+1 < len(runes) && runes[i+1] == '[' {
			if textEnd := findRuneForward(runes, i+2, ']'); textEnd >= 0 && textEnd+1 < len(runes) && runes[textEnd+1] == '(' {
				if linkEnd := findRuneForward(runes, textEnd+2, ')'); linkEnd >= 0 {
					projected = appendProjectedRuneRange(projected, runes, i+2, textEnd)
					i = linkEnd
					continue
				}
			}
		}
		if ch == '[' {
			if textEnd := findRuneForward(runes, i+1, ']'); textEnd >= 0 && textEnd+1 < len(runes) && runes[textEnd+1] == '(' {
				if linkEnd := findRuneForward(runes, textEnd+2, ')'); linkEnd >= 0 {
					projected = appendProjectedRuneRange(projected, runes, i+1, textEnd)
					i = linkEnd
					continue
				}
			}
		}
		if isMarkdownSyntaxRune(runes, i) {
			continue
		}
		projected = appendProjectedRune(projected, runes, i)
	}
	return projected
}

func appendProjectedRuneRange(projected []projectedRune, runes []rune, start, end int) []projectedRune {
	for i := start; i < end; i++ {
		if decoded, rawEnd, ok := decodeHTMLEntityAt(runes, i); ok && rawEnd <= end {
			for _, decodedRune := range decoded {
				projected = append(projected, projectedRune{value: decodedRune, rawStart: i, rawEnd: rawEnd})
			}
			i = rawEnd - 1
			continue
		}
		projected = append(projected, projectedRune{value: runes[i], rawStart: i, rawEnd: i + 1})
	}
	return projected
}

func appendProjectedRune(projected []projectedRune, runes []rune, index int) []projectedRune {
	if decoded, rawEnd, ok := decodeHTMLEntityAt(runes, index); ok {
		for _, decodedRune := range decoded {
			projected = append(projected, projectedRune{value: decodedRune, rawStart: index, rawEnd: rawEnd})
		}
		return projected
	}
	return append(projected, projectedRune{value: runes[index], rawStart: index, rawEnd: index + 1})
}

func decodeHTMLEntityAt(runes []rune, start int) ([]rune, int, bool) {
	if start < 0 || start >= len(runes) || runes[start] != '&' {
		return nil, 0, false
	}
	end := findRuneForward(runes, start+1, ';')
	if end < 0 || end-start > 32 {
		return nil, 0, false
	}
	raw := string(runes[start : end+1])
	decoded := html.UnescapeString(raw)
	if decoded == raw {
		return nil, 0, false
	}
	return []rune(decoded), end + 1, true
}

func findRuneForward(runes []rune, start int, target rune) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func isMarkdownSyntaxRune(runes []rune, index int) bool {
	ch := runes[index]
	if ch == '*' || ch == '`' {
		return true
	}
	if ch == '#' {
		atLineStart := index == 0 || runes[index-1] == '\n'
		nextIsSpace := index+1 < len(runes) && (runes[index+1] == ' ' || runes[index+1] == '#')
		return atLineStart && nextIsSpace
	}
	return false
}

func projectedText(projected []projectedRune) string {
	var b strings.Builder
	for _, item := range projected {
		b.WriteRune(item.value)
	}
	return b.String()
}

func projectedRawBounds(projected []projectedRune, start, end int) (int, int, bool) {
	if start < 0 || end > len(projected) || start >= end {
		return 0, 0, false
	}
	rawStart := projected[start].rawStart
	rawEnd := projected[end-1].rawEnd
	return rawStart, rawEnd, true
}

func normalizeSelectionCompareText(value string) string {
	normalized, _ := normalizeSelectionCompareTextWithMap(value)
	return normalized
}

func normalizeSelectionCompareTextWithMap(value string) (string, []int) {
	runes := []rune(value)
	var b strings.Builder
	indexMap := make([]int, 0, len(runes))
	inSpace := false
	for i, ch := range runes {
		if unicode.IsSpace(ch) {
			inSpace = true
			continue
		}
		if inSpace && b.Len() > 0 {
			b.WriteRune(' ')
			indexMap = append(indexMap, i)
		}
		inSpace = false
		b.WriteRune(ch)
		indexMap = append(indexMap, i)
	}
	return strings.TrimSpace(b.String()), indexMap
}

func collectEditableChartIDs(blocks []ReportBlock, blockID, chartID string) map[string]struct{} {
	refs := make(map[string]struct{})
	if chartID != "" {
		refs[chartID] = struct{}{}
	}
	if len(blocks) == 0 || blockID == "" {
		return refs
	}
	index := findReportBlockIndex(blocks, blockID)
	if index < 0 {
		return refs
	}
	block := blocks[index]
	if block.ChartID != "" {
		refs[block.ChartID] = struct{}{}
	}
	for _, ref := range ChartReferences(block.Content) {
		refs[ref] = struct{}{}
	}
	return refs
}

func referencedChartsOutsideChartBlocks(blocks []ReportBlock) map[string]struct{} {
	refs := make(map[string]struct{})
	for _, block := range blocks {
		if block.Kind == "chart" {
			continue
		}
		for _, ref := range ChartReferences(block.Content) {
			refs[ref] = struct{}{}
		}
	}
	return refs
}

func renderableReportBlockCount(blocks []ReportBlock) int {
	count := 0
	for _, block := range blocks {
		switch block.Kind {
		case "markdown", "html", "chart":
			count++
		}
	}
	return count
}

func RenderableReportBlockCount(state *ReportState) int {
	if state == nil {
		return 0
	}
	return renderableReportBlockCount(state.Blocks)
}

func RenderableReportBlockCountLocked(state *ReportState) int {
	return RenderableReportBlockCount(state)
}

func reportFinalizeIssues(state *ReportState) []string {
	if state == nil {
		return []string{"report_state_missing"}
	}

	chartSet := make(map[string]struct{}, len(state.Charts))
	for _, chart := range state.Charts {
		chartID := chart.ID
		if chartID != "" {
			chartSet[chartID] = struct{}{}
		}
	}

	refCounts := make(map[string]int)
	for _, block := range state.Blocks {
		if block.Kind == "chart" && block.ChartID != "" {
			refCounts[block.ChartID]++
		}
		for chartID := range referencedChartsOutsideChartBlocks([]ReportBlock{block}) {
			refCounts[chartID]++
		}
	}

	var issues []string
	if renderableReportBlockCount(state.Blocks) == 0 {
		issues = append(issues, "report_has_no_blocks")
	}

	var missingCharts []string
	for chartID := range refCounts {
		if _, ok := chartSet[chartID]; !ok {
			missingCharts = append(missingCharts, chartID)
		}
	}
	sort.Strings(missingCharts)
	for _, chartID := range missingCharts {
		issues = append(issues, "missing_chart:"+chartID)
	}

	var duplicateCharts []string
	for chartID, count := range refCounts {
		if count > 1 {
			duplicateCharts = append(duplicateCharts, fmt.Sprintf("%s(x%d)", chartID, count))
		}
	}
	sort.Strings(duplicateCharts)
	for _, item := range duplicateCharts {
		issues = append(issues, "duplicate_chart:"+item)
	}

	issues = append(issues, reportEvidenceFinalizeIssues(state)...)

	return issues
}

func reportEvidenceFinalizeIssues(state *ReportState) []string {
	if state == nil {
		return nil
	}
	var issues []string
	validate := func(owner string, refs []EvidenceRef) {
		for i, ref := range refs {
			kind := ref.Kind
			resultID := ref.ResultID
			artifactID := ref.ArtifactID
			chartID := ref.ChartID
			refCount := 0
			for _, value := range []string{resultID, artifactID, chartID} {
				if value != "" {
					refCount++
				}
			}
			if refCount != 1 {
				issues = append(issues, fmt.Sprintf("invalid_evidence_ref:%s:%d", owner, i))
				continue
			}
			switch kind {
			case "result":
				if resultID == "" {
					issues = append(issues, fmt.Sprintf("evidence_kind_mismatch:%s:%d", owner, i))
					continue
				}
				if _, ok := state.Results[resultID]; !ok {
					issues = append(issues, fmt.Sprintf("unknown_result_ref:%s:%d:%s", owner, i, resultID))
				}
			case "artifact":
				if artifactID == "" {
					issues = append(issues, fmt.Sprintf("evidence_kind_mismatch:%s:%d", owner, i))
					continue
				}
				if _, ok := state.Artifacts[artifactID]; !ok {
					issues = append(issues, fmt.Sprintf("unknown_artifact_ref:%s:%d:%s", owner, i, artifactID))
				}
			case "chart":
				if chartID == "" {
					issues = append(issues, fmt.Sprintf("evidence_kind_mismatch:%s:%d", owner, i))
					continue
				}
				found := false
				for _, chart := range state.Charts {
					if chart.ID == chartID {
						found = true
						break
					}
				}
				if !found {
					issues = append(issues, fmt.Sprintf("unknown_chart_ref:%s:%d:%s", owner, i, chartID))
				}
			default:
				issues = append(issues, fmt.Sprintf("unknown_evidence_kind:%s:%d:%s", owner, i, kind))
			}
		}
	}
	for _, block := range state.Blocks {
		validate("block:"+block.ID, block.Sources)
	}
	for _, chart := range state.Charts {
		validate("chart:"+chart.ID, chart.Sources)
	}
	return issues
}

func chartIDsForBlock(block ReportBlock) []string {
	ids := map[string]struct{}{}
	if chartID := block.ChartID; chartID != "" {
		ids[chartID] = struct{}{}
	}
	for chartID := range referencedChartsOutsideChartBlocks([]ReportBlock{block}) {
		ids[chartID] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func ReportFinalizeIssuesForAgent(state *ReportState) []string {
	return reportFinalizeIssues(state)
}

func ReportFinalizeIssuesForAgentLocked(state *ReportState) []string {
	return reportFinalizeIssues(state)
}
