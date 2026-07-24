package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/service"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

var reportPreviewTools = map[string]struct{}{
	"report_create_chart":     {},
	"report_configure_layout": {},
	"report_manage_blocks":    {},
}

var upgrader = websocket.Upgrader{
	Subprotocols: []string{"mcp-token"},
	CheckOrigin: func(r *http.Request) bool {
		return isWebSocketOriginAllowed(r.Header.Get("Origin"))
	},
}

func isWebSocketOriginAllowed(origin string) bool {
	return config.Cfg != nil && config.Cfg.IsOriginAllowed(origin)
}

const persistenceTimeout = 10 * time.Second

var (
	wsConnsMu   sync.Mutex
	wsConns     = make(map[string]map[*websocket.Conn]context.CancelFunc)
	overflowWg  sync.WaitGroup
	maxOverflow = int32(8)
	overflowCnt int32
)

func registerWS(sessionID string, conn *websocket.Conn) context.CancelFunc {
	wsConnsMu.Lock()
	defer wsConnsMu.Unlock()
	if wsConns[sessionID] == nil {
		wsConns[sessionID] = make(map[*websocket.Conn]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_ = ctx
	wsConns[sessionID][conn] = cancel
	return cancel
}

func unregisterWS(sessionID string, conn *websocket.Conn) {
	wsConnsMu.Lock()
	defer wsConnsMu.Unlock()
	if m, ok := wsConns[sessionID]; ok {
		if cancel, exists := m[conn]; exists {
			cancel()
			delete(m, conn)
		}
		if len(m) == 0 {
			delete(wsConns, sessionID)
		}
	}
}

func failRunStart(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, sess *session.Session, runID string, err error) {
	if err == nil {
		return
	}
	errMsg := err.Error()
	sess.FinishRun(runID, "failed")
	_ = withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusFailed, &errMsg)
	})
	sendSessionEvent(conn, writeMu, sess.ID, runID, agent.WSEvent{
		Type: agent.EventError,
		Data: agent.ErrorData{Message: errMsg},
	})
}

// CloseSessionWebSockets forces immediate disconnection of all active websockets tied to a session.
func CloseSessionWebSockets(sessionID string) {
	wsConnsMu.Lock()
	defer wsConnsMu.Unlock()
	if m, ok := wsConns[sessionID]; ok {
		for conn, cancel := range m {
			cancel()
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session closed"), time.Now().Add(time.Second))
			conn.Close()
		}
		delete(wsConns, sessionID)
	}
}

func isExpectedWebSocketReadClose(err error) bool {
	if err == nil {
		return false
	}
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
		websocket.CloseAbnormalClosure,
	) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") || strings.Contains(msg, "close 1005") || strings.Contains(msg, "close 1006")
}

func isExpectedWebSocketWriteClose(err error) bool {
	if err == nil {
		return false
	}
	if websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived,
		websocket.CloseAbnormalClosure,
	) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "reset by peer") || strings.Contains(msg, "unexpected eof")
}

// persistJob 异步事件持久化任务
type persistJob struct {
	workspaceID string
	sessionID   string
	runID       string
	ev          agent.WSEvent
}

// 全局异步持久化队列（#16）。
// 由 startEventPersistWorker 在服务器启动时初始化。
var eventPersistQueue chan persistJob

// startEventPersistWorker 开启后台持久化 goroutine，不影响实时事件推送。
// 返回的 shutdown 函数会关闭队列并等待 goroutine 完全退出，用于测试和优雅关闭。
func startEventPersistWorker() func() {
	q := make(chan persistJob, 4096)
	eventPersistQueue = q
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
						saveEventToDBSync(job.workspaceID, job.sessionID, job.runID, job.ev)
					default:
						return
					}
				}
			case job := <-q:
				saveEventToDBSync(job.workspaceID, job.sessionID, job.runID, job.ev)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			eventPersistQueue = nil // Stop accepting new jobs
			cancel()                // Signal worker to drain and exit, but DO NOT close(q) to avoid panics on concurrent sends
			wg.Wait()
		})
	}
}

type delegateRunPersistence struct {
	workspaceID string
	sessionID   string
	userID      string
	emit        func(agent.WSEvent)
}

func (p delegateRunPersistence) StartChildRun(ctx context.Context, input agent.ChildRunStart) (string, error) {
	runID := "d_" + uuid.New().String()[:8]
	now := time.Now()
	var parentRunID *string
	if trimmed := strings.TrimSpace(input.ParentRunID); trimmed != "" {
		parentRunID = &trimmed
	}
	var goalID *string
	if trimmed := strings.TrimSpace(input.GoalID); trimmed != "" {
		goalID = &trimmed
	}
	run := &domain.AnalysisRun{
		ID:           runID,
		SessionID:    p.sessionID,
		WorkspaceID:  p.workspaceID,
		UserID:       p.userID,
		ParentRunID:  parentRunID,
		RunKind:      domain.RunKindDelegate,
		DelegateRole: strings.TrimSpace(input.RoleName),
		GoalID:       goalID,
		Status:       domain.RunStatusRunning,
		InputMessage: strings.TrimSpace(input.InputMessage),
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

func (p delegateRunPersistence) AppendChildEvent(ctx context.Context, childRunID string, ev agent.WSEvent) error {
	saveEventToDB(ctx, p.workspaceID, p.sessionID, childRunID, ev)
	return nil
}

func (p delegateRunPersistence) UpdateChildRunStatus(ctx context.Context, childRunID string, status string, errMsg *string) error {
	if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.UpdateStatus(persistCtx, childRunID, domain.RunStatus(status), errMsg)
	}); err != nil {
		return err
	}
	run, err := getRunWithPersistence(ctx, childRunID)
	if err == nil && run.ParentRunID != nil {
		p.emitChildRunsUpdate(ctx, *run.ParentRunID)
	}
	return nil
}

func (p delegateRunPersistence) UpdateChildRunSummary(ctx context.Context, childRunID, summary string) error {
	if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		return runRepo.UpdateSummary(persistCtx, childRunID, strings.TrimSpace(summary))
	}); err != nil {
		return err
	}
	run, err := getRunWithPersistence(ctx, childRunID)
	if err == nil && run.ParentRunID != nil {
		p.emitChildRunsUpdate(ctx, *run.ParentRunID)
	}
	return nil
}

func (p delegateRunPersistence) UpdateChildRunTokens(ctx context.Context, childRunID string, promptTokens, completionTokens int) error {
	// 将 token 消耗嵌入一条日志事件，供评估脚本和分析使用。
	// 不改动 domain 字段，保持持久化层最小互动。
	return p.AppendChildEvent(ctx, childRunID, agent.WSEvent{
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
		return
	}
	p.emit(agent.WSEvent{
		Type:  agent.EventStateChildRunsUpdated,
		RunID: parentRunID,
		Data: agent.ChildRunsUpdatedData{
			ParentRunID: parentRunID,
			ChildRuns:   serializeRuns(detachedContext(ctx), childRuns),
		},
	})
}

func shouldEmitReportPreview(toolName string) bool {
	_, ok := reportPreviewTools[toolName]
	return ok
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

func emitReportPreviewUpdate(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, sessID, workspaceID, runID string, state *tools.ReportState) {
	state.RLock()
	snapshot := buildReportSnapshotLocked(state)
	html := tools.RenderReportHTML("", "", state)
	state.RUnlock()
	updateEv := agent.WSEvent{
		Type: agent.EventReportUpdate,
		Data: agent.ReportUpdateData{
			HTML:           html,
			Title:          strings.TrimSpace(snapshot.Title),
			ReportSnapshot: &snapshot,
		},
	}
	sendSessionEvent(conn, writeMu, sessID, runID, updateEv)
	saveEventToDB(ctx, workspaceID, sessID, runID, updateEv)
}

func finalizeAndPersistReport(ctx context.Context, conn *websocket.Conn, writeMu *sync.Mutex, sess *session.Session, identity auth.Identity, runID string) error {
	sess.ReportState.RLock()
	finalHTML := tools.RenderReportHTML(sess.ReportState.FinalTitle, sess.ReportState.FinalAuthor, sess.ReportState)
	snapshot := buildReportSnapshotLocked(sess.ReportState)
	sess.ReportState.RUnlock()
	var finalReportFileID string
	err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
		reportFile, err := fileService.SaveReportHTML(persistCtx, service.SaveReportInput{
			UserID:      identity.UserID,
			WorkspaceID: sess.WorkspaceID,
			SessionID:   sess.ID,
			RunID:       runID,
			Title:       sess.ReportState.FinalTitle,
			Author:      sess.ReportState.FinalAuthor,
			HTML:        finalHTML,
			Snapshot:    snapshot,
		})
		if err != nil {
			return err
		}
		if err := runRepo.BindReportFile(persistCtx, runID, reportFile.ID); err != nil {
			fileService.DeleteReportFile(persistCtx, reportFile.ID, runID)
			return err
		}
		finalReportFileID = reportFile.ID
		log.Printf("report saved run_id=%s session_id=%s file_id=%s size_bytes=%d", runID, sess.ID, reportFile.ID, reportFile.SizeBytes)
		return nil
	})
	if err != nil {
		errEv := agent.WSEvent{
			Type: agent.EventError,
			Data: agent.ErrorData{Message: "failed to save final report: " + err.Error()},
		}
		sendSessionEvent(conn, writeMu, sess.ID, runID, errEv)
		saveEventToDB(ctx, sess.WorkspaceID, sess.ID, runID, errEv)
		return err
	}
	if title := strings.TrimSpace(sess.ReportState.FinalTitle); title != "" {
		_ = withPersistenceContext(ctx, func(persistCtx context.Context) error {
			return sessionRepo.UpdateTitle(persistCtx, sess.ID, title)
		})
	}
	finalEv := agent.WSEvent{
		Type: agent.EventReportFinal,
		Data: agent.ReportUpdateData{
			HTML:         finalHTML,
			Title:        strings.TrimSpace(sess.ReportState.FinalTitle),
			ReportFileID: finalReportFileID,
			ReportSnapshot: func() *domain.ReportSnapshot {
				s2 := snapshot
				return &s2
			}(),
		},
	}
	sendSessionEvent(conn, writeMu, sess.ID, runID, finalEv)
	saveEventToDB(ctx, sess.WorkspaceID, sess.ID, runID, finalEv)
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
		if err := json.Unmarshal([]byte(report.SnapshotJSON), &snapshot); err != nil {
			return nil, fmt.Errorf("failed to parse report snapshot: %w", err)
		}
		return &snapshot, nil
	}

	_, _, sessionReportSnapshot, _, _ := getSessionRuntimeState(ctx, workspaceID, userID, sessionID)
	if sessionReportSnapshot != nil {
		return sessionReportSnapshot, nil
	}
	return nil, fmt.Errorf("target task has not generated an editable report yet")
}

func buildRunUserContent(sess *session.Session, userMsg agent.UserMessage) (string, error) {
	return strings.TrimSpace(userMsg.Content), nil
}

func turnContextRuntimeBlock(turnCtx *agent.TurnContext) *agent.RuntimeContextBlock {
	if turnCtx == nil || strings.TrimSpace(turnCtx.ReportTargetRunID) == "" {
		return nil
	}
	lines := []string{
		fmt.Sprintf("ReportTargetRunID: %s", strings.TrimSpace(turnCtx.ReportTargetRunID)),
	}
	if title := strings.TrimSpace(turnCtx.ReportTitle); title != "" {
		lines = append(lines, fmt.Sprintf("ReportTitle: %s", title))
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

// WSHandler WebSocket 连接处理
func WSHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex

	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	identity, _ := auth.FromContext(r.Context())
	shouldHydrate := false
	if sessionID != "" {
		var hydrateErr error
		shouldHydrate, hydrateErr = shouldHydrateSessionFromPersistence(r.Context(), identity.WorkspaceID, identity.UserID, sessionID)
		if hydrateErr != nil {
			log.Printf("failed to check session hydration status session_id=%s err=%v", sessionID, hydrateErr)
			return
		}
	}
	sess, _, err := sessionManager.GetOrCreate(r.Context(), sessionID, identity.WorkspaceID, identity.UserID)
	if err != nil {
		log.Printf("failed to create session: %v", err)
		return
	}
	if shouldHydrate {
		if err := recoverStaleSessionRuns(r.Context(), sess.ID); err != nil {
			log.Printf("failed to recover stale runs session_id=%s err=%v", sess.ID, err)
			return
		}
		if err := hydrateSessionFromPersistence(r.Context(), sess); err != nil {
			log.Printf("failed to hydrate session runtime state session_id=%s err=%v", sess.ID, err)
			return
		}
	}
	log.Printf("ws connected session_id=%s workspace_id=%s user_id=%s", sess.ID, sess.WorkspaceID, identity.UserID)
	_ = registerWS(sess.ID, conn)
	defer func() {
		unregisterWS(sess.ID, conn)
		log.Printf("ws disconnected session_id=%s workspace_id=%s user_id=%s", sess.ID, sess.WorkspaceID, identity.UserID)
	}()

	requestCtx := detachedContext(r.Context())
	memory, subgoals, reportSnapshot, reportHTML, editState := getSessionRuntimeState(requestCtx, sess.WorkspaceID, identity.UserID, sess.ID)

	sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
		Type: agent.EventSessionReady,
		Data: agent.SessionReadyData{
			SessionID:      sess.ID,
			Files:          sess.FilesForClient(),
			Subgoals:       subgoals,
			Memory:         memory,
			ReportHTML:     reportHTML,
			ReportSnapshot: reportSnapshot,
			EditState:      editState,
		},
	})

	const (
		pongWait   = 60 * time.Second
		pingPeriod = (pongWait * 9) / 10
	)

	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	pingTicker := time.NewTicker(pingPeriod)
	defer pingTicker.Stop()
	wsCtx, wsCancel := context.WithCancel(r.Context())
	defer wsCancel()

	go func() {
		for {
			select {
			case <-wsCtx.Done():
				return
			case <-pingTicker.C:
				writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second*5))
				writeMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if isExpectedWebSocketReadClose(err) {
				log.Printf("session %s: WebSocket connection closed", sess.ID)
			} else {
				log.Printf("session %s: failed to read message: %v", sess.ID, err)
			}
			break
		}

		var event agent.WSEvent
		if err := json.Unmarshal(msg, &event); err != nil {
			log.Printf("message parse failed: %v", err)
			continue
		}

		switch event.Type {
		case agent.EventUserMessage:
			dataBytes, _ := json.Marshal(event.Data)
			var userMsg agent.UserMessage
			if err := json.Unmarshal(dataBytes, &userMsg); err != nil {
				sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: "failed to parse user message"},
				})
				continue
			}

			// 修复 #15：用 ConsumeWaitingRun 原子地检查并清除等待状态。
			// 若返回非空，说明当前 run 正在等待用户输入，继续恢复执行；
			// 若返回空字符串（并发竞争被其他 goroutine 消费），则作为新任务处理。
			activeRunID := sess.ConsumeWaitingRun()

			if activeRunID != "" {
				runEmitter := newRuntimeEventDispatcher(requestCtx, conn, &writeMu, sess, identity, activeRunID)
				err := sess.Engine.ProvideAskUserResult(userMsg.Content)
				if err != nil {
					sendSessionEvent(conn, &writeMu, sess.ID, activeRunID, agent.WSEvent{
						Type: agent.EventError,
						Data: agent.ErrorData{Message: err.Error()},
					})
					continue
				}

				// 将用户的回答写入数据库历史
				saveEventToDB(requestCtx, sess.WorkspaceID, sess.ID, activeRunID, agent.WSEvent{
					Type: "user",
					Data: userMsg,
				})

				// ConsumeWaitingRun 已将状态改为 running，更新 DB
				_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
					return runRepo.UpdateStatus(persistCtx, activeRunID, domain.RunStatusRunning, nil)
				})

				// 恢复执行 (传入空 userInput，因为问题已作为 tool result)
				resumeCtx := agent.WithTraceMetadata(requestCtx, agent.TraceMetadata{
					WorkspaceID: sess.WorkspaceID,
					SessionID:   sess.ID,
					RunID:       activeRunID,
				})
				resumeCtx = tools.WithExecutionMetadata(resumeCtx, tools.ExecutionMetadata{
					WorkspaceID: sess.WorkspaceID,
					SessionID:   sess.ID,
					RunID:       activeRunID,
				})
				resumeCtx = agent.WithDelegateRunPersistence(resumeCtx, delegateRunPersistence{
					workspaceID: sess.WorkspaceID,
					sessionID:   sess.ID,
					userID:      identity.UserID,
					emit:        runEmitter.Emit,
				})

				ctx, cancel := context.WithCancel(resumeCtx)
				sess.UpdateCancelFunc(activeRunID, cancel)

				if err := dispatchRunExecution(runExecution{
					Context:     ctx,
					Session:     sess,
					RuntimeVars: sess.RuntimeVars,
					Emit:        runEmitter.Emit,
					OnDone:      cancel,
				}); err != nil {
					cancel()
					failRunStart(requestCtx, conn, &writeMu, sess, activeRunID, err)
				}
				continue
			}

			preparedUserMsg, extraRuntime, prepErr := resolvePreparedUserMessage(r.Context(), sess, userMsg)
			if prepErr != nil {
				log.Printf("prepare user message failed session_id=%s err=%v", sess.ID, prepErr)
				preparedUserMsg = userMsg
				extraRuntime = nil
			}

			if err := runBeforeUserRunHooks(r.Context(), sess, preparedUserMsg, prepareUserRunHook(handlerReportSnapshotLoader{})); err != nil {
				sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: err.Error()},
				})
				continue
			}
			runID, ctx, err := sess.StartRun(requestCtx)
			if err != nil {
				sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: err.Error()},
				})
				continue
			}

			now := time.Now()
			rawInput := strings.TrimSpace(userMsg.Content)
			log.Printf("run started run_id=%s session_id=%s workspace_id=%s user_id=%s input_chars=%d", runID, sess.ID, sess.WorkspaceID, identity.UserID, len([]rune(rawInput)))
			if err := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
				return runRepo.Create(persistCtx, &domain.AnalysisRun{
					ID:           runID,
					SessionID:    sess.ID,
					WorkspaceID:  sess.WorkspaceID,
					UserID:       identity.UserID,
					RunKind:      domain.RunKindRoot,
					Status:       domain.RunStatusRunning,
					InputMessage: rawInput,
					StartedAt:    &now,
					CreatedAt:    now,
					UpdatedAt:    now,
				})
			}); err != nil {
				sess.CancelRun(runID)
				sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: "failed to create task record: " + err.Error()},
				})
				continue
			}

			// Persist UserMessage
			saveEventToDB(requestCtx, sess.WorkspaceID, sess.ID, runID, agent.WSEvent{
				Type: "user",
				Data: userMsg,
			})

			_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
				return sessionRepo.UpdateLastRun(persistCtx, sess.ID, runID)
			})
			record, err := func() (*domain.Session, error) {
				var record *domain.Session
				err := withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
					var err error
					record, err = sessionRepo.GetByID(persistCtx, sess.ID)
					return err
				})
				return record, err
			}()
			if err == nil {
				if record.Title == "" || record.Title == "Untitled Analysis" {
					_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
						return sessionRepo.UpdateTitle(persistCtx, sess.ID, deriveSessionTitle(rawInput))
					})
				}
			}

			sendSessionEvent(conn, &writeMu, sess.ID, runID, agent.WSEvent{
				Type: agent.EventRunStarted,
				Data: agent.RunStartedData{RunID: runID},
			})
			editPayload := sess.CurrentEditStateData()
			sendSessionEvent(conn, &writeMu, sess.ID, runID, agent.WSEvent{
				Type: agent.EventStateReportEditUpdated,
				Data: editPayload,
			})
			saveEventToDB(requestCtx, sess.WorkspaceID, sess.ID, runID, agent.WSEvent{
				Type: agent.EventStateReportEditUpdated,
				Data: editPayload,
			})
			runEmitter := newRuntimeEventDispatcher(requestCtx, conn, &writeMu, sess, identity, runID)

			userContent, err := buildRunUserContent(sess, userMsg)
			if err != nil {
				sess.CancelRun(runID)
				_ = withPersistenceContext(requestCtx, func(persistCtx context.Context) error {
					return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusFailed, nil)
				})
				sendSessionEvent(conn, &writeMu, sess.ID, runID, agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: err.Error()},
				})
				continue
			}

			ctx = agent.WithTraceMetadata(ctx, agent.TraceMetadata{
				WorkspaceID: sess.WorkspaceID,
				SessionID:   sess.ID,
				RunID:       runID,
			})
			ctx = tools.WithExecutionMetadata(ctx, tools.ExecutionMetadata{
				WorkspaceID: sess.WorkspaceID,
				SessionID:   sess.ID,
				RunID:       runID,
			})
			ctx = agent.WithDelegateRunPersistence(ctx, delegateRunPersistence{
				workspaceID: sess.WorkspaceID,
				sessionID:   sess.ID,
				userID:      identity.UserID,
				emit:        runEmitter.Emit,
			})

			runtimeVarProvider := mergeRuntimeVarProvider(sess.RuntimeVars, extraRuntime)
			if err := dispatchRunExecution(runExecution{
				Context:     ctx,
				Session:     sess,
				UserInput:   userContent,
				RuntimeVars: runtimeVarProvider,
				Emit:        runEmitter.Emit,
			}); err != nil {
				sess.CancelRun(runID)
				failRunStart(requestCtx, conn, &writeMu, sess, runID, err)
				continue
			}

		case agent.EventStop:
			dataBytes, _ := json.Marshal(event.Data)
			var req agent.StopRunRequest
			_ = json.Unmarshal(dataBytes, &req)
			if !sess.CancelRun(req.RunID) {
				sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: "no running task to stop"},
				})
			}

		case agent.EventReset:
			dataBytes, _ := json.Marshal(event.Data)
			req := agent.ResetSessionRequest{KeepFiles: true}
			_ = json.Unmarshal(dataBytes, &req)
			if err := sess.Reset(req.KeepFiles); err != nil {
				sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
					Type: agent.EventError,
					Data: agent.ErrorData{Message: err.Error()},
				})
				continue
			}
			sendSessionEvent(conn, &writeMu, sess.ID, "", agent.WSEvent{
				Type: agent.EventSessionReset,
				Data: agent.SessionResetData{
					KeepFiles: req.KeepFiles,
					Files:     sess.FilesForClient(),
				},
			})

		default:
			log.Printf("unknown event type: %s", event.Type)
		}
	}
}

func clipLogText(input string, max int) string {
	input = strings.TrimSpace(input)
	if max <= 0 || len([]rune(input)) <= max {
		return input
	}
	return string([]rune(input)[:max]) + "...(truncated)"
}

func deriveSessionTitle(input string) string {
	title := strings.TrimSpace(input)
	if title == "" {
		return "Untitled Analysis"
	}
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) > 28 {
		return string(runes[:28]) + "..."
	}
	return title
}

// saveEventToDB 将事件投递到异步持乇化队列。
// 非阻塞：若队列满则丢弃并打印 warning（事件持乇化允许有损，不影响实时性）。
// 注意：读取全局 eventPersistQueue 到局部变量后再操作，
// 防止并发 shutdown 时出现向已关闭 channel 发送 panic。
func saveEventToDB(ctx context.Context, workspaceID, sessionID, runID string, ev agent.WSEvent) {
	if messageRepo == nil {
		return
	}
	// 用局部变量快照全局队列引用，避免在 nil 检查和 send 之间被置 nil
	q := eventPersistQueue
	if q == nil {
		return
	}
	select {
	case q <- persistJob{workspaceID: workspaceID, sessionID: sessionID, runID: runID, ev: ev}:
	default:
		if v := atomic.AddInt32(&overflowCnt, 1); v <= int32(maxOverflow) {
			overflowWg.Add(1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[PANIC RECOVER] saveEventToDB: %v", r)
					}
					atomic.AddInt32(&overflowCnt, -1)
					overflowWg.Done()
				}()
				saveEventToDBSync(workspaceID, sessionID, runID, ev)
			}()
		} else {
			atomic.AddInt32(&overflowCnt, -1)
			log.Printf("[warn] eventPersistQueue full and overflow limit reached, dropping event type=%s run_id=%s", ev.Type, runID)
		}
	}
}

// saveEventToDBSync 是实际的同步写库逻辑，由后台 goroutine 调用。
func saveEventToDBSync(workspaceID, sessionID, runID string, ev agent.WSEvent) {
	if messageRepo == nil {
		return
	}

	msg := &domain.RunMessage{
		ID:          uuid.New().String(),
		RunID:       runID,
		SessionID:   sessionID,
		WorkspaceID: workspaceID,
		Type:        string(ev.Type),
		CreatedAt:   time.Now(),
	}

	switch data := ev.Data.(type) {
	case agent.UserMessage:
		msg.Content = data.Content
	case agent.AssistantStatusData:
		msg.Content = data.Content
	case agent.AskUserData:
		argsBytes, _ := json.Marshal(data)
		msg.Content = string(argsBytes)
	case agent.MemoryUpdatedData:
		argsBytes, _ := json.Marshal(data)
		msg.Content = string(argsBytes)
	case agent.ToolCallData:
		msg.Name = data.Name
		if data.ID != "" {
			id := data.ID
			msg.ToolCallID = &id
		}
		argsBytes, _ := json.Marshal(data.Arguments)
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
		if err == nil {
			msg.Content = string(contentBytes)
		}
	default:
		// Attempt to marshal any other unhandled types as JSON
		contentBytes, err := json.Marshal(ev.Data)
		if err == nil {
			msg.Content = string(contentBytes)
		}
	}

	ctx2, cancel := context.WithTimeout(context.Background(), persistenceTimeout)
	defer cancel()
	if err := messageRepo.Create(ctx2, msg); err != nil {
		log.Printf("Failed to save event to db run_id=%s type=%s err=%v", runID, ev.Type, err)
	}
}

func sendSessionEvent(conn *websocket.Conn, mu *sync.Mutex, sessionID, runID string, event agent.WSEvent) {
	event.SessionID = sessionID
	if runID != "" && event.RunID == "" {
		event.RunID = runID
	}
	if conn != nil && mu != nil {
		sendEvent(conn, mu, event)
	}
	GlobalSSEBroker.Broadcast(sessionID, event)
}

func sendEvent(conn *websocket.Conn, mu *sync.Mutex, event agent.WSEvent) {
	mu.Lock()
	defer mu.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		log.Printf("serialization failed: %v", err)
		return
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		if isExpectedWebSocketWriteClose(err) {
			log.Printf("send skipped on closed websocket: %v", err)
			return
		}
		log.Printf("send failed: %v", err)
	}
}

func buildReportSnapshot(state *tools.ReportState) domain.ReportSnapshot {
	if state != nil {
		state.RLock()
		defer state.RUnlock()
	}
	return buildReportSnapshotLocked(state)
}

func buildReportSnapshotLocked(state *tools.ReportState) domain.ReportSnapshot {
	snapshot := domain.ReportSnapshot{
		Version:       "v3",
		GeneratedAt:   time.Now(),
		NeedsFinalize: state != nil && state.NeedsFinalize,
	}
	if state == nil {
		return snapshot
	}

	snapshot.Title = strings.TrimSpace(state.FinalTitle)
	snapshot.Author = strings.TrimSpace(state.FinalAuthor)
	snapshot.Layout = domain.ReportSnapshotLayout{
		CustomCSS: state.Layout.CustomCSS,
		BodyClass: state.Layout.BodyClass,
	}
	if snapshot.Title == "" {
		snapshot.Title = tools.ResolveReportTitleFromState(state)
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
			if err == nil {
				snapshotBlock.Sources = sourcesJSON
			}
		}
		snapshot.Blocks = append(snapshot.Blocks, snapshotBlock)
	}
	snapshot.Charts = make([]domain.ReportSnapshotChart, 0, len(state.Charts))
	for _, chart := range state.Charts {
		snapshot.Charts = append(snapshot.Charts, domain.ReportSnapshotChart{
			ID:     chart.ID,
			Option: chart.Option,
			Width:  chart.Width,
			Height: chart.Height,
		})
	}
	return snapshot
}
