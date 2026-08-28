package handler

import (
	"context"
	"log"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
	"github.com/ifnodoraemon/openDataAnalysis/session"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

type beforeUserRunHook func(context.Context, *session.Session, agent.UserMessage) error

func runBeforeUserRunHooks(ctx context.Context, sess *session.Session, userMsg agent.UserMessage, hooks ...beforeUserRunHook) error {
	for _, hook := range hooks {
		if hook == nil {
			continue
		}
		if err := hook(ctx, sess, userMsg); err != nil {
			return err
		}
	}
	return nil
}

func prepareUserRunHook(loader session.ReportSnapshotLoader) beforeUserRunHook {
	return func(ctx context.Context, sess *session.Session, userMsg agent.UserMessage) error {
		return sess.PrepareUserRun(ctx, userMsg, loader)
	}
}

type runtimeEventHook func(runtimeEventScope, agent.RuntimeEvent)

type runtimeEventScope struct {
	session           *session.Session
	runID             string
	emitReportPreview func()
	emitEditState     func(agent.EditStateUpdatedData)
	finalizeReport    func() error
	setRunStatus      func(domain.RunStatus, *string)
	setRunSummary     func(string)
}

type runtimeEventDispatcher struct {
	deliver           func(agent.RuntimeEvent) error
	deliverToRun      func(string, agent.RuntimeEvent) error
	onDeliveryFailure func(string, error)
	emitChildPreview  func(string)
	scope             runtimeEventScope
	hooks             []runtimeEventHook
}

func newRuntimeEventDispatcher(ctx context.Context, sess *session.Session, identity auth.Identity, runID string) runtimeEventDispatcher {
	deliverToRun := func(targetRunID string, ev agent.RuntimeEvent) error {
		msg, err := saveEventToDB(ctx, sess.WorkspaceID, sess.ID, targetRunID, ev)
		if err != nil {
			return err
		}
		sendSessionEvent(sess.ID, targetRunID, ev, persistedMessageID(msg))
		return nil
	}
	deliver := func(ev agent.RuntimeEvent) error { return deliverToRun(runID, ev) }
	handlePersistenceFailure := func(targetRunID string, err error) {
		log.Printf("runtime event persistence failed run_id=%s err=%v", targetRunID, err)
		sendSessionEvent(sess.ID, targetRunID, agent.RuntimeEvent{
			Type: agent.EventError,
			Data: agent.ErrorData{Message: "运行时状态持久化失败"},
		}, "")
		if targetRunID != runID {
			return
		}
		sess.CancelRun(runID)
		msg := "运行时状态持久化失败"
		if statusErr := withPersistenceContext(ctx, func(persistCtx context.Context) error {
			return runRepo.UpdateStatus(persistCtx, runID, domain.RunStatusFailed, &msg)
		}); statusErr != nil {
			log.Printf("failed to persist run failure run_id=%s err=%v", runID, statusErr)
		}
	}

	scope := runtimeEventScope{
		session: sess,
		runID:   runID,
		emitReportPreview: func() {
			if err := emitReportPreviewUpdate(ctx, sess.ID, sess.WorkspaceID, runID, sess.ReportState); err != nil {
				handlePersistenceFailure(runID, err)
			}
		},
		emitEditState: func(data agent.EditStateUpdatedData) {
			deliverToRun(runID, agent.RuntimeEvent{Type: agent.EventStateReportEditUpdated, Data: data})
		},
		finalizeReport: func() error {
			return finalizeAndPersistReport(ctx, sess, identity, runID)
		},
		setRunStatus: func(status domain.RunStatus, errMsg *string) {
			if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
				return runRepo.UpdateStatus(persistCtx, runID, status, errMsg)
			}); err != nil {
				log.Printf("failed to persist run status run_id=%s status=%s err=%v", runID, status, err)
			}
		},
		setRunSummary: func(summary string) {
			if err := withPersistenceContext(ctx, func(persistCtx context.Context) error {
				return runRepo.UpdateSummary(persistCtx, runID, summary)
			}); err != nil {
				log.Printf("failed to persist run summary run_id=%s err=%v", runID, err)
			}
		},
	}

	return runtimeEventDispatcher{
		deliver:           deliver,
		deliverToRun:      deliverToRun,
		onDeliveryFailure: handlePersistenceFailure,
		emitChildPreview: func(targetRunID string) {
			if err := emitReportPreviewUpdate(ctx, sess.ID, sess.WorkspaceID, targetRunID, sess.ReportState); err != nil {
				handlePersistenceFailure(targetRunID, err)
			}
		},
		scope: scope,
		hooks: []runtimeEventHook{
			reportLifecycleHook,
			runLifecycleHook,
			runLoggingHook,
		},
	}
}

func (d runtimeEventDispatcher) Emit(ev agent.RuntimeEvent) {
	if runID := strings.TrimSpace(ev.RunID); runID != "" && runID != strings.TrimSpace(d.scope.runID) {
		if d.deliverToRun != nil {
			if err := d.deliverToRun(runID, ev); err != nil {
				if d.onDeliveryFailure != nil {
					d.onDeliveryFailure(runID, err)
				} else {
					log.Printf("failed to persist delegated runtime event run_id=%s type=%s err=%v", runID, ev.Type, err)
				}
				return
			}
		}
		childScope := d.scope
		childScope.runID = runID
		childScope.finalizeReport = nil
		childScope.setRunStatus = nil
		childScope.setRunSummary = nil
		if d.emitChildPreview != nil {
			childScope.emitReportPreview = func() {
				d.emitChildPreview(runID)
			}
		} else {
			childScope.emitReportPreview = nil
		}
		reportLifecycleHook(childScope, ev)
		runLoggingHook(childScope, ev)
		return
	}
	if err := d.deliver(ev); err != nil {
		if d.onDeliveryFailure != nil {
			d.onDeliveryFailure(d.scope.runID, err)
		} else {
			log.Printf("failed to persist runtime event run_id=%s type=%s err=%v", d.scope.runID, ev.Type, err)
		}
		return
	}
	switch ev.Type {
	case agent.EventRunCompleted:
		metrics.AgentRunsTotal.WithLabelValues("completed").Inc()
	case agent.EventError:
		metrics.AgentRunsTotal.WithLabelValues("failed").Inc()
	case agent.EventRunCancelled:
		metrics.AgentRunsTotal.WithLabelValues("cancelled").Inc()
	}
	for _, hook := range d.hooks {
		hook(d.scope, ev)
	}
}

func reportLifecycleHook(scope runtimeEventScope, ev agent.RuntimeEvent) {
	if ev.Type != agent.EventToolResult {
		return
	}
	result, ok := ev.Data.(agent.ToolResultData)
	if !ok {
		return
	}
	capability, hasCapability := runtimeToolCapability(scope.session, result.Name)
	if hasCapability && capability.EmitsReportPreview && scope.emitReportPreview != nil {
		scope.emitReportPreview()
	}
	if hasCapability && capability.DeliveryBoundary && result.Success && scope.finalizeReport != nil {
		if err := scope.finalizeReport(); err != nil {
			rollbackFinalizeDraft(scope)
			if scope.session != nil {
				scope.session.CancelRun(scope.runID)
			}
			if finishTerminalRun(scope, "failed") && scope.setRunStatus != nil {
				msg := "report saved but binding failed: " + err.Error()
				scope.setRunStatus(domain.RunStatusFailed, &msg)
			}
		}
	}
}

func runtimeToolCapability(sess *session.Session, name string) (tools.ToolCapability, bool) {
	if sess == nil || sess.Registry == nil {
		return tools.ToolCapability{}, false
	}
	tool, err := sess.Registry.Get(name)
	if err != nil {
		return tools.ToolCapability{}, false
	}
	provider, ok := tool.(tools.CapabilityTool)
	if !ok {
		return tools.ToolCapability{}, false
	}
	return provider.Capability(), true
}

func rollbackFinalizeDraft(scope runtimeEventScope) {
	if scope.session == nil || scope.session.ReportState == nil {
		return
	}

	state := scope.session.ReportState
	state.Lock()
	defer state.Unlock()
	state.NeedsFinalize = true
}

func runLifecycleHook(scope runtimeEventScope, ev agent.RuntimeEvent) {
	if scope.session == nil || strings.TrimSpace(scope.runID) == "" {
		return
	}

	switch ev.Type {
	case agent.EventUserRequestInput:
		if !scope.session.SuspendRun(scope.runID) {
			return
		}
		if scope.setRunStatus != nil {
			scope.setRunStatus(domain.RunStatusWaitingUserInput, nil)
		}
	case agent.EventRunCompleted:
		if !finishTerminalRun(scope, "completed") {
			return
		}
		if scope.setRunStatus != nil {
			scope.setRunStatus(domain.RunStatusCompleted, nil)
		}
		if complete, ok := ev.Data.(agent.CompleteData); ok && scope.setRunSummary != nil {
			scope.setRunSummary(complete.Summary)
		}
	case agent.EventRunCancelled:
		if !finishTerminalRun(scope, "cancelled") {
			return
		}
		if scope.setRunStatus != nil {
			scope.setRunStatus(domain.RunStatusCancelled, nil)
		}
	case agent.EventError:
		if !finishTerminalRun(scope, "failed") {
			return
		}
		if scope.setRunStatus == nil {
			return
		}
		if errData, ok := ev.Data.(agent.ErrorData); ok {
			msg := errData.Message
			scope.setRunStatus(domain.RunStatusFailed, &msg)
			return
		}
		scope.setRunStatus(domain.RunStatusFailed, nil)
	}
}

func finishTerminalRun(scope runtimeEventScope, status string) bool {
	if scope.session == nil {
		return false
	}
	current := scope.session.CurrentEditStateData()
	editWasActive := current != nil && current.Active
	finished := scope.session.FinishRun(scope.runID, status)
	if finished && editWasActive && scope.emitEditState != nil {
		scope.emitEditState(agent.EditStateUpdatedData{Active: false})
	}
	return finished
}

func runLoggingHook(scope runtimeEventScope, ev agent.RuntimeEvent) {
	if scope.session == nil || strings.TrimSpace(scope.runID) == "" {
		return
	}

	switch ev.Type {
	case agent.EventUserRequestInput:
		log.Printf("run suspended waiting_user_input run_id=%s session_id=%s", scope.runID, scope.session.ID)
	case agent.EventRunCompleted:
		if complete, ok := ev.Data.(agent.CompleteData); ok {
			log.Printf("run completed run_id=%s session_id=%s summary_chars=%d", scope.runID, scope.session.ID, len([]rune(strings.TrimSpace(complete.Summary))))
			return
		}
		log.Printf("run completed run_id=%s session_id=%s", scope.runID, scope.session.ID)
	case agent.EventRunCancelled:
		log.Printf("run cancelled run_id=%s session_id=%s", scope.runID, scope.session.ID)
	case agent.EventError:
		if errData, ok := ev.Data.(agent.ErrorData); ok {
			log.Printf("run failed run_id=%s session_id=%s error=%q", scope.runID, scope.session.ID, clipLogText(errData.Message, 240))
			return
		}
		log.Printf("run failed run_id=%s session_id=%s", scope.runID, scope.session.ID)
	}
}
