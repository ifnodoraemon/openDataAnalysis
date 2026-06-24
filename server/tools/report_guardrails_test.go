package tools

import (
	"sync"
	"testing"
)

func TestReportEditStateRefreshFromReportStateCollectsEditableCharts(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Title:   "分析",
				Content: "结论如下：{{chart:chart_inline}}",
				ChartID: "chart_primary",
			},
			{
				ID:      "other",
				Kind:    "chart",
				ChartID: "chart_other",
				Content: "说明",
			},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_block",
		TargetBlockID:       "analysis",
		PreserveOtherBlocks: true,
	}

	editState.RefreshFromReportState(state)

	if len(editState.AllowedChartIDs) != 2 {
		t.Fatalf("expected 2 editable charts, got %#v", editState.AllowedChartIDs)
	}
	if !editState.ChartMutationAllowed("chart_primary") || !editState.ChartMutationAllowed("chart_inline") {
		t.Fatalf("expected referenced charts to remain editable, got %#v", editState.AllowedChartIDs)
	}
	if editState.ChartMutationAllowed("chart_other") {
		t.Fatalf("expected unrelated chart to be blocked, got %#v", editState.AllowedChartIDs)
	}
	if !editState.BlockMutationAllowed("upsert", "analysis") {
		t.Fatal("expected target block upsert to be allowed")
	}
	if editState.BlockMutationAllowed("remove", "analysis") {
		t.Fatal("expected remove to be blocked when preserving other blocks")
	}
	if editState.BlockMutationAllowed("upsert", "other") {
		t.Fatal("expected non-target block to be blocked")
	}
}

func TestReportEditStateChartScopeRestrictsToTargetChart(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Title:   "分析",
				Content: "结论如下：{{chart:chart_inline}}",
				ChartID: "chart_primary",
			},
		},
	}
	editState := &ReportEditState{
		Mode:                "revise_chart",
		TargetChartID:       "chart_inline",
		PreserveOtherBlocks: true,
	}

	editState.RefreshFromReportState(state)

	if editState.ScopeKind() != "partial_chart" {
		t.Fatalf("expected partial_chart scope, got %q", editState.ScopeKind())
	}
	if !editState.ChartMutationAllowed("chart_inline") {
		t.Fatalf("expected target chart to remain editable, got %#v", editState.AllowedChartIDs)
	}
	if editState.ChartMutationAllowed("chart_primary") {
		t.Fatalf("expected unrelated chart to be blocked, got %#v", editState.AllowedChartIDs)
	}
	if editState.BlockMutationAllowed("upsert", "analysis") {
		t.Fatal("expected block mutations to be blocked for chart-only scope")
	}
}

func TestReportEditStateSelectionScopePreservesOutsideText(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "markdown", Content: "前文。需要改写的句子。后文。"},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_selection",
		TargetBlockID:       "analysis",
		SelectionText:       "需要改写的句子",
		SelectionStart:      3,
		SelectionEnd:        10,
		SelectionRangeSet:   true,
		PreserveOtherBlocks: true,
	}
	editState.RefreshFromReportState(state)

	if !editState.SelectionMutationAllowed("analysis", "前文。新的句子。后文。") {
		t.Fatal("expected replacement inside selection to be allowed")
	}
	if editState.SelectionMutationAllowed("analysis", "前文也改了。新的句子。后文。") {
		t.Fatal("expected prefix mutation outside selection to be blocked")
	}
	if editState.SelectionMutationAllowed("analysis", "前文。新的句子。后文也改了。") {
		t.Fatal("expected suffix mutation outside selection to be blocked")
	}
}

func TestReportEditStateSelectionScopeUsesRenderedTextProjection(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "markdown", Content: "# 概览\n\n指标 **收入** [详情](https://example.com)"},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_selection",
		TargetBlockID:       "analysis",
		SelectionText:       "收入",
		SelectionStart:      8,
		SelectionEnd:        10,
		SelectionRangeSet:   true,
		PreserveOtherBlocks: true,
	}
	editState.RefreshFromReportState(state)

	if !editState.SelectionMutationAllowed("analysis", "# 概览\n\n指标 **利润** [详情](https://example.com)") {
		t.Fatal("expected replacement inside rendered markdown selection to be allowed")
	}
	if editState.SelectionMutationAllowed("analysis", "# 概览\n\n指标 **利润** [详情](https://changed.example.com)") {
		t.Fatal("expected link target outside selected text to remain protected")
	}
}

func TestReportEditStateSelectionScopePreservesBlockMetadata(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{
				ID:      "analysis",
				Kind:    "markdown",
				Title:   "分析",
				Content: "前文。需要改写的句子。后文。",
				Sources: []EvidenceRef{{Kind: "sql", SQL: "select 1"}},
			},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_selection",
		TargetBlockID:       "analysis",
		SelectionText:       "需要改写的句子",
		SelectionStart:      3,
		SelectionEnd:        10,
		SelectionRangeSet:   true,
		PreserveOtherBlocks: true,
	}
	editState.RefreshFromReportState(state)

	if !editState.SelectionBlockMutationAllowed(ReportBlock{
		ID:      "analysis",
		Kind:    "markdown",
		Title:   "分析",
		Content: "前文。新的句子。后文。",
		Sources: []EvidenceRef{{Kind: "sql", SQL: "select 1"}},
	}) {
		t.Fatal("expected content-only replacement to be allowed")
	}
	if editState.SelectionBlockMutationAllowed(ReportBlock{
		ID:      "analysis",
		Kind:    "markdown",
		Title:   "改名",
		Content: "前文。新的句子。后文。",
		Sources: []EvidenceRef{{Kind: "sql", SQL: "select 1"}},
	}) {
		t.Fatal("expected title mutation outside selected text to be blocked")
	}
	if editState.SelectionBlockMutationAllowed(ReportBlock{
		ID:      "analysis",
		Kind:    "markdown",
		Title:   "分析",
		Content: "前文。新的句子。后文。",
		Sources: []EvidenceRef{{Kind: "sql", SQL: "select 2"}},
	}) {
		t.Fatal("expected source mutation outside selected text to be blocked")
	}
}

func TestReportEditStateSelectionScopeRejectsMissingRange(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "markdown", Content: "收入上涨。成本下降。收入贡献最大。"},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_selection",
		TargetBlockID:       "analysis",
		SelectionText:       "收入",
		PreserveOtherBlocks: true,
	}
	editState.RefreshFromReportState(state)

	if editState.SelectionMutationAllowed("analysis", "营收上涨。成本下降。收入贡献最大。") {
		t.Fatal("expected missing selection range to be rejected instead of guessing first repeated text")
	}
}

func TestReportEditStateSelectionScopeDecodesHTMLEntities(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "html", Content: "<p>A &amp; B improved.</p>"},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_selection",
		TargetBlockID:       "analysis",
		SelectionText:       "A & B",
		SelectionStart:      0,
		SelectionEnd:        5,
		SelectionRangeSet:   true,
		PreserveOtherBlocks: true,
	}
	editState.RefreshFromReportState(state)

	if !editState.SelectionMutationAllowed("analysis", "<p>C &amp; D improved.</p>") {
		t.Fatal("expected replacement inside decoded HTML entity selection to be allowed")
	}
	if editState.SelectionMutationAllowed("analysis", "<p>C &amp; D regressed.</p>") {
		t.Fatal("expected suffix outside decoded HTML entity selection to stay protected")
	}
}

func TestReportEditStateSelectionScopeNormalizesDecodedWhitespace(t *testing.T) {
	t.Parallel()

	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "analysis", Kind: "html", Content: "<p>A&nbsp;B improved.</p>"},
		},
	}
	editState := &ReportEditState{
		Mode:                "regenerate_selection",
		TargetBlockID:       "analysis",
		SelectionText:       "A B",
		SelectionStart:      0,
		SelectionEnd:        3,
		SelectionRangeSet:   true,
		PreserveOtherBlocks: true,
	}
	editState.RefreshFromReportState(state)

	if !editState.SelectionMutationAllowed("analysis", "<p>C&nbsp;D improved.</p>") {
		t.Fatal("expected selection containing decoded non-breaking space to be allowed")
	}
}

func TestNormalizeSectionTitleStripsCommonOrdinalPrefixes(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"二、各维度分布":      "各维度分布",
		"2. 各维度分布":     "各维度分布",
		"第3章 各维度分布":    "各维度分布",
		"  第十部分 经营分析 ": "经营分析",
	}

	for input, want := range cases {
		if got := normalizeSectionTitle(input); got != want {
			t.Fatalf("normalizeSectionTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestReportEditStateConcurrentRefreshAndSnapshot(t *testing.T) {
	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "b1", Kind: "markdown", ChartID: "c1", Content: "{{chart:c2}}"},
		},
	}
	edit := &ReportEditState{
		Mode:                "regenerate_block",
		TargetBlockID:       "b1",
		PreserveOtherBlocks: true,
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			edit.RefreshFromReportState(state)
		}()
		go func() {
			defer wg.Done()
			_ = edit.Snapshot()
		}()
	}
	wg.Wait()
}

func TestReportEditStateConcurrentChartMutationReadAndRefresh(t *testing.T) {
	state := &ReportState{
		Blocks: []ReportBlock{
			{ID: "b1", Kind: "markdown", ChartID: "c1", Content: "{{chart:c2}}"},
		},
	}
	edit := &ReportEditState{
		Mode:                "regenerate_block",
		TargetBlockID:       "b1",
		PreserveOtherBlocks: true,
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			edit.RefreshFromReportState(state)
		}()
		go func() {
			defer wg.Done()
			_ = edit.ChartMutationAllowed("c1")
		}()
	}
	wg.Wait()
}

func TestReportEditStateConcurrentResetAndSnapshot(t *testing.T) {
	edit := &ReportEditState{
		Mode:          "revise_block",
		TargetBlockID: "b1",
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			edit.Reset()
		}()
		go func() {
			defer wg.Done()
			_ = edit.Snapshot()
		}()
	}
	wg.Wait()
}

func TestReportEditStateConcurrentScopeKindAndReset(t *testing.T) {
	edit := &ReportEditState{
		Mode:                "regenerate_block",
		TargetBlockID:       "b1",
		TargetChartID:       "c1",
		PreserveOtherBlocks: true,
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = edit.ScopeKind()
		}()
		go func() {
			defer wg.Done()
			edit.Reset()
		}()
	}
	wg.Wait()
}

func TestReportEditStateConcurrentBlockMutationAndReset(t *testing.T) {
	edit := &ReportEditState{
		Mode:                "regenerate_block",
		TargetBlockID:       "b1",
		PreserveOtherBlocks: true,
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = edit.BlockMutationAllowed("upsert", "b1")
		}()
		go func() {
			defer wg.Done()
			edit.Reset()
		}()
	}
	wg.Wait()
}

func TestBlockNeedsEvidenceForGenericNumericClaims(t *testing.T) {
	t.Parallel()

	if !blockNeedsEvidence(ReportBlock{ID: "analysis", Kind: "markdown", Content: "设备温度升高 3.5 摄氏度。"}) {
		t.Fatal("expected generic numeric claim to require evidence without domain keywords")
	}
	if !blockNeedsEvidence(ReportBlock{ID: "analysis", Kind: "markdown", Content: "模型响应延迟下降 18%。"}) {
		t.Fatal("expected percentage claim to require evidence without business keywords")
	}
}

func TestBlockNeedsEvidenceIgnoresStructuralSectionOrdinals(t *testing.T) {
	t.Parallel()

	block := ReportBlock{ID: "overview", Kind: "markdown", Title: "第1章 数据概览", Content: "## 2. 字段说明\n\n正文"}
	if blockNeedsEvidence(block) {
		t.Fatal("expected structural section ordinals without numeric claims to skip evidence requirement")
	}
}
