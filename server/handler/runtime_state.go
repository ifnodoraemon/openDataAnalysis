package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

func serializeRuntimeState(entries map[string]agent.MemoryEntry, subgoals []agent.Subgoal, reportHTML string) map[string]interface{} {
	return serializeRuntimeStateWithSnapshot(entries, subgoals, nil, reportHTML, nil)
}

func serializeRuntimeStateWithSnapshot(entries map[string]agent.MemoryEntry, subgoals []agent.Subgoal, reportSnapshot *domain.ReportSnapshot, reportHTML string, editState *agent.EditStateUpdatedData) map[string]interface{} {
	resp := map[string]interface{}{
		"memory_entries": entries,
		"subgoals":       subgoals,
		"report_html":    reportHTML,
	}
	if reportSnapshot != nil {
		resp["report_snapshot"] = reportSnapshot
	}
	if editState != nil {
		resp["edit_state"] = editState
	}
	return resp
}

func getSessionRuntimeState(ctx context.Context, workspaceID, userID, sessionID string) (map[string]agent.MemoryEntry, []agent.Subgoal, *domain.ReportSnapshot, string, *agent.EditStateUpdatedData, error) {
	if sessionManager != nil {
		sess, ok, err := sessionManager.Peek(sessionID, workspaceID, userID)
		if err != nil {
			return nil, nil, nil, "", nil, fmt.Errorf("failed to inspect live session state: %w", err)
		}
		if ok && sess != nil {
			memory, subgoals := sess.RuntimeState()
			reportSnapshot, reportHTML, err := renderLiveSessionRuntimeReport(sess)
			if err != nil {
				return nil, nil, nil, "", nil, err
			}
			return memory, subgoals, reportSnapshot, reportHTML, sess.CurrentEditStateData(), nil
		}
	}

	return loadSessionRuntimeStateFromPersistence(ctx, sessionID)
}

func loadSessionRuntimeStateFromPersistence(ctx context.Context, sessionID string) (map[string]agent.MemoryEntry, []agent.Subgoal, *domain.ReportSnapshot, string, *agent.EditStateUpdatedData, error) {
	messages, err := collectSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, nil, nil, "", nil, err
	}
	if len(messages) == 0 {
		return map[string]agent.MemoryEntry{}, []agent.Subgoal{}, nil, "", nil, nil
	}
	return deriveRuntimeStateFromMessages(messages)
}

func deriveRuntimeStateFromRun(ctx context.Context, runID string) (map[string]agent.MemoryEntry, []agent.Subgoal, *domain.ReportSnapshot, string, *agent.EditStateUpdatedData, error) {
	messages, err := collectRunTreeMessages(ctx, runID)
	if err != nil {
		return nil, nil, nil, "", nil, err
	}
	if len(messages) == 0 {
		return map[string]agent.MemoryEntry{}, []agent.Subgoal{}, nil, "", nil, nil
	}
	return deriveRuntimeStateFromMessages(messages)
}

func collectSessionMessages(ctx context.Context, sessionID string) ([]domain.RunMessage, error) {
	if runRepo == nil || messageRepo == nil {
		return nil, fmt.Errorf("runtime state repositories are not configured")
	}
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(sessionID) != sessionID {
		return nil, fmt.Errorf("session id must be a non-empty exact value")
	}
	runs, err := runRepo.ListBySession(ctx, sessionID, -1)
	if err != nil {
		return nil, fmt.Errorf("failed to list session tasks: %w", err)
	}
	var messages []domain.RunMessage
	for _, run := range runs {
		runMessages, err := messageRepo.ListByRun(ctx, run.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to list messages for task %s: %w", run.ID, err)
		}
		messages = append(messages, runMessages...)
	}
	sortRunMessages(messages)
	return messages, nil
}

func collectRunTreeMessages(ctx context.Context, runID string) ([]domain.RunMessage, error) {
	if messageRepo == nil || runRepo == nil {
		return nil, fmt.Errorf("runtime state repositories are not configured")
	}
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(runID) != runID {
		return nil, fmt.Errorf("task id must be a non-empty exact value")
	}
	messages, err := messageRepo.ListByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("failed to list task messages: %w", err)
	}

	queue := []string{runID}
	visited := map[string]bool{runID: true}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		childRuns, err := runRepo.ListByParent(ctx, curr)
		if err != nil {
			return nil, fmt.Errorf("failed to list child tasks for %s: %w", curr, err)
		}
		for _, childRun := range childRuns {
			if visited[childRun.ID] {
				continue
			}
			visited[childRun.ID] = true
			queue = append(queue, childRun.ID)
			childMsgs, err := messageRepo.ListByRun(ctx, childRun.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to list messages for child task %s: %w", childRun.ID, err)
			}
			messages = append(messages, childMsgs...)
		}
	}
	sortRunMessages(messages)
	return messages, nil
}

func sortRunMessages(messages []domain.RunMessage) {
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].CreatedAt.Before(messages[j].CreatedAt)
	})
}

func deriveRuntimeStateFromMessages(messages []domain.RunMessage) (map[string]agent.MemoryEntry, []agent.Subgoal, *domain.ReportSnapshot, string, *agent.EditStateUpdatedData, error) {
	entries := map[string]agent.MemoryEntry{}
	subgoals := []agent.Subgoal{}
	var reportSnapshot *domain.ReportSnapshot
	reportHTML := ""
	var editState *agent.EditStateUpdatedData

	for _, msg := range messages {
		switch msg.Type {
		case string(agent.EventStateMemoryUpdated):
			var payload agent.MemoryUpdatedData
			if err := jsoncontract.Decode([]byte(msg.Content), &payload); err != nil {
				return nil, nil, nil, "", nil, fmt.Errorf("invalid memory state event %s: %w", msg.ID, err)
			}
			entries = payload.Entries
		case string(agent.EventStateSubgoalsUpdated):
			var payload struct {
				Goals []agent.Subgoal `json:"goals"`
			}
			if err := jsoncontract.Decode([]byte(msg.Content), &payload); err != nil {
				return nil, nil, nil, "", nil, fmt.Errorf("invalid subgoal state event %s: %w", msg.ID, err)
			}
			subgoals = payload.Goals
		case string(agent.EventReportUpdate), string(agent.EventReportFinal):
			var payload agent.ReportUpdateData
			if err := jsoncontract.Decode([]byte(msg.Content), &payload); err != nil {
				return nil, nil, nil, "", nil, fmt.Errorf("invalid report state event %s: %w", msg.ID, err)
			}
			if payload.ReportSnapshot != nil {
				reportSnapshot = payload.ReportSnapshot
			}
			if strings.TrimSpace(payload.HTML) != "" {
				reportHTML = payload.HTML
			}
		case string(agent.EventStateReportEditUpdated):
			var payload agent.EditStateUpdatedData
			if err := jsoncontract.Decode([]byte(msg.Content), &payload); err != nil {
				return nil, nil, nil, "", nil, fmt.Errorf("invalid edit state event %s: %w", msg.ID, err)
			}
			if payload.Active && payload.EditContext != nil {
				editState = &payload
			} else {
				editState = nil
			}
		}
	}

	if reportSnapshot != nil {
		html, err := renderReportHTMLFromSnapshotData(reportSnapshot)
		if err != nil {
			return nil, nil, nil, "", nil, fmt.Errorf("persisted report snapshot is not renderable: %w", err)
		}
		reportHTML = html
	}

	return entries, subgoals, reportSnapshot, reportHTML, editState, nil
}

func attachRuntimeState(ctx context.Context, resp map[string]interface{}, workspaceID, userID, sessionID string) error {
	entries, subgoals, reportSnapshot, reportHTML, editState, err := getSessionRuntimeState(ctx, workspaceID, userID, sessionID)
	if err != nil {
		return err
	}
	state := serializeRuntimeStateWithSnapshot(entries, subgoals, reportSnapshot, reportHTML, editState)
	resp["runtimeState"] = state
	return nil
}

func attachRunRuntimeState(ctx context.Context, resp map[string]interface{}, run domain.AnalysisRun) error {
	entries, subgoals, reportSnapshot, reportHTML, editState, err := deriveRuntimeStateFromRun(ctx, run.ID)
	if err != nil {
		return err
	}
	state := serializeRuntimeStateWithSnapshot(entries, subgoals, reportSnapshot, reportHTML, editState)
	resp["runtimeState"] = state
	return nil
}

func renderLiveSessionRuntimeReport(sess *session.Session) (*domain.ReportSnapshot, string, error) {
	if sess == nil || sess.ReportState == nil {
		return nil, "", nil
	}
	sess.ReportState.RLock()
	hasContent := len(sess.ReportState.Blocks) > 0 || len(sess.ReportState.Charts) > 0
	sess.ReportState.RUnlock()
	if !hasContent {
		return nil, "", nil
	}
	snapshot, err := buildReportSnapshot(sess.ReportState)
	if err != nil {
		return nil, "", fmt.Errorf("build live report snapshot: %w", err)
	}
	html, err := renderReportHTMLFromSnapshotData(&snapshot)
	if err != nil {
		return nil, "", fmt.Errorf("live report snapshot is not renderable: %w", err)
	}
	return &snapshot, html, nil
}
