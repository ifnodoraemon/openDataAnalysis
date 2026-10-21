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
	tools.RegisterGlobalTool(func(ctx tools.ToolContext) tools.Tool {
		if ctx.Jobs == nil {
			return nil
		}
		manager, ok := ctx.Jobs.(*JobManager)
		if !ok {
			panic("tool context jobs has an invalid type")
		}
		return &ReadJobOutputTool{Jobs: manager}
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
	return "Start a long-running analysis tool in the background and return its job id immediately. The job keeps running after the current turn ends; its status is a fact readable with job_status and its result is readable in pages with job_output. Jobs do not survive a process restart, so an id from an earlier process reports an unknown outcome. Returns job_id, status, and the start time."
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

func (t *InspectJobStatusTool) Name() string { return "job_status" }

func (t *InspectJobStatusTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *InspectJobStatusTool) Description() string {
	return "Read the fact state of one background job: status (running, succeeded, failed), elapsed duration, retained result size, and the failure message when it failed. Does not modify any state."
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
		"tool":          "job_status",
		"job_id":        job.ID,
		"job_tool":      job.Tool,
		"status":        job.Status,
		"duration_ms":   job.Duration().Milliseconds(),
		"result_chars":  job.ResultChars,
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

// ReadJobOutputTool pages through a finished job's retained result.
type ReadJobOutputTool struct{ Jobs *JobManager }

func (t *ReadJobOutputTool) Name() string { return "job_output" }

func (t *ReadJobOutputTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe", RuntimeEnabled: true, Delegable: true}
}

func (t *ReadJobOutputTool) Description() string {
	return "Read a window of a finished background job's result. Returns at most 6000 runes per call, with the total length and whether more remains. A job that is still running, failed, or did not retain a result reports that instead of content. Does not modify any state."
}

func (t *ReadJobOutputTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"additionalProperties": false,
		"properties": {
			"job_id": {"type": "string", "description": "The job id returned by job_submit."},
			"offset": {"type": "integer", "minimum": 0, "description": "Rune offset into the result. Defaults to 0."},
			"limit": {"type": "integer", "minimum": 1, "maximum": 6000, "description": "Maximum runes to return. Defaults to 6000."}
		},
		"required": ["job_id"]
	}`)
}

func (t *ReadJobOutputTool) Execute(args json.RawMessage) (string, error) {
	if t.Jobs == nil {
		return "", fmt.Errorf("background jobs are not initialized")
	}
	var payload struct {
		JobID  string `json:"job_id"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := jsoncontract.Decode(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	content, job, total, err := t.Jobs.Output(payload.JobID, payload.Offset, payload.Limit)
	if err != nil {
		return "", err
	}
	return marshalToolPayload(map[string]interface{}{
		"ok":          true,
		"tool":        "job_output",
		"job_id":      job.ID,
		"job_tool":    job.Tool,
		"content":     content,
		"total_chars": total,
		"offset":      payload.Offset,
		"has_more":    payload.Offset+len([]rune(content)) < total,
		"ui_summary":  fmt.Sprintf("已读取后台任务 %s 的结果片段。", job.ID),
	})
}
