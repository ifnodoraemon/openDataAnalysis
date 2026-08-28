package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type captureMessageRepo struct {
	mu            sync.Mutex
	createCtxErr  error
	created       []*domain.RunMessage
	panicOnCreate bool
}

func (r *captureMessageRepo) Create(ctx context.Context, msg *domain.RunMessage) error {
	if r.panicOnCreate {
		panic("repository exploded")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCtxErr = ctx.Err()
	copy := *msg
	r.created = append(r.created, &copy)
	return nil
}

func (r *captureMessageRepo) ListByRun(context.Context, string) ([]domain.RunMessage, error) {
	return nil, nil
}

func (r *captureMessageRepo) ListByRunPath(context.Context, string) ([]domain.RunMessage, error) {
	return nil, nil
}

func (r *captureMessageRepo) ListRecentByRun(context.Context, string, int) ([]domain.RunMessage, error) {
	return nil, nil
}

func (r *captureMessageRepo) snapshot() ([]*domain.RunMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*domain.RunMessage, len(r.created))
	copy(out, r.created)
	return out, r.createCtxErr
}

func TestSaveRuntimeEventDetachesCancelledContext(t *testing.T) {
	previous := messageRepo
	prevQ := eventPersistQueue
	repo := &captureMessageRepo{}
	messageRepo = repo
	t.Cleanup(func() {
		messageRepo = previous
		eventPersistQueue = prevQ
	})

	// 启动专属测试用的异步持久化 worker，并获取 shutdown 函数
	shutdown := startEventPersistWorker()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消的 context

	saveEventToDB(ctx, "ws_1", "sess_1", "run_1", agent.RuntimeEvent{
		Type: agent.EventRunCompleted,
		Data: agent.CompleteData{Summary: "done"},
	})

	// shutdown 关闭队列并等待 worker goroutine 完全退出（含 saveEventToDBSync 返回）
	shutdown()

	created, ctxErr := repo.snapshot()

	if len(created) != 1 {
		t.Fatalf("expected one persisted message, got %d", len(created))
	}
	if ctxErr != nil {
		t.Fatalf("expected detached context (nil Err), got %v", ctxErr)
	}
	if created[0].Content != "done" {
		t.Fatalf("unexpected persisted content: %#v", created[0])
	}
}

func TestRunPersistJobRecoversPanicAndKeepsWorkerAlive(t *testing.T) {
	previous := messageRepo
	prevQ := eventPersistQueue
	repo := &captureMessageRepo{panicOnCreate: true}
	messageRepo = repo
	t.Cleanup(func() {
		messageRepo = previous
		eventPersistQueue = prevQ
	})

	job := persistJob{
		workspaceID: "ws_1",
		sessionID:   "sess_1",
		runID:       "run_1",
		ev:          agent.RuntimeEvent{Type: agent.EventRunCompleted, Data: agent.CompleteData{Summary: "done"}},
		createdAt:   time.Now(),
		result:      make(chan persistResult, 1),
	}

	runPersistJob(job)

	result := <-job.result
	if result.err == nil {
		t.Fatal("expected panic to be converted into a persistence error")
	}
	if !strings.Contains(result.err.Error(), "panicked") {
		t.Fatalf("expected panic error, got %v", result.err)
	}

	repo.panicOnCreate = false
	runPersistJob(job)
	result = <-job.result
	if result.err != nil {
		t.Fatalf("expected worker to keep processing jobs after recovery, got %v", result.err)
	}
	if result.msg == nil || result.msg.Content != "done" {
		t.Fatalf("expected persisted message after recovery, got %#v", result.msg)
	}
}

func TestSaveEventToDBPreservesSubmissionOrderAcrossFallback(t *testing.T) {
	previous := messageRepo
	prevQ := eventPersistQueue
	repo := &captureMessageRepo{}
	messageRepo = repo
	t.Cleanup(func() {
		messageRepo = previous
		eventPersistQueue = prevQ
	})

	shutdown := startEventPersistWorker()
	t.Cleanup(shutdown)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	first := agent.RuntimeEvent{Type: agent.EventAssistantStatus, Data: agent.AssistantStatusData{Content: "first"}}
	second := agent.RuntimeEvent{Type: agent.EventAssistantStatus, Data: agent.AssistantStatusData{Content: "second"}}

	if _, err := saveEventToDB(ctx, "ws_1", "sess_1", "run_1", first); err != nil {
		t.Fatalf("save first event: %v", err)
	}
	secondMsg, err := saveEventToDB(ctx, "ws_1", "sess_1", "run_1", second)
	if err != nil {
		t.Fatalf("save second event: %v", err)
	}
	if secondMsg == nil || secondMsg.Content != "second" {
		t.Fatalf("expected persisted message from fallback path, got %#v", secondMsg)
	}

	shutdown()

	created, _ := repo.snapshot()
	if len(created) != 2 {
		t.Fatalf("expected two persisted messages, got %d", len(created))
	}
	if !created[0].CreatedAt.Before(created[1].CreatedAt) {
		t.Fatalf("expected fallback path to keep submission-order timestamps, got %v then %v", created[0].CreatedAt, created[1].CreatedAt)
	}
}

func TestWriteRepoLookupErrorRecognizesNotFound(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	handled := writeRepoLookupError(recorder, repository.ErrNotFound, "任务不存在")
	if !handled {
		t.Fatal("expected helper to handle repository.ErrNotFound")
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", recorder.Code)
	}
}

func TestWriteRepoLookupErrorRecognizesInternalError(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	handled := writeRepoLookupError(recorder, errors.New("database is locked"), "任务不存在")
	if !handled {
		t.Fatal("expected helper to handle internal error")
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", recorder.Code)
	}
}

func TestSaveEventToDBSyncPersistsReportUpdatePayload(t *testing.T) {
	previous := messageRepo
	repo := &captureMessageRepo{}
	messageRepo = repo
	t.Cleanup(func() {
		messageRepo = previous
	})

	saveEventToDBSync("ws_1", "sess_1", "run_1", agent.RuntimeEvent{
		Type: agent.EventReportUpdate,
		Data: agent.ReportUpdateData{
			HTML:  "<p>draft</p>",
			Title: "Draft",
			ReportSnapshot: &domain.ReportSnapshot{
				Title:         "Draft",
				NeedsFinalize: true,
				Blocks: []domain.ReportSnapshotBlock{
					{ID: "blk_1", Kind: "markdown", Content: "draft body"},
				},
			},
		},
	}, time.Now())

	created, _ := repo.snapshot()
	if len(created) != 1 {
		t.Fatalf("expected one persisted message, got %d", len(created))
	}

	var payload agent.ReportUpdateData
	if err := json.Unmarshal([]byte(created[0].Content), &payload); err != nil {
		t.Fatalf("expected report update payload JSON, got err=%v content=%q", err, created[0].Content)
	}
	if payload.HTML != "<p>draft</p>" || payload.Title != "Draft" {
		t.Fatalf("unexpected persisted report payload: %#v", payload)
	}
	if payload.ReportSnapshot == nil || !payload.ReportSnapshot.NeedsFinalize || len(payload.ReportSnapshot.Blocks) != 1 {
		t.Fatalf("expected structured report snapshot to be persisted, got %#v", payload.ReportSnapshot)
	}
}

func TestResolvePreparedUserMessageDoesNotCallHiddenResolver(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{LLMProvider: "openai", LLMModel: "gpt-4o"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	sess := &session.Session{
		Engine: agent.NewEngine(tools.NewRegistry()),
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "markdown", Title: "Overview", Content: "body"},
			},
			NeedsFinalize: true,
		},
		EditState: &tools.ReportEditState{},
	}

	prepared, extra, err := resolvePreparedUserMessage(context.Background(), sess, agent.UserMessage{Content: "把当前报告整体整理一下"})
	if err != nil {
		t.Fatalf("resolve prepared user message: %v", err)
	}
	if prepared.EditContext != nil {
		t.Fatalf("did not expect hidden resolver to materialize edit context: %#v", prepared.EditContext)
	}
	if len(extra) != 0 {
		t.Fatalf("did not expect hidden runtime blocks, got %#v", extra)
	}
}

func TestResolvePreparedUserMessageCarriesTurnTargetIntoEditContext(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{LLMProvider: "openai", LLMModel: "gpt-4o"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	sess := &session.Session{
		Engine:    agent.NewEngine(tools.NewRegistry()),
		EditState: &tools.ReportEditState{},
	}

	prepared, extra, err := resolvePreparedUserMessage(context.Background(), sess, agent.UserMessage{
		Content: "把这份历史报告整体整理一下",
		TurnContext: &agent.TurnContext{
			ReportTargetRunID: "run_history_1",
			ReportTitle:       "历史报告",
		},
	})
	if err != nil {
		t.Fatalf("resolve prepared user message: %v", err)
	}
	if prepared.EditContext != nil {
		t.Fatalf("did not expect turn target to imply edit context, got %#v", prepared.EditContext)
	}
	if len(extra) != 1 || extra[0].Name != "current_turn_target" {
		t.Fatalf("expected factual current_turn_target runtime block, got %#v", extra)
	}
	if !strings.Contains(extra[0].Content, "ReportTargetRunID: run_history_1") {
		t.Fatalf("expected target run fact, got %q", extra[0].Content)
	}
}

func TestResolvePreparedUserMessageDoesNotReconcileGoals(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{LLMProvider: "openai", LLMModel: "gpt-4o"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	subgoals := agent.NewSubgoalManager()
	rootID, err := subgoals.AddGoalWithBlocking("先补充报告数据", "", false)
	if err != nil {
		t.Fatalf("add root goal: %v", err)
	}

	sess := &session.Session{
		Engine:    agent.NewEngine(tools.NewRegistry()),
		EditState: &tools.ReportEditState{},
		Subgoals:  subgoals,
	}

	prepared, extra, err := resolvePreparedUserMessage(context.Background(), sess, agent.UserMessage{Content: "别补数据了，直接整理当前报告结构"})
	if err != nil {
		t.Fatalf("resolve prepared user message: %v", err)
	}
	if prepared.EditContext != nil {
		t.Fatalf("did not expect goal reconciliation to materialize edit context: %#v", prepared.EditContext)
	}
	if len(extra) != 0 {
		t.Fatalf("did not expect hidden goal runtime block, got %#v", extra)
	}
	goals := subgoals.ListAll()
	if len(goals) != 1 || goals[0].ID != rootID || goals[0].Status != agent.StatusPending {
		t.Fatalf("expected goal tree to stay unchanged, got %#v", goals)
	}
}

func TestResolvePreparedUserMessageDoesNotInferActiveSelectionScope(t *testing.T) {
	prevCfg := config.Cfg
	config.Cfg = &config.Config{LLMProvider: "openai", LLMModel: "gpt-4o"}
	t.Cleanup(func() { config.Cfg = prevCfg })

	sess := &session.Session{
		Engine: agent.NewEngine(tools.NewRegistry()),
		ReportState: &tools.ReportState{
			Blocks: []tools.ReportBlock{
				{ID: "b1", Kind: "markdown", Title: "结论", Content: "原文内容"},
			},
			NeedsFinalize: true,
		},
		EditState: &tools.ReportEditState{
			ScopeKindValue:    "partial_selection",
			TargetRunID:       "run_report_1",
			TargetBlockID:     "b1",
			TargetBlockLabel:  "结论",
			SelectionText:     "这句需要改短",
			SelectionStart:    4,
			SelectionEnd:      10,
			SelectionRangeSet: true,
		},
	}

	prepared, extra, err := resolvePreparedUserMessage(context.Background(), sess, agent.UserMessage{Content: "再改短一点"})
	if err != nil {
		t.Fatalf("resolve prepared user message: %v", err)
	}
	if prepared.EditContext != nil {
		t.Fatalf("did not expect active session edit scope to be copied into user message: %#v", prepared.EditContext)
	}
	if len(extra) != 0 {
		t.Fatalf("did not expect hidden selection runtime block, got %#v", extra)
	}
}
