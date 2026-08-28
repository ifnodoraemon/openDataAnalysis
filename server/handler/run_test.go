package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func TestSummarizeRunMessagePrefersUISummary(t *testing.T) {
	t.Parallel()

	msg := domain.RunMessage{
		Type:    "tool_result",
		Name:    "task_delegate",
		Content: `{"tool":"task_delegate","ui_summary":"子 Agent researcher 已完成: 收集到了 3 个事实"}`,
	}

	summary := summarizeRunMessage(msg)
	if summary != "子 Agent researcher 已完成: 收集到了 3 个事实" {
		t.Fatalf("expected ui_summary to win, got %q", summary)
	}
}

func TestRenderReportHTMLFromSnapshotRegeneratesCurrentTemplate(t *testing.T) {
	t.Parallel()

	report := &domain.Report{
		Title: "测试报告",
		SnapshotJSON: `{
			"version":"v3",
			"title":"测试报告",
			"author":"AI",
			"blocks":[
				{"id":"blk_1","kind":"markdown","title":"一、概览","content":"说明"},
				{"id":"blk_chart","kind":"chart","title":"趋势图","content":"图表说明","chartId":"chart_sales"}
			],
			"charts":[
				{"id":"chart_sales","option":{"series":[{"type":"bar","data":[1]}]}}
			]
		}`,
	}

	html, err := renderReportHTMLFromSnapshot(report)
	if err != nil {
		t.Fatalf("expected snapshot to be rendered: %v", err)
	}
	if !strings.Contains(html, `<h2>一、概览</h2>`) {
		t.Fatalf("expected regenerated html to retain original prefixed titles, got: %s", html)
	}
	if !strings.Contains(html, `data-chart-option="`) || !strings.Contains(html, `id="oda-chart-runtime" src="/oda-chart-runtime.js"`) {
		t.Fatalf("expected regenerated html to use chart data attributes and external runtime, got: %s", html)
	}
	if strings.Contains(html, `data-block-id="blk_chart" data-block-kind="chart" data-chart-id="chart_sales"`) {
		t.Fatalf("expected chart wrapper not to retain duplicate data-chart-id, got: %s", html)
	}
}

func TestAttachRunRuntimeStateRejectsUnavailablePersistence(t *testing.T) {
	t.Parallel()

	prevSessionManager := sessionManager
	prevMessageRepo := messageRepo
	t.Cleanup(func() {
		sessionManager = prevSessionManager
		messageRepo = prevMessageRepo
	})

	sessionManager = session.NewManager(t.TempDir(), nil, nil)
	sess, _, err := sessionManager.GetOrCreate(context.Background(), "sess_live", "ws_1", "user_1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	sess.ReportState.FinalTitle = "实时草稿"
	sess.ReportState.Blocks = []tools.ReportBlock{
		{ID: "blk_1", Kind: "markdown", Title: "概览", Content: "这里是共享草稿内容"},
	}
	messageRepo = nil

	resp := map[string]interface{}{}
	err = attachRunRuntimeState(context.Background(), resp, domain.AnalysisRun{
		ID:          "run_live",
		SessionID:   "sess_live",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
	})
	if err == nil || !strings.Contains(err.Error(), "repositories are not configured") {
		t.Fatalf("expected unavailable persistence error, got %v", err)
	}
}

func TestHandlerReportSnapshotLoaderUsesTargetRunDraftEvents(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	prevRunRepo := runRepo
	prevReportRepo := reportRepo
	prevMessageRepo := messageRepo
	prevSessionRepo := sessionRepo
	prevSessionManager := sessionManager
	t.Cleanup(func() {
		runRepo = prevRunRepo
		reportRepo = prevReportRepo
		messageRepo = prevMessageRepo
		sessionRepo = prevSessionRepo
		sessionManager = prevSessionManager
	})

	userRepo := sqliterepo.NewUserRepository(store.DB)
	workspaceRepo := sqliterepo.NewWorkspaceRepository(store.DB)
	sessionRepo = sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	reportRepo = sqliterepo.NewReportRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)
	sessionManager = nil

	now := time.Now()
	if err := userRepo.Create(ctx, &domain.User{
		ID:           "user_1",
		Email:        "user@example.com",
		PasswordHash: "hash",
		Name:         "User",
		Status:       domain.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := workspaceRepo.CreateWorkspace(ctx, &domain.Workspace{
		ID:          "ws_1",
		Name:        "Workspace",
		Slug:        "workspace",
		OwnerUserID: "user_1",
		Status:      domain.WorkspaceStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := workspaceRepo.AddMember(ctx, &domain.WorkspaceMember{
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		Role:        domain.WorkspaceRoleOwner,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("add workspace member: %v", err)
	}
	if err := sessionRepo.Create(ctx, &domain.Session{
		ID:          "sess_live",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		Title:       "Session",
		Status:      domain.SessionStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastSeenAt:  now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	run := &domain.AnalysisRun{
		ID:           "run_live_draft",
		SessionID:    "sess_live",
		WorkspaceID:  "ws_1",
		UserID:       "user_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "draft",
	}
	if err := runRepo.Create(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "msg_live_report",
		RunID:       "run_live_draft",
		SessionID:   "sess_live",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventReportUpdate),
		Content: `{
			"report_snapshot":{
				"title":"当前草稿",
				"needsFinalize":true,
				"blocks":[{"id":"blk_live","kind":"markdown","content":"live draft body"}],
				"charts":[]
			}
		}`,
		CreatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("create report update message: %v", err)
	}

	loader := handlerReportSnapshotLoader{}
	snapshot, err := loader.LoadReportSnapshot(ctx, "sess_live", "ws_1", "user_1", "run_live_draft")
	if err != nil {
		t.Fatalf("expected persisted draft snapshot, got err=%v", err)
	}
	if snapshot == nil || snapshot.Title != "当前草稿" || !snapshot.NeedsFinalize {
		t.Fatalf("expected persisted draft snapshot, got %#v", snapshot)
	}
	if len(snapshot.Blocks) != 1 || snapshot.Blocks[0].ID != "blk_live" {
		t.Fatalf("expected persisted draft blocks, got %#v", snapshot)
	}
}
