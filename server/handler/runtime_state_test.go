package handler

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func TestAttachRunRuntimeStateUsesRunTreeState(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	prevRunRepo := runRepo
	prevMessageRepo := messageRepo
	prevSessionManager := sessionManager
	t.Cleanup(func() {
		runRepo = prevRunRepo
		messageRepo = prevMessageRepo
		sessionManager = prevSessionManager
	})

	userRepo := sqliterepo.NewUserRepository(store.DB)
	workspaceRepo := sqliterepo.NewWorkspaceRepository(store.DB)
	sessionRepo := sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)
	sessionManager = nil

	now := time.Now()
	rootID := "run_root"
	childID := "run_child"

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
		ID:          "sess_1",
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

	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           rootID,
		SessionID:    "sess_1",
		WorkspaceID:  "ws_1",
		UserID:       "user_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "root",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create root run: %v", err)
	}

	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           childID,
		SessionID:    "sess_1",
		WorkspaceID:  "ws_1",
		UserID:       "user_1",
		ParentRunID:  &rootID,
		RunKind:      domain.RunKindDelegate,
		Status:       domain.RunStatusCompleted,
		InputMessage: "child",
		CreatedAt:    now.Add(time.Second),
		UpdatedAt:    now.Add(time.Second),
	}); err != nil {
		t.Fatalf("create child run: %v", err)
	}

	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "msg_1",
		RunID:       rootID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventStateMemoryUpdated),
		Content:     `{"entries":{"retained_context":{"statement":"user-provided context","status":"inferred","created_by":"agent","created_at":"2026-08-11T00:00:00Z"}}}`,
		CreatedAt:   now.Add(2 * time.Second),
	}); err != nil {
		t.Fatalf("create memory state event: %v", err)
	}

	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "msg_3",
		RunID:       childID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventStateSubgoalsUpdated),
		Content:     `{"goals":[{"id":"goal_123","description":"Inspect source quality","status":"pending"}]}`,
		CreatedAt:   now.Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("create subgoal state event: %v", err)
	}

	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "msg_5",
		RunID:       childID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventReportUpdate),
		Content: `{
			"report_snapshot":{
				"title":"共享草稿",
				"needsFinalize":true,
				"blocks":[{"id":"blk_1","kind":"markdown","content":"共享草稿已持久化"}],
				"charts":[]
			}
		}`,
		CreatedAt: now.Add(6 * time.Second),
	}); err != nil {
		t.Fatalf("create report update: %v", err)
	}
	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "msg_6",
		RunID:       childID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventStateReportEditUpdated),
		Content:     `{"active":true,"scopeKind":"partial_selection","editContext":{"scopeKind":"partial_selection","targetRunId":"run_child","blockId":"blk_1","blockLabel":"概览","selectionText":"共享草稿已持久化","selectionStart":0,"selectionEnd":8,"selectionRangeSet":true}}`,
		CreatedAt:   now.Add(7 * time.Second),
	}); err != nil {
		t.Fatalf("create report edit update: %v", err)
	}

	resp := map[string]interface{}{}
	if err := attachRunRuntimeState(ctx, resp, domain.AnalysisRun{
		ID:          rootID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
	}); err != nil {
		t.Fatalf("attach run runtime state: %v", err)
	}

	runtimeState, ok := resp["runtimeState"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected runtimeState map, got %#v", resp["runtimeState"])
	}

	memory, ok := runtimeState["memory_entries"].(map[string]agent.MemoryEntry)
	if !ok {
		t.Fatalf("expected memory entries, got %#v", runtimeState["memory_entries"])
	}
	if memory["retained_context"].Statement != "user-provided context" {
		t.Fatalf("expected run-tree memory state from root run, got %#v", memory)
	}

	subgoals, ok := runtimeState["subgoals"].([]agent.Subgoal)
	if !ok {
		t.Fatalf("expected subgoals slice, got %#v", runtimeState["subgoals"])
	}
	if len(subgoals) != 1 || subgoals[0].ID != "goal_123" {
		t.Fatalf("expected child subgoal to be preserved in run-tree runtime state, got %#v", subgoals)
	}

	reportHTML, ok := runtimeState["report_html"].(string)
	if !ok || reportHTML == "" {
		t.Fatalf("expected persisted report_html, got %#v", runtimeState["report_html"])
	}
	if !strings.Contains(reportHTML, "共享草稿已持久化") {
		t.Fatalf("expected rendered report_html to contain draft body, got %q", reportHTML)
	}
	reportSnapshot, ok := runtimeState["report_snapshot"].(*domain.ReportSnapshot)
	if !ok || reportSnapshot == nil {
		t.Fatalf("expected persisted report_snapshot, got %#v", runtimeState["report_snapshot"])
	}
	if !reportSnapshot.NeedsFinalize || len(reportSnapshot.Blocks) != 1 || reportSnapshot.Blocks[0].ID != "blk_1" {
		t.Fatalf("expected structured draft snapshot, got %#v", reportSnapshot)
	}
	editState, ok := runtimeState["edit_state"].(*agent.EditStateUpdatedData)
	if !ok || editState == nil {
		t.Fatalf("expected persisted edit_state, got %#v", runtimeState["edit_state"])
	}
	if !editState.Active || editState.ScopeKind != "partial_selection" || editState.EditContext == nil || editState.EditContext.BlockID != "blk_1" {
		t.Fatalf("expected grounded edit scope in runtime state, got %#v", editState)
	}
}

func TestDeriveRuntimeStateRegeneratesReportHTMLFromSnapshot(t *testing.T) {
	t.Parallel()

	_, _, reportSnapshot, reportHTML, _, err := deriveRuntimeStateFromMessages([]domain.RunMessage{
		{
			ID:          "msg_report_update",
			RunID:       "run_1",
			SessionID:   "sess_1",
			WorkspaceID: "ws_1",
			Type:        string(agent.EventReportFinal),
			Content: `{
				"html":"<p>stale rendered html</p>",
				"report_snapshot":{
					"title":"结构化报告标题",
					"author":"AI",
					"needsFinalize":false,
					"blocks":[{"id":"blk_1","kind":"markdown","title":"执行摘要","content":"正文"}],
					"charts":[]
				}
			}`,
			CreatedAt: time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("derive runtime state: %v", err)
	}

	if reportSnapshot == nil || reportSnapshot.Title != "结构化报告标题" {
		t.Fatalf("expected structured report snapshot, got %#v", reportSnapshot)
	}
	if strings.Contains(reportHTML, "stale rendered html") {
		t.Fatalf("expected runtime report html to be regenerated from snapshot, got %q", reportHTML)
	}
	if !strings.Contains(reportHTML, `<h1>结构化报告标题</h1>`) || !strings.Contains(reportHTML, "执行摘要") {
		t.Fatalf("expected regenerated report html to include snapshot title and content, got %q", reportHTML)
	}
}

func TestHydrateSessionFromPersistenceRestoresStructuredReportState(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	setupRuntimeStateRepos(t, ctx, store)

	now := time.Now()
	rootID := "run_report_root"

	mustCreateRunMessage(t, ctx, &domain.AnalysisRun{
		ID:           rootID,
		SessionID:    "sess_1",
		WorkspaceID:  "ws_1",
		UserID:       "user_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "draft",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	mustCreateRunMessage(t, ctx, &domain.RunMessage{
		ID:          "msg_report_call",
		RunID:       rootID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventStateMemoryUpdated),
		Content:     `{"entries":{"report_scope":{"statement":"draft report should survive restart","status":"inferred","created_by":"agent","created_at":"2026-08-11T00:00:00Z"}}}`,
		CreatedAt:   now.Add(time.Second),
	})
	mustCreateRunMessage(t, ctx, &domain.RunMessage{
		ID:          "msg_report_update",
		RunID:       rootID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventReportUpdate),
		Content: `{
			"html":"<p>draft body</p>",
			"report_snapshot":{
				"title":"恢复后的草稿",
				"author":"Analyst",
				"needsFinalize":true,
				"layout":{"customCss":"body { color: red; }"},
				"blocks":[{"id":"blk_1","kind":"markdown","title":"概览","content":"draft body"}],
				"charts":[{"id":"chart_1","option":{"series":[{"type":"bar","data":[1]}]}}]
			}
		}`,
		CreatedAt: now.Add(3 * time.Second),
	})
	mustCreateRunMessage(t, ctx, &domain.RunMessage{
		ID:          "msg_report_edit",
		RunID:       rootID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventStateReportEditUpdated),
		Content:     `{"active":true,"scopeKind":"partial_selection","editContext":{"scopeKind":"partial_selection","targetRunId":"run_report_root","blockId":"blk_1","blockLabel":"概览","selectionText":"draft body","selectionStart":0,"selectionEnd":10,"selectionRangeSet":true}}`,
		CreatedAt:   now.Add(4 * time.Second),
	})

	sess := &session.Session{
		ID:          "sess_1",
		WorkspaceID: "ws_1",
		UserID:      "user_1",
		ReportState: &tools.ReportState{},
		EditState:   &tools.ReportEditState{},
		Memory:      agent.NewWorkingMemory(),
		Subgoals:    agent.NewSubgoalManager(),
	}

	if err := hydrateSessionFromPersistence(ctx, sess); err != nil {
		t.Fatalf("hydrate session: %v", err)
	}

	if got := sess.Memory.Snapshot()["report_scope"]; got != "draft report should survive restart" {
		t.Fatalf("expected working memory to be restored, got %q", got)
	}
	if sess.ReportState.FinalTitle != "恢复后的草稿" || sess.ReportState.FinalAuthor != "Analyst" {
		t.Fatalf("expected report metadata to be restored, got %#v", sess.ReportState)
	}
	if !sess.ReportState.NeedsFinalize {
		t.Fatalf("expected draft report to remain draft after hydrate, got %#v", sess.ReportState)
	}
	if len(sess.ReportState.Blocks) != 1 || sess.ReportState.Blocks[0].ID != "blk_1" {
		t.Fatalf("expected report blocks to be restored, got %#v", sess.ReportState.Blocks)
	}
	if len(sess.ReportState.Charts) != 1 || sess.ReportState.Charts[0].ID != "chart_1" {
		t.Fatalf("expected report charts to be restored, got %#v", sess.ReportState.Charts)
	}
	if sess.EditState == nil || !sess.EditState.Active() {
		t.Fatalf("expected edit scope to be restored, got %#v", sess.EditState)
	}
	if sess.EditState.TargetBlockID != "blk_1" || sess.EditState.SelectionText != "draft body" || sess.EditState.SelectionStart != 0 || sess.EditState.SelectionEnd != 10 {
		t.Fatalf("expected selection edit scope to be restored, got %#v", sess.EditState)
	}
}

func TestLoadSessionRuntimeStateFromPersistenceDoesNotTruncateLongHistory(t *testing.T) {
	ctx := context.Background()
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	setupRuntimeStateRepos(t, ctx, store)

	now := time.Now()
	oldestRunID := ""
	for i := 0; i < 1001; i++ {
		runID := "run_bulk_" + strconv.Itoa(i)
		createdAt := now.Add(time.Duration(i) * time.Second)
		mustCreateRunMessage(t, ctx, &domain.AnalysisRun{
			ID:           runID,
			SessionID:    "sess_1",
			WorkspaceID:  "ws_1",
			UserID:       "user_1",
			RunKind:      domain.RunKindRoot,
			Status:       domain.RunStatusCompleted,
			InputMessage: runID,
			CreatedAt:    createdAt,
			UpdatedAt:    createdAt,
		})
		if i == 0 {
			oldestRunID = runID
			mustCreateRunMessage(t, ctx, &domain.RunMessage{
				ID:          "msg_old_state",
				RunID:       runID,
				SessionID:   "sess_1",
				WorkspaceID: "ws_1",
				Type:        string(agent.EventStateMemoryUpdated),
				Content:     `{"entries":{"oldest_entry":{"statement":"must survive beyond 1000 roots","status":"inferred","created_by":"agent","created_at":"2026-08-11T00:00:00Z"}}}`,
				CreatedAt:   createdAt.Add(100 * time.Millisecond),
			})
		}
	}

	memory, subgoals, reportSnapshot, reportHTML, editState, err := loadSessionRuntimeStateFromPersistence(ctx, "sess_1")
	if err != nil {
		t.Fatalf("load runtime state: %v", err)
	}
	if memory["oldest_entry"].Statement != "must survive beyond 1000 roots" {
		t.Fatalf("expected oldest root fact from %s to survive unlimited replay, got %#v", oldestRunID, memory)
	}
	if len(subgoals) != 0 {
		t.Fatalf("expected no subgoals in bulk replay test, got %#v", subgoals)
	}
	if reportSnapshot != nil || reportHTML != "" {
		t.Fatalf("expected no report state in bulk replay test, got snapshot=%#v html=%q", reportSnapshot, reportHTML)
	}
	if editState != nil {
		t.Fatalf("expected no edit state in bulk replay test, got %#v", editState)
	}
}

func TestGetSessionRuntimeStatePrefersLiveSessionStateOverPersistedDraft(t *testing.T) {
	ctx := context.Background()
	prevCfg := config.Cfg
	config.Cfg = &config.Config{}
	t.Cleanup(func() { config.Cfg = prevCfg })
	store, err := metadata.Open(t.TempDir() + "/metadata.db")
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() {
		_ = store.DB.Close()
	})

	setupRuntimeStateRepos(t, ctx, store)

	now := time.Now()
	rootID := "run_live_vs_persisted"
	mustCreateRunMessage(t, ctx, &domain.AnalysisRun{
		ID:           rootID,
		SessionID:    "sess_1",
		WorkspaceID:  "ws_1",
		UserID:       "user_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "draft",
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	mustCreateRunMessage(t, ctx, &domain.RunMessage{
		ID:          "msg_live_vs_persisted_report",
		RunID:       rootID,
		SessionID:   "sess_1",
		WorkspaceID: "ws_1",
		Type:        string(agent.EventReportUpdate),
		Content: `{
			"html":"<p>persisted draft</p>",
			"report_snapshot":{
				"title":"暂存草稿",
				"needsFinalize":true,
				"blocks":[{"id":"blk_1","kind":"markdown","content":"persisted draft"}],
				"charts":[]
			}
		}`,
		CreatedAt: now.Add(time.Second),
	})

	sessionManager = session.NewManager(t.TempDir(), nil, nil)
	liveSess, _, err := sessionManager.GetOrCreate(ctx, "sess_1", "ws_1", "user_1")
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	liveSess.ReportState = &tools.ReportState{}
	liveSess.EditState = &tools.ReportEditState{}
	liveSess.Memory = agent.NewWorkingMemory()
	liveSess.Subgoals = agent.NewSubgoalManager()

	memory, subgoals, reportSnapshot, reportHTML, editState, err := getSessionRuntimeState(ctx, "ws_1", "user_1", "sess_1")
	if err != nil {
		t.Fatalf("get runtime state: %v", err)
	}
	if len(memory) != 0 || len(subgoals) != 0 {
		t.Fatalf("expected live empty runtime state, got memory=%#v subgoals=%#v", memory, subgoals)
	}
	if reportSnapshot != nil || reportHTML != "" {
		t.Fatalf("expected live session to suppress persisted draft snapshot, got snapshot=%#v html=%q", reportSnapshot, reportHTML)
	}
	if editState == nil || editState.Active {
		t.Fatalf("expected inactive live edit state, got %#v", editState)
	}
}

func setupRuntimeStateRepos(t *testing.T, ctx context.Context, store *metadata.Store) {
	t.Helper()

	prevRunRepo := runRepo
	prevMessageRepo := messageRepo
	prevSessionRepo := sessionRepo
	prevSessionManager := sessionManager
	t.Cleanup(func() {
		runRepo = prevRunRepo
		messageRepo = prevMessageRepo
		sessionRepo = prevSessionRepo
		sessionManager = prevSessionManager
	})

	userRepo := sqliterepo.NewUserRepository(store.DB)
	workspaceRepo := sqliterepo.NewWorkspaceRepository(store.DB)
	sessionRepo = sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
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
		ID:          "sess_1",
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
}

func mustCreateRunMessage(t *testing.T, ctx context.Context, value interface{}) {
	t.Helper()
	switch item := value.(type) {
	case *domain.AnalysisRun:
		if err := runRepo.Create(ctx, item); err != nil {
			t.Fatalf("create run: %v", err)
		}
	case *domain.RunMessage:
		if err := messageRepo.Create(ctx, item); err != nil {
			t.Fatalf("create run message: %v", err)
		}
	default:
		t.Fatalf("unsupported test fixture type %T", value)
	}
}

func strPtr(v string) *string {
	return &v
}
