package tools

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/ifnodoraemon/openDataAnalysis/service"
)

var (
	sectionChapterPrefixPattern = regexp.MustCompile(`^(第[一二三四五六七八九十百千0-9]+(?:章|节|部分|篇)\s*)`)
	sectionOrdinalPrefixPattern = regexp.MustCompile(`^([一二三四五六七八九十百千0-9]+[.、)）]\s*)`)
	sectionWhitespacePattern    = regexp.MustCompile(`\s+`)
	reportNumberPattern         = regexp.MustCompile(`\d`)
)

func (s *ReportEditState) RefreshFromReportState(state *ReportState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AllowedChartIDs = collectEditableChartIDs(state, s.TargetBlockID, s.TargetChartID)
	s.TargetBlockContent = ""
	s.TargetBlockKind = ""
	s.TargetBlockTitle = ""
	s.TargetBlockChartID = ""
	s.TargetBlockSources = nil
	if state != nil && strings.TrimSpace(s.TargetBlockID) != "" {
		if index := findReportBlockIndex(state.Blocks, strings.TrimSpace(s.TargetBlockID)); index >= 0 {
			block := state.Blocks[index]
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
	if s.modeLocked() == "" || !s.PreserveOtherBlocks {
		return true
	}
	if strings.TrimSpace(s.TargetChartID) != "" && strings.TrimSpace(s.TargetBlockID) == "" {
		return false
	}
	target := strings.TrimSpace(s.TargetBlockID)
	id := strings.TrimSpace(blockID)
	switch strings.ToLower(strings.TrimSpace(action)) {
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
	if s.modeLocked() == "" || !s.PreserveOtherBlocks {
		return true
	}
	_, ok := s.AllowedChartIDs[strings.TrimSpace(chartID)]
	return ok
}

func (s *ReportEditState) SelectionMutationAllowed(blockID, newContent string) bool {
	if s == nil {
		return true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.modeLocked() == "" || s.scopeKindLocked() != "partial_selection" {
		return true
	}
	if strings.TrimSpace(blockID) == "" || strings.TrimSpace(blockID) != strings.TrimSpace(s.TargetBlockID) {
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
	if s.modeLocked() == "" || s.scopeKindLocked() != "partial_selection" {
		return true
	}
	kindUnchanged := strings.TrimSpace(s.TargetBlockKind) == "" || strings.TrimSpace(block.Kind) == strings.TrimSpace(s.TargetBlockKind)
	return kindUnchanged &&
		block.Title == s.TargetBlockTitle &&
		strings.TrimSpace(block.ChartID) == strings.TrimSpace(s.TargetBlockChartID) &&
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

func collectEditableChartIDs(state *ReportState, blockID, chartID string) map[string]struct{} {
	refs := make(map[string]struct{})
	if trimmedChartID := strings.TrimSpace(chartID); trimmedChartID != "" {
		refs[trimmedChartID] = struct{}{}
	}
	if state == nil || strings.TrimSpace(blockID) == "" {
		return refs
	}
	index := findReportBlockIndex(state.Blocks, strings.TrimSpace(blockID))
	if index < 0 {
		return refs
	}
	block := state.Blocks[index]
	if strings.TrimSpace(block.ChartID) != "" {
		refs[strings.TrimSpace(block.ChartID)] = struct{}{}
	}
	for _, ref := range chartRefsOutsideChartBlock(block.Content) {
		refs[ref] = struct{}{}
	}
	return refs
}

func referencedChartsOutsideChartBlocks(blocks []ReportBlock) map[string]struct{} {
	refs := make(map[string]struct{})
	for _, block := range blocks {
		if strings.EqualFold(strings.TrimSpace(block.Kind), "chart") {
			continue
		}
		for _, ref := range chartRefsOutsideChartBlock(block.Content) {
			refs[ref] = struct{}{}
		}
	}
	return refs
}

func renderableReportBlockCount(blocks []ReportBlock) int {
	count := 0
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Kind)) {
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
		chartID := strings.TrimSpace(chart.ID)
		if chartID != "" {
			chartSet[chartID] = struct{}{}
		}
	}

	refCounts := make(map[string]int)
	for _, block := range state.Blocks {
		if strings.EqualFold(strings.TrimSpace(block.Kind), "chart") && strings.TrimSpace(block.ChartID) != "" {
			refCounts[strings.TrimSpace(block.ChartID)]++
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
	chartByID := make(map[string]ChartData, len(state.Charts))
	for _, chart := range state.Charts {
		if id := strings.TrimSpace(chart.ID); id != "" {
			chartByID[id] = chart
		}
	}

	seen := map[string]struct{}{}
	var issues []string
	addIssue := func(issue string) {
		if _, ok := seen[issue]; ok {
			return
		}
		seen[issue] = struct{}{}
		issues = append(issues, issue)
	}

	for _, block := range state.Blocks {
		if !blockNeedsEvidence(block) {
			continue
		}
		chartRefs := chartIDsForBlock(block)
		hasEvidence := len(block.Sources) > 0
		for _, chartID := range chartRefs {
			if chart, ok := chartByID[chartID]; ok && len(chart.Sources) > 0 {
				hasEvidence = true
				break
			}
		}
		if hasEvidence {
			continue
		}
		blockID := strings.TrimSpace(block.ID)
		if blockID == "" {
			blockID = "unknown_block"
		}
		addIssue("missing_evidence:" + blockID)
		for _, chartID := range chartRefs {
			addIssue("missing_chart_evidence:" + chartID)
		}
	}
	return issues
}

func blockNeedsEvidence(block ReportBlock) bool {
	kind := strings.ToLower(strings.TrimSpace(block.Kind))
	if kind == "chart" && strings.TrimSpace(block.ChartID) != "" {
		return true
	}
	if len(chartIDsForBlock(block)) > 0 {
		return true
	}
	text := strings.ToLower(strings.Join([]string{block.Title, block.Content}, "\n"))
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		claimText := normalizeSectionTitle(trimReportLinePrefix(line))
		if strings.TrimSpace(claimText) == "" {
			continue
		}
		if reportNumberPattern.MatchString(claimText) {
			return true
		}
	}
	return false
}

func trimReportLinePrefix(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimLeft(line, "#>")
	line = strings.TrimSpace(line)
	for {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
			line = strings.TrimSpace(trimmed[2:])
			continue
		}
		return trimmed
	}
}

func chartIDsForBlock(block ReportBlock) []string {
	ids := map[string]struct{}{}
	if chartID := strings.TrimSpace(block.ChartID); chartID != "" {
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

func chartEvidenceRefsForBlock(state *ReportState, block ReportBlock) []EvidenceRef {
	if state == nil {
		return nil
	}
	chartRefs := chartIDsForBlock(block)
	if len(chartRefs) == 0 {
		return nil
	}
	refSet := make(map[string]struct{}, len(chartRefs))
	for _, chartID := range chartRefs {
		refSet[chartID] = struct{}{}
	}
	var refs []EvidenceRef
	for _, chart := range state.Charts {
		if _, ok := refSet[strings.TrimSpace(chart.ID)]; ok {
			refs = append(refs, chart.Sources...)
		}
	}
	return refs
}

func reportSemanticFinalizeIssues(state *ReportState, sources []service.SessionSourceSummary, profileDetail ProfileDetailProvider) []string {
	if state == nil || len(sources) == 0 {
		return nil
	}

	var unresolved []service.SessionSourceSummary
	for _, source := range sources {
		if source.AmbiguityCount <= 0 {
			continue
		}
		unresolved = append(unresolved, source)
	}
	if len(unresolved) == 0 {
		return nil
	}

	issues := make([]string, 0, len(unresolved))
	for _, source := range unresolved {
		ref := strings.TrimSpace(source.AnalysisTableName)
		if ref == "" {
			ref = strings.TrimSpace(source.ProfileID)
		}
		if ref == "" {
			ref = strings.TrimSpace(source.SourceID)
		}
		if ref == "" {
			ref = "unknown_source"
		}

		detailText, candidateRefs := unresolvedSemanticDetail(source, profileDetail)
		if !reportExplicitlyUsesUnresolvedSource(state, source, candidateRefs) {
			continue
		}

		var detail strings.Builder
		detail.WriteString(source.DisplayName)
		detail.WriteString(fmt.Sprintf(" (profile=%s", source.ProfileID))
		if detailText != "" {
			detail.WriteString(", ambiguities=[")
			detail.WriteString(detailText)
			detail.WriteString("]")
		}
		detail.WriteString(")")
		detail.WriteString(fmt.Sprintf(", unresolved_ambiguities=%d", source.AmbiguityCount))

		issues = append(issues, "unresolved_semantic_ambiguity:"+ref+":"+(detail.String()))
	}

	return issues
}

func unresolvedSemanticDetail(source service.SessionSourceSummary, profileDetail ProfileDetailProvider) (string, []string) {
	if profileDetail == nil || strings.TrimSpace(source.ProfileID) == "" {
		return "", nil
	}
	profileJSON, _, err := profileDetail(source.ProfileID)
	if err != nil || strings.TrimSpace(profileJSON) == "" {
		return "", nil
	}
	var profile map[string]interface{}
	if json.Unmarshal([]byte(profileJSON), &profile) != nil {
		return "", nil
	}
	amb, ok := profile["ambiguities"].([]interface{})
	if !ok || len(amb) == 0 {
		return "", nil
	}
	var detail strings.Builder
	var candidates []string
	for i, a := range amb {
		am, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		if i > 0 {
			detail.WriteString("; ")
		}
		if kind, _ := am["kind"].(string); kind != "" {
			detail.WriteString(kind)
			detail.WriteString(": ")
		}
		if cands, ok := am["candidates"].([]interface{}); ok {
			names := make([]string, 0, len(cands))
			for _, c := range cands {
				if s, ok := c.(string); ok && strings.TrimSpace(s) != "" {
					names = append(names, s)
					candidates = append(candidates, s)
				}
			}
			detail.WriteString(strings.Join(names, ", "))
		}
	}
	return detail.String(), candidates
}

func reportExplicitlyUsesUnresolvedSource(state *ReportState, source service.SessionSourceSummary, candidateRefs []string) bool {
	if state == nil {
		return false
	}
	table := strings.TrimSpace(source.AnalysisTableName)
	displayName := strings.TrimSpace(source.DisplayName)
	sourceID := strings.TrimSpace(source.SourceID)
	for _, block := range state.Blocks {
		if evidenceRefsUseSource(block.Sources, table, displayName, sourceID) {
			return true
		}
		content := strings.Join([]string{block.ID, block.Title, block.Content, block.ChartID}, "\n")
		if containsMeaningfulToken(content, table) || containsMeaningfulToken(content, displayName) || containsMeaningfulToken(content, sourceID) {
			return true
		}
		if evidenceRefsUseSource(chartEvidenceRefsForBlock(state, block), table, displayName, sourceID) {
			return true
		}
		for _, candidate := range candidateRefs {
			if isSpecificSemanticCandidate(candidate) && containsMeaningfulToken(content, candidate) {
				return true
			}
		}
	}
	return false
}

func evidenceRefsUseSource(refs []EvidenceRef, table, displayName, sourceID string) bool {
	for _, ref := range refs {
		text := strings.Join([]string{ref.TableName, ref.SQL, ref.Summary, ref.ToolName, ref.ChartID}, "\n")
		if containsMeaningfulToken(text, table) || containsMeaningfulToken(text, displayName) || containsMeaningfulToken(text, sourceID) {
			return true
		}
	}
	return false
}

func containsMeaningfulToken(text, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || len([]rune(token)) < 3 {
		return false
	}
	return strings.Contains(strings.ToLower(text), strings.ToLower(token))
}

func isSpecificSemanticCandidate(candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if len([]rune(candidate)) < 5 {
		return false
	}
	return strings.Contains(candidate, "_") || strings.Contains(candidate, ".")
}

func hasDuplicateLeadingHeading(block ReportBlock) bool {
	kind := strings.ToLower(strings.TrimSpace(block.Kind))
	if kind != "markdown" && kind != "html" {
		return false
	}
	if strings.TrimSpace(block.Title) == "" || strings.TrimSpace(block.Content) == "" {
		return false
	}
	lines := strings.Split(block.Content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			return false
		}
		heading := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		return normalizeSectionTitle(heading) == normalizeSectionTitle(block.Title)
	}
	return false
}

func HasDuplicateLeadingHeadingForAgent(block ReportBlock) bool {
	return hasDuplicateLeadingHeading(block)
}

func normalizeSectionTitle(value string) string {
	normalized := strings.TrimSpace(value)
	normalized = sectionChapterPrefixPattern.ReplaceAllString(normalized, "")
	normalized = sectionOrdinalPrefixPattern.ReplaceAllString(normalized, "")
	normalized = sectionWhitespacePattern.ReplaceAllString(normalized, "")
	return normalized
}

func ReportFinalizeIssuesForAgent(state *ReportState) []string {
	return reportFinalizeIssues(state)
}

func ReportFinalizeIssuesForAgentLocked(state *ReportState) []string {
	return reportFinalizeIssues(state)
}
