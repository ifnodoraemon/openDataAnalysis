package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type stubSnapshotLoader struct {
	snapshot *domain.ReportSnapshot
	err      error
	calls    int
	runID    string
}

func (s *stubSnapshotLoader) LoadReportSnapshot(_ context.Context, _, _, _, runID string) (*domain.ReportSnapshot, error) {
	s.calls++
	s.runID = runID
	return s.snapshot, s.err
}

func TestNewSessionToleratesMissingGlobalConfig(t *testing.T) {
	prev := config.Cfg
	config.Cfg = nil
	t.Cleanup(func() { config.Cfg = prev })
	t.Setenv("PROXY_TOKEN", "")

	sess, err := New("sess_1", "ws_1", "user_1", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if sess.Engine == nil || sess.Registry == nil {
		t.Fatalf("expected session runtime to initialize")
	}
	for _, toolName := range []string{
		"state_session_sources_inspect",
		"state_semantic_profile_inspect",
		"state_governance_inspect",
		"profile_patch_commit",
	} {
		if sess.Registry.HasTool(toolName) {
			t.Fatalf("source capability %s must be absent when the source service is unavailable", toolName)
		}
	}
}

func TestPrepareUserRunLoadsSnapshotThroughLoader(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID:          "sess_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		ReportState: &tools.ReportState{},
		EditState:   &tools.ReportEditState{},
	}
	loader := &stubSnapshotLoader{
		snapshot: &domain.ReportSnapshot{
			Title:         "原报告",
			Author:        "tester",
			NeedsFinalize: true,
			Blocks: []domain.ReportSnapshotBlock{
				{ID: "b1", Kind: "chart", Title: "趋势", ChartID: "chart_1"},
			},
			Charts: []domain.ReportSnapshotChart{
				{ID: "chart_1", Option: json.RawMessage(`{}`)},
			},
		},
	}

	err := sess.PrepareUserRun(context.Background(), agent.UserMessage{
		Content: "重写这一段",
		EditContext: &agent.ReportEditContext{
			ScopeKind:   "partial_block",
			TargetRunID: "run_123",
			BlockID:     "b1",
		},
	}, loader)
	if err != nil {
		t.Fatalf("prepare user run: %v", err)
	}
	if loader.calls != 1 || loader.runID != "run_123" {
		t.Fatalf("expected loader to be called for target run, calls=%d runID=%q", loader.calls, loader.runID)
	}
	if sess.ReportState.FinalTitle != "原报告" || len(sess.ReportState.Blocks) != 1 {
		t.Fatalf("expected snapshot to be loaded into report state: %#v", sess.ReportState)
	}
	if !sess.ReportState.NeedsFinalize {
		t.Fatalf("expected draft snapshot to preserve needs_finalize, got %#v", sess.ReportState)
	}
	if sess.EditState.TargetBlockID != "b1" || len(sess.EditState.AllowedChartIDs) != 1 {
		t.Fatalf("expected edit state to refresh from loaded snapshot: %#v", sess.EditState)
	}
}

func TestPrepareUserRunWithoutEditTargetDoesNotLoadSnapshot(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID:          "sess_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		ReportState: &tools.ReportState{},
		EditState:   &tools.ReportEditState{},
	}
	loader := &stubSnapshotLoader{}

	err := sess.PrepareUserRun(context.Background(), agent.UserMessage{
		Content: "继续分析",
	}, loader)
	if err != nil {
		t.Fatalf("prepare user run: %v", err)
	}
	if loader.calls != 0 {
		t.Fatalf("expected loader to remain unused, calls=%d", loader.calls)
	}
}

func TestPrepareUserRunLoadsSnapshotFromTurnContextTarget(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID:          "sess_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		ReportState: &tools.ReportState{},
		EditState:   &tools.ReportEditState{},
	}
	loader := &stubSnapshotLoader{
		snapshot: &domain.ReportSnapshot{
			Title:         "历史报告",
			Author:        "tester",
			NeedsFinalize: true,
			Blocks: []domain.ReportSnapshotBlock{
				{ID: "b1", Kind: "markdown", Title: "结论", Content: "结论内容"},
			},
		},
	}

	err := sess.PrepareUserRun(context.Background(), agent.UserMessage{
		Content: "把这份历史报告整体整理一下",
		TurnContext: &agent.TurnContext{
			ReportTargetRunID: "run_history_1",
			ReportTitle:       "历史报告",
		},
	}, loader)
	if err != nil {
		t.Fatalf("prepare user run: %v", err)
	}
	if loader.calls != 1 || loader.runID != "run_history_1" {
		t.Fatalf("expected loader to be called for turn context run, calls=%d runID=%q", loader.calls, loader.runID)
	}
	if sess.ReportState.FinalTitle != "历史报告" || len(sess.ReportState.Blocks) != 1 {
		t.Fatalf("expected turn-context snapshot to be loaded into report state: %#v", sess.ReportState)
	}
}

func TestSessionRuntimeVars(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID: "test-sess",
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "markdown", Title: "Overview", Content: "body"},
			},
			NeedsFinalize: true,
		},
	}

	// 1. Test EditScope
	sess.EditState = &tools.ReportEditState{
		ScopeKindValue: "partial_block",
		TargetBlockID:  "b1",
		SelectionText:  "hello",
	}
	vars := sess.RuntimeVars()
	if len(vars) != 1 {
		t.Fatalf("expected 1 runtime fact, got %v", vars)
	}
	if vars[0].Name != "active_edit_scope" {
		t.Fatalf("expected active_edit_scope, got %v", vars)
	}
	if !strings.Contains(vars[0].Content, "ScopeKind: partial_block") {
		t.Errorf("missing ScopeKind in edit scope fact: %s", vars[0].Content)
	}
	if strings.Contains(vars[0].Content, "请仅在允许的边界") {
		t.Errorf("imperative hint should not be present in edit scope")
	}
}

func TestSessionRuntimeVarsChartScope(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID: "test-sess",
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "chart", Title: "销售趋势", ChartID: "chart_sales"},
			},
			Charts: []tools.ChartData{
				{ID: "chart_sales"},
			},
			NeedsFinalize: true,
		},
		EditState: &tools.ReportEditState{
			ScopeKindValue: "partial_chart",
			TargetChartID:  "chart_sales",
		},
	}

	vars := sess.RuntimeVars()
	if len(vars) != 1 {
		t.Fatalf("expected 1 runtime fact, got %v", vars)
	}
	if !strings.Contains(vars[0].Content, "ScopeKind: partial_chart") {
		t.Fatalf("missing partial_chart scope fact: %s", vars[0].Content)
	}
	if !strings.Contains(vars[0].Content, "TargetChartID: chart_sales") {
		t.Fatalf("missing TargetChartID fact: %s", vars[0].Content)
	}
}

func TestSessionRuntimeVarsSelectionScope(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID: "test-sess",
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "markdown", Title: "结论", Content: "body"},
			},
			NeedsFinalize: true,
		},
		EditState: &tools.ReportEditState{
			ScopeKindValue:    "partial_selection",
			TargetBlockID:     "b1",
			TargetBlockLabel:  "结论",
			SelectionText:     "其中这句需要改",
			SelectionStart:    0,
			SelectionEnd:      7,
			SelectionRangeSet: true,
		},
	}

	vars := sess.RuntimeVars()
	if len(vars) != 1 {
		t.Fatalf("expected 1 runtime fact, got %v", vars)
	}
	if !strings.Contains(vars[0].Content, "ScopeKind: partial_selection") {
		t.Fatalf("missing partial_selection scope fact: %s", vars[0].Content)
	}
	if !strings.Contains(vars[0].Content, "SelectionText: 其中这句需要改") {
		t.Fatalf("missing selection text fact: %s", vars[0].Content)
	}
	if !strings.Contains(vars[0].Content, "SelectionRange: 0-7") {
		t.Fatalf("missing selection range fact: %s", vars[0].Content)
	}
	if !strings.Contains(vars[0].Content, "MutationContract: only the target block content may change") {
		t.Fatalf("missing mutation contract fact: %s", vars[0].Content)
	}
}

func TestSessionRuntimeVarsLayoutScope(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID: "test-sess",
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "markdown", Title: "Overview", Content: "body"},
			},
			NeedsFinalize: true,
		},
		EditState: &tools.ReportEditState{
			ScopeKindValue: "layout",
		},
	}

	vars := sess.RuntimeVars()
	if len(vars) != 1 {
		t.Fatalf("expected 1 runtime fact, got %v", vars)
	}
	if !strings.Contains(vars[0].Content, "ScopeKind: layout") {
		t.Fatalf("missing layout scope fact: %s", vars[0].Content)
	}
}

func TestSessionRuntimeVarsDoNotInjectGoalState(t *testing.T) {
	t.Parallel()

	subgoals := agent.NewSubgoalManager()
	rootID, err := subgoals.AddGoalWithBlocking("整理当前报告结构", "", false)
	if err != nil {
		t.Fatalf("add root goal: %v", err)
	}
	childID, err := subgoals.AddGoalWithBlocking("重写结论部分", rootID, false)
	if err != nil {
		t.Fatalf("add child goal: %v", err)
	}
	if err := subgoals.UpdateGoalStatus(childID, agent.StatusRunning, ""); err != nil {
		t.Fatalf("mark child running: %v", err)
	}

	sess := &Session{
		ID:        "test-sess",
		Subgoals:  subgoals,
		EditState: &tools.ReportEditState{},
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "markdown", Title: "Overview", Content: "body"},
			},
		},
	}

	vars := sess.RuntimeVars()
	if len(vars) != 0 {
		t.Fatalf("expected goal/report state to stay pull-based, got %#v; root=%s", vars, rootID)
	}
}

func TestSessionWaitingRunRecovery(t *testing.T) {
	t.Parallel()

	sess := &Session{
		ID: "sess_waiting",
		ActiveRun: &RunState{
			RunID:  "run_waiting_1",
			Status: "running",
		},
	}

	sess.SuspendRun("run_waiting_1")

	runID, isWaiting := sess.GetWaitingRunID()
	if !isWaiting || runID != "run_waiting_1" {
		t.Fatalf("expected waiting run run_waiting_1, got waiting=%t, id=%s", isWaiting, runID)
	}

	consumedRunID := sess.ConsumeWaitingRun()
	if consumedRunID != "run_waiting_1" {
		t.Fatalf("expected to consume run_waiting_1, got %s", consumedRunID)
	}

	runID, isWaiting = sess.GetWaitingRunID()
	if isWaiting || runID != "" {
		t.Fatalf("expected no waiting run after consume, got waiting=%t, id=%s", isWaiting, runID)
	}
}
