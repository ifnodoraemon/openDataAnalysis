package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/repository"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

const persistenceTimeout = 10 * time.Second

func failRunStart(ctx context.Context, sess *session.Session, runID string, err error) {
	if err == nil {
		return
	}
	log.Printf("run start failed run_id=%s session_id=%s err=%v", runID, sess.ID, err)
	errMsg := "启动任务失败"
	sess.FinishRun(runID, "failed")
	if persistErr := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusFailed, &errMsg)
	}); persistErr != nil {
		log.Printf("persist terminal run state failed run_id=%s err=%v", runID, persistErr)
		errMsg = "启动任务失败，且无法保存失败状态"
	}
	sendSessionEvent(sess.ID, runID, agent.RuntimeEvent{
		Type: agent.EventError,
		Data: agent.ErrorData{Message: errMsg},
	}, "")
}

// persistJob 表示等待持久化的运行事件。
type persistJob struct {
	workspaceID string
	sessionID   string
	runID       string
	ev          agent.RuntimeEvent
	createdAt   time.Time
	result      chan persistResult
}

type persistResult struct {
	msg *domain.RunMessage
	err error
}

var (
	eventPersistMu    sync.RWMutex
	eventPersistQueue chan persistJob
)

func runPersistJob(job persistJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVER] event persist job: %v", r)
			job.result <- persistResult{err: fmt.Errorf("event persistence panicked: %v", r)}
		}
	}()
	msg, err := saveEventToDBSync(job.workspaceID, job.sessionID, job.runID, job.ev, job.createdAt)
	job.result <- persistResult{msg: msg, err: err}
}

// startEventPersistWorker serializes event writes. Each submitter waits for the
// repository result, so delivery never implies persistence that has not happened.
func startEventPersistWorker() func() {
	q := make(chan persistJob, 4096)
	eventPersistMu.Lock()
	eventPersistQueue = q
	eventPersistMu.Unlock()
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVER] startEventPersistWorker: %v", r)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				// Drain the remaining jobs before exiting
				for {
					select {
					case job := <-q:
						runPersistJob(job)
					default:
						return
					}
				}
			case job := <-q:
				runPersistJob(job)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			eventPersistMu.Lock()
			eventPersistQueue = nil
			cancel()
			eventPersistMu.Unlock()
			wg.Wait()
		})
	}
}

type delegateRunPersistence struct {
	workspaceID string
	sessionID   string
	userID      string
	emit        func(agent.RuntimeEvent)
}

func (p delegateRunPersistence) StartChildRun(ctx context.Context, input agent.ChildRunStart) (string, error) {
	for field, value := range map[string]string{
		"parent run id": input.ParentRunID,
		"role name":     input.RoleName,
		"input message": input.InputMessage,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return "", fmt.Errorf("%s must be a non-empty exact value", field)
		}
	}
	if strings.TrimSpace(input.GoalID) != input.GoalID {
		return "", fmt.Errorf("goal id must be an exact value")
	}
	runID := "d_" + uuid.New().String()[:8]
	now := time.Now()
	parentRunID := &input.ParentRunID
	var goalID *string
	if input.GoalID != "" {
		goalID = &input.GoalID
	}
	run := &domain.AnalysisRun{
		ID:           runID,
		SessionID:    p.sessionID,
		WorkspaceID:  p.workspaceID,
		UserID:       p.userID,
		ParentRunID:  parentRunID,
		RunKind:      domain.RunKindDelegate,
		DelegateRole: input.RoleName,
		GoalID:       goalID,
		Status:       domain.RunStatusRunning,
		InputMessage: input.InputMessage,
		StartedAt:    &now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.Create(persistCtx, run)
	}); err != nil {
		return "", err
	}
	p.emitChildRunsUpdate(ctx, input.ParentRunID)
	return runID, nil
}

func (p delegateRunPersistence) AppendChildEvent(ctx context.Context, childRunID string, ev agent.RuntimeEvent) error {
	_, err := saveEventToDB(ctx, p.workspaceID, p.sessionID, childRunID, ev)
	return err
}

func (p delegateRunPersistence) UpdateChildRunStatus(ctx context.Context, childRunID string, status string, errMsg *string) error {
	if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.UpdateStatus(persistCtx, childRunID, domain.RunStatus(status), errMsg)
	}); err != nil {
		return err
	}
	run, err := getRunWithPersistence(ctx, childRunID)
	if err != nil {
		return err
	}
	if run.ParentRunID != nil {
		p.emitChildRunsUpdate(ctx, *run.ParentRunID)
	}
	return nil
}

func (p delegateRunPersistence) UpdateChildRunSummary(ctx context.Context, childRunID, summary string) error {
	if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.UpdateSummary(persistCtx, childRunID, summary)
	}); err != nil {
		return err
	}
	run, err := getRunWithPersistence(ctx, childRunID)
	if err != nil {
		return err
	}
	if run.ParentRunID != nil {
		p.emitChildRunsUpdate(ctx, *run.ParentRunID)
	}
	return nil
}

func (p delegateRunPersistence) UpdateChildRunTokens(ctx context.Context, childRunID string, promptTokens, completionTokens int) error {
	// 将 token 消耗嵌入一条日志事件，供评估脚本和分析使用。
	// 不改动 domain 字段，保持持久化层最小互动。
	return p.AppendChildEvent(ctx, childRunID, agent.RuntimeEvent{
		Type: "child_run_tokens",
		Data: map[string]interface{}{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	})
}

func (p delegateRunPersistence) emitChildRunsUpdate(ctx context.Context, parentRunID string) {
	if p.emit == nil || strings.TrimSpace(parentRunID) == "" {
		return
	}
	var childRuns []domain.AnalysisRun
	err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		runs, err := runRepo.ListByParent(persistCtx, parentRunID)
		if err != nil {
			return err
		}
		childRuns = runs
		return nil
	})
	if err != nil {
		log.Printf("read child run state failed parent_run_id=%s err=%v", parentRunID, err)
		p.emit(agent.RuntimeEvent{Type: agent.EventError, RunID: parentRunID, Data: agent.ErrorData{Message: "读取子任务状态失败"}})
		return
	}
	serializedRuns, err := serializeRuns(detachedContext(ctx), childRuns)
	if err != nil {
		log.Printf("serialize child run state failed parent_run_id=%s err=%v", parentRunID, err)
		p.emit(agent.RuntimeEvent{Type: agent.EventError, RunID: parentRunID, Data: agent.ErrorData{Message: "序列化子任务状态失败"}})
		return
	}
	p.emit(agent.RuntimeEvent{
		Type:  agent.EventStateChildRunsUpdated,
		RunID: parentRunID,
		Data: agent.ChildRunsUpdatedData{
			ParentRunID: parentRunID,
			ChildRuns:   serializedRuns,
		},
	})
}

func detachedContext(parent context.Context) context.Context {
	if parent == nil {
		return context.Background()
	}
	return context.WithoutCancel(parent)
}

func withPersistenceContext(parent context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(detachedContext(parent), persistenceTimeout)
	defer cancel()
	return fn(ctx)
}

func getRunWithPersistence(ctx context.Context, runID string) (*domain.AnalysisRun, error) {
	var run *domain.AnalysisRun
	err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		var err error
		run, err = runRepo.GetByID(persistCtx, runID)
		return err
	})
	return run, err
}

func emitReportPreviewUpdate(ctx context.Context, sessID, workspaceID, runID string, state *tools.ReportState) error {
	state.RLock()
	snapshot, err := buildReportSnapshotLocked(state)
	if err != nil {
		state.RUnlock()
		return err
	}
	html := tools.RenderReportHTML("", "", state)
	state.RUnlock()
	updateEv := agent.RuntimeEvent{
		Type: agent.EventReportUpdate,
		Data: agent.ReportUpdateData{
			HTML:           html,
			Title:          snapshot.Title,
			ReportSnapshot: &snapshot,
		},
	}
	msg, err := saveEventToDB(ctx, workspaceID, sessID, runID, updateEv)
	if err != nil {
		return fmt.Errorf("persist report preview event: %w", err)
	}
	sendSessionEvent(sessID, runID, updateEv, persistedMessageID(msg))
	return nil
}

func finalizeAndPersistReport(ctx context.Context, sess *session.Session, identity auth.Identity, runID string) error {
	sess.ReportState.RLock()
	finalHTML := tools.RenderReportHTML(sess.ReportState.FinalTitle, sess.ReportState.FinalAuthor, sess.ReportState)
	snapshot, snapshotErr := buildReportSnapshotLocked(sess.ReportState)
	sess.ReportState.RUnlock()
	if snapshotErr != nil {
		return snapshotErr
	}
	var finalReportFileID string
	err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		reportFile, err := fileService.SaveReportHTML(persistCtx, service.SaveReportInput{
			UserID:      identity.UserID,
			WorkspaceID: sess.WorkspaceID,
			SessionID:   sess.ID,
			RunID:       runID,
			HTML:        finalHTML,
			Snapshot:    snapshot,
		})
		if err != nil {
			return err
		}
		if err := runRepo.BindReportFile(persistCtx, runID, reportFile.ID); err != nil {
			return errors.Join(err, fileService.DeleteReportFile(persistCtx, reportFile.ID, runID))
		}
		finalReportFileID = reportFile.ID
		log.Printf("report saved run_id=%s session_id=%s file_id=%s size_bytes=%d", runID, sess.ID, reportFile.ID, reportFile.SizeBytes)
		return nil
	})
	if err != nil {
		log.Printf("save final report failed run_id=%s err=%v", runID, err)
		errEv := agent.RuntimeEvent{
			Type: agent.EventError,
			Data: agent.ErrorData{Message: "保存最终报告失败"},
		}
		msg, persistErr := saveEventToDB(ctx, sess.WorkspaceID, sess.ID, runID, errEv)
		sendSessionEvent(sess.ID, runID, errEv, persistedMessageID(msg))
		return errors.Join(err, persistErr)
	}
	finalEv := agent.RuntimeEvent{
		Type: agent.EventReportFinal,
		Data: agent.ReportUpdateData{
			HTML:         finalHTML,
			Title:        sess.ReportState.FinalTitle,
			ReportFileID: finalReportFileID,
			ReportSnapshot: func() *domain.ReportSnapshot {
				s2 := snapshot
				return &s2
			}(),
		},
	}
	msg, err := saveEventToDB(ctx, sess.WorkspaceID, sess.ID, runID, finalEv)
	if err != nil {
		return fmt.Errorf("persist final report event: %w", err)
	}
	sendSessionEvent(sess.ID, runID, finalEv, persistedMessageID(msg))
	return nil
}

type handlerReportSnapshotLoader struct{}

func (handlerReportSnapshotLoader) LoadReportSnapshot(ctx context.Context, sessionID, workspaceID, userID, runID string) (*domain.ReportSnapshot, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, nil
	}
	run, err := runRepo.GetByID(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to read target task: %w", err)
	}
	if run.SessionID != sessionID || run.WorkspaceID != workspaceID || run.UserID != userID {
		return nil, fmt.Errorf("target task does not belong to current session")
	}
	report, err := reportRepo.GetByRunID(ctx, runID)
	if err == nil {
		var snapshot domain.ReportSnapshot
		if err := jsoncontract.Decode([]byte(report.SnapshotJSON), &snapshot); err != nil {
			return nil, fmt.Errorf("failed to parse report snapshot: %w", err)
		}
		return &snapshot, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("failed to read target report: %w", err)
	}

	_, _, sessionReportSnapshot, _, _, err := deriveRuntimeStateFromRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to read session runtime state: %w", err)
	}
	if sessionReportSnapshot != nil {
		return sessionReportSnapshot, nil
	}
	return nil, fmt.Errorf("target task has not generated an editable report yet")
}

func buildRunUserContent(sess *session.Session, userMsg agent.UserMessage) (string, error) {
	if err := userMsg.Validate(); err != nil {
		return "", err
	}
	return userMsg.Content, nil
}

func turnContextRuntimeBlock(turnCtx *agent.TurnContext) *agent.RuntimeContextBlock {
	if turnCtx == nil || turnCtx.ReportTargetRunID == "" {
		return nil
	}
	lines := []string{
		fmt.Sprintf("ReportTargetRunID: %s", turnCtx.ReportTargetRunID),
	}
	if turnCtx.ReportTitle != "" {
		lines = append(lines, fmt.Sprintf("ReportTitle: %s", turnCtx.ReportTitle))
	}
	return &agent.RuntimeContextBlock{
		Name:    "current_turn_target",
		Role:    "user",
		Content: strings.Join(lines, "\n"),
	}
}

func resolvePreparedUserMessage(_ context.Context, sess *session.Session, userMsg agent.UserMessage) (agent.UserMessage, []agent.RuntimeContextBlock, error) {
	if sess == nil || userMsg.EditContext != nil {
		return userMsg, nil, nil
	}

	var extra []agent.RuntimeContextBlock
	if targetBlock := turnContextRuntimeBlock(userMsg.TurnContext); targetBlock != nil {
		extra = append(extra, *targetBlock)
	}
	return userMsg, extra, nil
}

func mergeRuntimeVarProvider(base func() []agent.RuntimeContextBlock, extra []agent.RuntimeContextBlock) func() []agent.RuntimeContextBlock {
	if len(extra) == 0 {
		return base
	}
	return func() []agent.RuntimeContextBlock {
		var merged []agent.RuntimeContextBlock
		if base != nil {
			merged = append(merged, base()...)
		}
		merged = append(merged, extra...)
		return merged
	}
}

func clipLogText(input string, max int) string {
	input = strings.TrimSpace(input)
	if max <= 0 || len([]rune(input)) <= max {
		return input
	}
	return string([]rune(input)[:max]) + "...(truncated)"
}

// saveEventToDB returns only after the repository has accepted or rejected the
// event. The queue provides ordering, not best-effort delivery. The event
// timestamp is stamped at submission time so the synchronous fallback on
// ctx.Done keeps the same persisted ordering as queued events.
func saveEventToDB(ctx context.Context, workspaceID, sessionID, runID string, ev agent.RuntimeEvent) (*domain.RunMessage, error) {
	if messageRepo == nil {
		return nil, errors.New("message repository is not initialized")
	}
	eventPersistMu.RLock()
	q := eventPersistQueue
	if q == nil {
		eventPersistMu.RUnlock()
		return saveEventToDBSync(workspaceID, sessionID, runID, ev, time.Now())
	}
	job := persistJob{
		workspaceID: workspaceID,
		sessionID:   sessionID,
		runID:       runID,
		ev:          ev,
		createdAt:   time.Now(),
		result:      make(chan persistResult, 1),
	}
	select {
	case q <- job:
		eventPersistMu.RUnlock()
		result := <-job.result
		return result.msg, result.err
	case <-ctx.Done():
		eventPersistMu.RUnlock()
		return saveEventToDBSync(workspaceID, sessionID, runID, ev, job.createdAt)
	}
}

// saveEventToDBSync 是实际的同步写库逻辑，由后台 goroutine 调用。
func saveEventToDBSync(workspaceID, sessionID, runID string, ev agent.RuntimeEvent, createdAt time.Time) (*domain.RunMessage, error) {
	if messageRepo == nil {
		return nil, errors.New("message repository is not initialized")
	}

	msg := &domain.RunMessage{
		ID:          uuid.New().String(),
		RunID:       runID,
		SessionID:   sessionID,
		WorkspaceID: workspaceID,
		Type:        string(ev.Type),
		CreatedAt:   createdAt,
	}

	switch data := ev.Data.(type) {
	case agent.UserMessage:
		msg.Content = data.Content
	case agent.AssistantStatusData:
		msg.Content = data.Content
	case agent.AskUserData:
		argsBytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("encode ask-user event: %w", err)
		}
		msg.Content = string(argsBytes)
	case agent.MemoryUpdatedData:
		argsBytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("encode memory event: %w", err)
		}
		msg.Content = string(argsBytes)
	case agent.ToolCallData:
		msg.Name = data.Name
		if data.ID != "" {
			id := data.ID
			msg.ToolCallID = &id
		}
		argsBytes, err := json.Marshal(data.Arguments)
		if err != nil {
			return nil, fmt.Errorf("encode tool-call arguments: %w", err)
		}
		msg.Content = string(argsBytes)
	case agent.ToolResultData:
		msg.Name = data.Name
		if data.ID != "" {
			id := data.ID
			msg.ToolCallID = &id
		}
		msg.Content = string(data.Result)
		msg.Duration = &data.Duration
		success := data.Success
		msg.Success = &success
	case agent.ErrorData:
		msg.Content = data.Message
	case agent.CompleteData:
		msg.Content = data.Summary
	case agent.ReportUpdateData:
		contentBytes, err := json.Marshal(data)
		if err != nil {
			return nil, fmt.Errorf("encode report update event: %w", err)
		}
		msg.Content = string(contentBytes)
	default:
		contentBytes, err := json.Marshal(ev.Data)
		if err != nil {
			return nil, fmt.Errorf("encode event payload: %w", err)
		}
		msg.Content = string(contentBytes)
	}

	ctx2, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	if err := messageRepo.Create(ctx2, msg); err != nil {
		return nil, fmt.Errorf("save event to database: %w", err)
	}
	return msg, nil
}

func persistedMessageID(msg *domain.RunMessage) string {
	if msg == nil {
		return ""
	}
	return msg.ID
}

func sendSessionEvent(sessionID, runID string, event agent.RuntimeEvent, messageID string) {
	event.SessionID = sessionID
	if runID != "" && event.RunID == "" {
		event.RunID = runID
	}
	GlobalSSEBroker.Broadcast(sessionID, event, messageID)
}

func buildReportSnapshot(state *tools.ReportState) (domain.ReportSnapshot, error) {
	if state != nil {
		state.RLock()
		defer state.RUnlock()
	}
	return buildReportSnapshotLocked(state)
}

func buildReportSnapshotLocked(state *tools.ReportState) (domain.ReportSnapshot, error) {
	snapshot := domain.ReportSnapshot{
		Version:       "v3",
		GeneratedAt:   time.Now(),
		NeedsFinalize: state != nil && state.NeedsFinalize,
	}
	if state == nil {
		return snapshot, nil
	}

	snapshot.Title = state.FinalTitle
	snapshot.Author = state.FinalAuthor
	snapshot.Layout = domain.ReportSnapshotLayout{
		CustomCSS: state.Layout.CustomCSS,
		BodyClass: state.Layout.BodyClass,
	}
	snapshot.Blocks = make([]domain.ReportSnapshotBlock, 0, len(state.Blocks))
	for _, block := range state.Blocks {
		snapshotBlock := domain.ReportSnapshotBlock{
			ID:      block.ID,
			Kind:    block.Kind,
			Title:   block.Title,
			Content: block.Content,
			ChartID: block.ChartID,
		}
		if len(block.Sources) > 0 {
			sourcesJSON, err := json.Marshal(block.Sources)
			if err != nil {
				return domain.ReportSnapshot{}, fmt.Errorf("encode report block %q sources: %w", block.ID, err)
			}
			snapshotBlock.Sources = sourcesJSON
		}
		snapshot.Blocks = append(snapshot.Blocks, snapshotBlock)
	}
	snapshot.Charts = make([]domain.ReportSnapshotChart, 0, len(state.Charts))
	for _, chart := range state.Charts {
		snapshotChart := domain.ReportSnapshotChart{
			ID:     chart.ID,
			Option: chart.Option,
			Width:  chart.Width,
			Height: chart.Height,
		}
		if len(chart.Sources) > 0 {
			var err error
			snapshotChart.Sources, err = json.Marshal(chart.Sources)
			if err != nil {
				return domain.ReportSnapshot{}, fmt.Errorf("encode report chart %q sources: %w", chart.ID, err)
			}
		}
		snapshot.Charts = append(snapshot.Charts, snapshotChart)
	}
	if len(state.Results) > 0 {
		var err error
		snapshot.Results, err = json.Marshal(state.Results)
		if err != nil {
			return domain.ReportSnapshot{}, fmt.Errorf("encode report results: %w", err)
		}
	}
	if len(state.Artifacts) > 0 {
		var err error
		snapshot.Artifacts, err = json.Marshal(state.Artifacts)
		if err != nil {
			return domain.ReportSnapshot{}, fmt.Errorf("encode report artifacts: %w", err)
		}
	}
	return snapshot, nil
}
