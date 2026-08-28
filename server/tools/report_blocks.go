package tools

import (
	"fmt"
	"regexp"
	"strings"
)

var chartReferenceRegexp = regexp.MustCompile(`\{\{chart:([^{}\r\n]+)\}\}`)

func validateExactReportField(field, value string, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain leading or trailing whitespace", field)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s contains a NUL byte", field)
	}
	return nil
}

type reportBlockMutationParams struct {
	Action        string        `json:"action"`
	BlockID       string        `json:"block_id"`
	BlockKind     string        `json:"block_kind"`
	Title         string        `json:"title"`
	Content       string        `json:"content"`
	ChartID       string        `json:"chart_id"`
	BeforeBlockID string        `json:"before_block_id"`
	AfterBlockID  string        `json:"after_block_id"`
	Sources       []EvidenceRef `json:"sources"`
}

type reportBlockMutationResult struct {
	Action     string
	BlockID    string
	BlockKind  string
	BlockCount int
	UISummary  string
}

type reportBlockScopeError struct {
	Action  string
	BlockID string
}

func (e reportBlockScopeError) Error() string {
	return fmt.Sprintf("block %s is outside editable scope for %s", e.BlockID, e.Action)
}

func applyReportBlockMutation(state *ReportState, editState *ReportEditState, params reportBlockMutationParams) (reportBlockMutationResult, error) {
	if state == nil {
		return reportBlockMutationResult{}, fmt.Errorf("report state is not initialized")
	}

	if err := validateExactReportField("action", params.Action, true); err != nil {
		return reportBlockMutationResult{}, err
	}
	if err := validateExactReportField("block_id", params.BlockID, true); err != nil {
		return reportBlockMutationResult{}, err
	}
	for field, value := range map[string]string{
		"before_block_id": params.BeforeBlockID,
		"after_block_id":  params.AfterBlockID,
	} {
		if err := validateExactReportField(field, value, false); err != nil {
			return reportBlockMutationResult{}, err
		}
	}

	switch params.Action {
	case "append", "upsert":
		return upsertReportBlock(state, editState, params)
	case "remove":
		return removeReportBlock(state, editState, params)
	case "move":
		return moveReportBlock(state, editState, params)
	default:
		return reportBlockMutationResult{}, fmt.Errorf("unknown action: %s", params.Action)
	}
}

func upsertReportBlock(state *ReportState, editState *ReportEditState, params reportBlockMutationParams) (reportBlockMutationResult, error) {
	if err := validateExactReportField("block_kind", params.BlockKind, true); err != nil {
		return reportBlockMutationResult{}, err
	}
	if err := validateExactReportField("chart_id", params.ChartID, false); err != nil {
		return reportBlockMutationResult{}, err
	}
	block, err := buildReportBlock(params.BlockKind, params.BlockID, params.Title, params.Content, params.ChartID, params.Sources)
	if err != nil {
		return reportBlockMutationResult{}, err
	}
	existingIndex := findReportBlockIndex(state.Blocks, block.ID)
	if params.Action == "append" && existingIndex >= 0 {
		return reportBlockMutationResult{}, fmt.Errorf("block_id %s already exists", block.ID)
	}
	if editState != nil && !editState.BlockMutationAllowed(params.Action, block.ID) {
		return reportBlockMutationResult{}, reportBlockScopeError{Action: params.Action, BlockID: block.ID}
	}
	if editState != nil && !editState.SelectionBlockMutationAllowed(block) {
		return reportBlockMutationResult{}, reportBlockScopeError{Action: "partial_selection", BlockID: block.ID}
	}

	workingBlocks := append([]ReportBlock(nil), state.Blocks...)
	insertHintIndex := -1
	summaryText := fmt.Sprintf("内容块 [%s] %s 已写入报告，当前仍为草稿。", block.Kind, block.ID)
	if existingIndex >= 0 {
		workingBlocks = append(workingBlocks[:existingIndex], workingBlocks[existingIndex+1:]...)
		insertHintIndex = existingIndex
		summaryText = fmt.Sprintf("内容块 [%s] %s 已更新，当前仍为草稿。", block.Kind, block.ID)
	}

	insertAt := len(workingBlocks)
	if params.BeforeBlockID == "" && params.AfterBlockID == "" && insertHintIndex >= 0 {
		insertAt = insertHintIndex
	} else {
		insertAt, err = resolveReportBlockInsertIndex(workingBlocks, params.BeforeBlockID, params.AfterBlockID)
		if err != nil {
			return reportBlockMutationResult{}, err
		}
	}

	state.Blocks = insertReportBlockAt(workingBlocks, block, insertAt)
	state.NeedsFinalize = true
	state.MutationVersion++
	return reportBlockMutationResult{
		Action:     params.Action,
		BlockID:    block.ID,
		BlockKind:  block.Kind,
		BlockCount: len(state.Blocks),
		UISummary:  summaryText,
	}, nil
}

func removeReportBlock(state *ReportState, editState *ReportEditState, params reportBlockMutationParams) (reportBlockMutationResult, error) {
	if editState != nil && !editState.BlockMutationAllowed(params.Action, params.BlockID) {
		return reportBlockMutationResult{}, reportBlockScopeError{Action: params.Action, BlockID: params.BlockID}
	}

	index := findReportBlockIndex(state.Blocks, params.BlockID)
	if index < 0 {
		return reportBlockMutationResult{}, fmt.Errorf("block_id %s not found", params.BlockID)
	}

	removed := state.Blocks[index]
	state.Blocks = append(state.Blocks[:index], state.Blocks[index+1:]...)
	state.NeedsFinalize = true
	state.MutationVersion++
	return reportBlockMutationResult{
		Action:     params.Action,
		BlockID:    params.BlockID,
		BlockKind:  removed.Kind,
		BlockCount: len(state.Blocks),
		UISummary:  fmt.Sprintf("内容块 [%s] %s 已从报告移除，当前仍为草稿。", removed.Kind, removed.ID),
	}, nil
}

func moveReportBlock(state *ReportState, editState *ReportEditState, params reportBlockMutationParams) (reportBlockMutationResult, error) {
	if editState != nil && !editState.BlockMutationAllowed(params.Action, params.BlockID) {
		return reportBlockMutationResult{}, reportBlockScopeError{Action: params.Action, BlockID: params.BlockID}
	}

	index := findReportBlockIndex(state.Blocks, params.BlockID)
	if index < 0 {
		return reportBlockMutationResult{}, fmt.Errorf("block_id %s not found", params.BlockID)
	}

	block := state.Blocks[index]
	blocks := append([]ReportBlock{}, state.Blocks[:index]...)
	blocks = append(blocks, state.Blocks[index+1:]...)
	insertAt, err := resolveReportBlockInsertIndex(blocks, params.BeforeBlockID, params.AfterBlockID)
	if err != nil {
		return reportBlockMutationResult{}, err
	}

	state.Blocks = insertReportBlockAt(blocks, block, insertAt)
	state.NeedsFinalize = true
	state.MutationVersion++
	return reportBlockMutationResult{
		Action:     params.Action,
		BlockID:    params.BlockID,
		BlockKind:  block.Kind,
		BlockCount: len(state.Blocks),
		UISummary:  fmt.Sprintf("内容块 [%s] %s 已重新排序，当前仍为草稿。", block.Kind, block.ID),
	}, nil
}

func buildReportBlock(kind, blockID, title, content, chartID string, sources []EvidenceRef) (ReportBlock, error) {
	for index, source := range sources {
		if err := validateEvidenceRefShape(source); err != nil {
			return ReportBlock{}, fmt.Errorf("sources[%d]: %w", index, err)
		}
	}
	block := ReportBlock{
		ID:      blockID,
		Kind:    kind,
		Title:   title,
		Content: content,
		ChartID: chartID,
		Sources: sources,
	}
	switch kind {
	case "markdown", "html":
		if strings.TrimSpace(content) == "" {
			return ReportBlock{}, fmt.Errorf("content is required for %s block", kind)
		}
	case "chart":
		if strings.TrimSpace(chartID) == "" {
			return ReportBlock{}, fmt.Errorf("chart_id is required for chart block")
		}
	default:
		return ReportBlock{}, fmt.Errorf("unsupported block_kind: %s", kind)
	}
	return block, nil
}

func findReportBlockIndex(blocks []ReportBlock, blockID string) int {
	for i, block := range blocks {
		if block.ID == blockID {
			return i
		}
	}
	return -1
}

func resolveReportBlockInsertIndex(blocks []ReportBlock, beforeBlockID, afterBlockID string) (int, error) {
	if beforeBlockID != "" && afterBlockID != "" {
		return 0, fmt.Errorf("before_block_id and after_block_id cannot both be set")
	}
	if beforeBlockID != "" {
		index := findReportBlockIndex(blocks, beforeBlockID)
		if index < 0 {
			return 0, fmt.Errorf("before_block_id %s not found", beforeBlockID)
		}
		return index, nil
	}
	if afterBlockID != "" {
		index := findReportBlockIndex(blocks, afterBlockID)
		if index < 0 {
			return 0, fmt.Errorf("after_block_id %s not found", afterBlockID)
		}
		return index + 1, nil
	}
	return len(blocks), nil
}

func insertReportBlockAt(blocks []ReportBlock, block ReportBlock, index int) []ReportBlock {
	if index < 0 {
		index = 0
	}
	if index > len(blocks) {
		index = len(blocks)
	}
	blocks = append(blocks, ReportBlock{})
	copy(blocks[index+1:], blocks[index:])
	blocks[index] = block
	return blocks
}

func ChartReferences(content string) []string {
	matches := chartReferenceRegexp.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	refs := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" && match[1] == strings.TrimSpace(match[1]) {
			refs = append(refs, match[1])
		}
	}
	return refs
}
