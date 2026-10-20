package handler

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/domain"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

// Approval auditing records who was asked to authorize what, how they answered,
// and which calls the runtime refused. It is the accountability trail for the
// fail-closed admission decision.
const (
	auditEventApprovalRequested = "approval_requested"
	auditEventApprovalDecided   = "approval_decided"
	auditEventApprovalDenied    = "approval_denied"
	auditResourceApproval       = "tool_approval"
)

func encodeApprovalAuditPayload(payload map[string]interface{}) string {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// recordApprovalAudit writes one approval audit row. Auditing must never break
// the run, so a failure is logged rather than propagated.
func recordApprovalAudit(ctx context.Context, event domain.AuditEvent) {
	if auditEventRepo == nil {
		return
	}
	event.ID = uuid.New().String()
	event.CreatedAt = time.Now()
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()
	if err := auditEventRepo.Create(persistCtx, &event); err != nil {
		log.Printf("approval audit write failed type=%s session_id=%s err=%v", event.EventType, event.SessionID, err)
	}
}

// recordApprovalRequested audits an authorization request the model put to the
// user. It is called for the event that suspends the run.
func recordApprovalRequested(ctx context.Context, workspaceID, sessionID, runID string, ask agent.AskUserData) {
	if ask.Authorization == nil {
		return
	}
	recordApprovalAudit(ctx, domain.AuditEvent{
		WorkspaceID:  workspaceID,
		SessionID:    sessionID,
		RunID:        runID,
		EventType:    auditEventApprovalRequested,
		ResourceType: auditResourceApproval,
		ResourceID:   ask.Authorization.ResourceRef,
		PayloadJSON: encodeApprovalAuditPayload(map[string]interface{}{
			"action":       ask.Authorization.Action,
			"resource_ref": ask.Authorization.ResourceRef,
			"question":     ask.Question,
		}),
	})
}

// recordApprovalDecision audits how the user answered an authorization request.
func recordApprovalDecision(ctx context.Context, sess *session.Session, identity auth.Identity, runID string, outcome agent.ApprovalOutcome) {
	decision := "rejected"
	if outcome.Approved {
		decision = "approved"
	}
	workspaceID := identity.WorkspaceID
	sessionID := ""
	if sess != nil {
		workspaceID = sess.WorkspaceID
		sessionID = sess.ID
	}
	recordApprovalAudit(ctx, domain.AuditEvent{
		WorkspaceID:  workspaceID,
		SessionID:    sessionID,
		RunID:        runID,
		ActorUserID:  identity.UserID,
		EventType:    auditEventApprovalDecided,
		ResourceType: auditResourceApproval,
		ResourceID:   outcome.ResourceRef,
		PayloadJSON: encodeApprovalAuditPayload(map[string]interface{}{
			"action":       outcome.Action,
			"resource_ref": outcome.ResourceRef,
			"decision":     decision,
		}),
	})
}

// recordApprovalDenied audits a tool call the runtime refused to execute.
func recordApprovalDenied(ctx context.Context, workspaceID, sessionID, runID string, denied agent.ApprovalDeniedData) {
	recordApprovalAudit(ctx, domain.AuditEvent{
		WorkspaceID:  workspaceID,
		SessionID:    sessionID,
		RunID:        runID,
		EventType:    auditEventApprovalDenied,
		ResourceType: auditResourceApproval,
		ResourceID:   denied.Tool,
		PayloadJSON: encodeApprovalAuditPayload(map[string]interface{}{
			"tool":     denied.Tool,
			"decision": denied.Decision,
			"reason":   denied.Reason,
		}),
	})
}
