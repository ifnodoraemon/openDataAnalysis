package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

func init() {
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Jobs == nil {
			return nil
		}
		manager, ok := ctx.Jobs.(*JobManager)
		if !ok {
			panic("tool context jobs has an invalid type")
		}
		return &SubmitJobTool{Jobs: manager}
	})
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Jobs == nil {
			return nil
		}
		manager, ok := ctx.Jobs.(*JobManager)
		if !ok {
			panic("tool context jobs has an invalid type")
		}
		return &InspectJobStatusTool{Jobs: manager}
	})
}

// SubmitJobTool starts a long analysis tool in the background.
type SubmitJobTool struct {
	Jobs *JobManager
	ctx  context.Context
}

// SetExecutionContext receives the current run's context from the engine. The
// job inherits it, so its tool calls run with the same session, workspace and
// user facts as the turn that submitted them.
func (t *SubmitJobTool) SetExecutionContext(ctx context.Context) { t.ctx = ctx }

func (t *SubmitJobTool) Name() string { return "job_submit" }

func (t *SubmitJobTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "action", RuntimeEnabled: true}
}

func (t *SubmitJobTool) Description() string {
	return "Start a long-running analysis tool in the background and return its job id immediately. The job keeps running after the current turn ends. Its state is read with state_job_inspect; the id of its retained result payload is readable in pages with state_tool_result_inspect. Jobs do not survive a process restart, so an id from an earlier process reports an unknown outcome. Returns job_id, status, and the start time."
}

func (t *SubmitJobTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"tool": {
				"type": "string",
				"enum": ["code_run_python", "data_query_sql"],
				"description": "The analysis tool to run in the background."
			},
			"arguments": {
				"type": "object",
				"description": "Arguments for that tool, exactly as they would be passed to it directly."
			}
		},
		"required": ["tool", "arguments"]
	}`)
}

func (t *SubmitJobTool) Execute(args json.RawMessage) (string, error) {
	if t.Jobs == nil {
		return "", fmt.Errorf("background jobs are not initialized")
	}
	var payload struct {
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := jsoncontract.Decode(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	job, err := t.Jobs.Submit(ctx, payload.Tool, payload.Arguments)
	if err != nil {
		return "", err
	}
	return marshalToolPayload(map[string]interface{}{
		"ok":         true,
		"tool":       "job_submit",
		"job_id":     job.ID,
		"job_tool":   job.Tool,
		"status":     job.Status,
		"started_at": job.StartedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"ui_summary": fmt.Sprintf("已在后台启动 %s，任务编号 %s。", job.Tool, job.ID),
	})
}

// InspectJobStatusTool reports the state of one background job.
type InspectJobStatusTool struct{ Jobs *JobManager }

func (t *InspectJobStatusTool) Name() string { return "state_job_inspect" }

func (t *InspectJobStatusTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectJobStatusTool) Description() string {
	return "Read the fact state of one background job: status (running, succeeded, failed), elapsed duration, retained result size and its payload id, and the failure message when it failed. The payload id is readable in pages with state_tool_result_inspect. Does not modify any state."
}

func (t *InspectJobStatusTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"job_id": {"type": "string", "description": "The job id returned by job_submit."}
		},
		"required": ["job_id"]
	}`)
}

func (t *InspectJobStatusTool) Execute(args json.RawMessage) (string, error) {
	if t.Jobs == nil {
		return "", fmt.Errorf("background jobs are not initialized")
	}
	var payload struct {
		JobID string `json:"job_id"`
	}
	if err := jsoncontract.Decode(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	job, err := t.Jobs.Status(payload.JobID)
	if err != nil {
		return "", err
	}
	result := map[string]interface{}{
		"ok":            true,
		"tool":          "state_job_inspect",
		"job_id":        job.ID,
		"job_tool":      job.Tool,
		"status":        job.Status,
		"duration_ms":   job.Duration().Milliseconds(),
		"result_chars":  job.ResultChars,
		"result_id":     job.ResultID,
		"has_result":    job.ResultID != "",
		"error_message": job.ErrorMessage,
	}
	switch job.Status {
	case JobStatusRunning:
		result["ui_summary"] = fmt.Sprintf("后台任务 %s 仍在运行。", job.ID)
	case JobStatusSucceeded:
		result["ui_summary"] = fmt.Sprintf("后台任务 %s 已完成。", job.ID)
	default:
		result["ui_summary"] = fmt.Sprintf("后台任务 %s 失败。", job.ID)
	}
	return marshalToolPayload(result)
}
