package handler

import (
	"context"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metadata"
	repomemory "github.com/ifnodoraemon/openDataAnalysis/repository/memory"
	sqliterepo "github.com/ifnodoraemon/openDataAnalysis/repository/sqlite"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

func TestSessionRuntimeRejectsMissingDependenciesAndIdentity(t *testing.T) {
	previousSessionRepo := sessionRepo
	previousSessionManager := sessionManager
	t.Cleanup(func() {
		sessionRepo = previousSessionRepo
		sessionManager = previousSessionManager
	})

	sessionRepo = nil
	if _, err := sessionExists(context.Background(), "s_1"); err == nil {
		t.Fatal("expected missing session repository to fail")
	}
	sessionManager = nil
	if _, err := shouldHydrateSessionFromPersistence(context.Background(), "w_1", "u_1", "s_1"); err == nil {
		t.Fatal("expected missing session manager to fail")
	}
	if err := hydrateSessionFromPersistence(context.Background(), nil); err == nil {
		t.Fatal("expected nil session hydration to fail")
	}
}

func TestRecoverStaleSessionRunsMarksNestedRunsFailed(t *testing.T) {
	ctx := context.Background()
	prevRunRepo := runRepo
	prevSessionManager := sessionManager
	t.Cleanup(func() {
		runRepo = prevRunRepo
		sessionManager = prevSessionManager
	})

	runRepo = repomemory.NewRunRepository()
	sessionManager = session.NewManager(t.TempDir(), nil, nil)

	now := time.Now()
	rootID := "r_root"
	childID := "r_child"
	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           rootID,
		SessionID:    "s_1",
		WorkspaceID:  "w_1",
		UserID:       "u_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusWaitingUserInput,
		InputMessage: "root",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create root run: %v", err)
	}
	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           childID,
		SessionID:    "s_1",
		WorkspaceID:  "w_1",
		UserID:       "u_1",
		ParentRunID:  &rootID,
		RunKind:      domain.RunKindDelegate,
		Status:       domain.RunStatusRunning,
		InputMessage: "child",
		CreatedAt:    now.Add(time.Second),
		UpdatedAt:    now.Add(time.Second),
	}); err != nil {
		t.Fatalf("create child run: %v", err)
	}

	if err := recoverStaleSessionRuns(ctx, "s_1"); err != nil {
		t.Fatalf("recover stale session runs: %v", err)
	}

	rootRun, err := runRepo.GetByID(ctx, rootID)
	if err != nil {
		t.Fatalf("get root run: %v", err)
	}
	childRun, err := runRepo.GetByID(ctx, childID)
	if err != nil {
		t.Fatalf("get child run: %v", err)
	}

	if rootRun.Status != domain.RunStatusFailed {
		t.Fatalf("expected root run to be failed, got %s", rootRun.Status)
	}
	if childRun.Status != domain.RunStatusFailed {
		t.Fatalf("expected child run to be failed, got %s", childRun.Status)
	}
}

func TestHydrateSessionFromPersistenceRestoresRuntimeState(t *testing.T) {
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
	t.Cleanup(func() {
		runRepo = prevRunRepo
		messageRepo = prevMessageRepo
	})

	userRepo := sqliterepo.NewUserRepository(store.DB)
	workspaceRepo := sqliterepo.NewWorkspaceRepository(store.DB)
	sessionRepo := sqliterepo.NewSessionRepository(store.DB)
	runRepo = sqliterepo.NewRunRepository(store.DB)
	messageRepo = sqliterepo.NewMessageRepository(store.DB)

	now := time.Now()
	if err := userRepo.Create(ctx, &domain.User{
		ID:           "u_1",
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
		ID:          "w_1",
		Name:        "Workspace",
		Slug:        "workspace",
		OwnerUserID: "u_1",
		Status:      domain.WorkspaceStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := workspaceRepo.AddMember(ctx, &domain.WorkspaceMember{
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Role:        domain.WorkspaceRoleOwner,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("add workspace member: %v", err)
	}
	if err := sessionRepo.Create(ctx, &domain.Session{
		ID:          "s_1",
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Title:       "Hydrate",
		Status:      domain.SessionStatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
		LastSeenAt:  now,
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := runRepo.Create(ctx, &domain.AnalysisRun{
		ID:           "r_1",
		SessionID:    "s_1",
		WorkspaceID:  "w_1",
		UserID:       "u_1",
		RunKind:      domain.RunKindRoot,
		Status:       domain.RunStatusCompleted,
		InputMessage: "analyze",
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "m_1",
		RunID:       "r_1",
		SessionID:   "s_1",
		WorkspaceID: "w_1",
		Type:        string(agent.EventStateMemoryUpdated),
		Content:     `{"entries":{"retained_context":{"statement":"user-provided context","status":"inferred","created_by":"agent","created_at":"2026-08-11T00:00:00Z"}}}`,
		CreatedAt:   now.Add(time.Second),
	}); err != nil {
		t.Fatalf("create memory state event: %v", err)
	}
	if err := messageRepo.Create(ctx, &domain.RunMessage{
		ID:          "m_3",
		RunID:       "r_1",
		SessionID:   "s_1",
		WorkspaceID: "w_1",
		Type:        string(agent.EventStateSubgoalsUpdated),
		Content:     `{"goals":[{"id":"goal_123","description":"Inspect source quality","status":"pending","created_at":"2026-08-11T00:00:00Z","updated_at":"2026-08-11T00:00:00Z"}]}`,
		CreatedAt:   now.Add(3 * time.Second),
	}); err != nil {
		t.Fatalf("create subgoal state event: %v", err)
	}

	sess := &session.Session{
		ID:          "s_1",
		WorkspaceID: "w_1",
		UserID:      "u_1",
		Memory:      agent.NewWorkingMemory(),
		Subgoals:    agent.NewSubgoalManager(),
	}
	if err := hydrateSessionFromPersistence(ctx, sess); err != nil {
		t.Fatalf("hydrate session from persistence: %v", err)
	}

	memory, subgoals := sess.RuntimeState()
	if memory["retained_context"].Statement != "user-provided context" {
		t.Fatalf("unexpected memory snapshot: %#v", memory)
	}
	if len(subgoals) != 1 || subgoals[0].ID != "goal_123" || subgoals[0].Description != "Inspect source quality" {
		t.Fatalf("unexpected subgoals: %#v", subgoals)
	}
}
