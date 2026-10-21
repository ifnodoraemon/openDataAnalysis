package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/internal/idgen"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// Job status values reported to the model.
const (
	JobStatusRunning   = "running"
	JobStatusSucceeded = "succeeded"
	JobStatusFailed    = "failed"
)

// maxRetainedJobs bounds how many jobs a session keeps. Old terminal jobs are
// evicted first; a running job is never evicted.
const maxRetainedJobs = 32

// jobAllowedTools is the set of tools that may run as a background job.
//
// Only the long-running analysis tools qualify. A report mutation or an import
// must complete inside the turn that requested it: the model's next step reads
// their effect, and a background writer would make the report state racy.
var jobAllowedTools = map[string]struct{}{
	"code_run_python": {},
	"data_query_sql":  {},
}

// Job is one background tool execution.
type Job struct {
	ID           string
	Tool         string
	Status       string
	ResultID     string
	ResultChars  int
	ErrorMessage string
	StartedAt    time.Time
	FinishedAt   time.Time
}

// Duration reports how long the job has run so far.
func (j Job) Duration() time.Duration {
	end := j.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(j.StartedAt)
}

// JobManager runs long analysis tools in the background so a run does not block
// on them.
//
// Jobs belong to a session, not to a run: the model submits a query, ends its
// turn if it wants, and reads the result later. They live in process memory, so
// a restart forgets them — an unknown id is reported as such instead of being
// silently treated as pending or failed.
type JobManager struct {
	mu       sync.Mutex
	execMu   sync.Mutex
	registry *tools.Registry
	jobs     map[string]*Job
	order    []string
	// unannounced holds finished job ids that the model has not been told
	// about yet. They ride the next request as runtime facts, so a finished
	// job does not have to be discovered by polling for it to be noticed.
	unannounced []string
}

// NewJobManager creates an empty job manager. The registry is attached once the
// session has finished building its tool set.
func NewJobManager() *JobManager {
	return &JobManager{jobs: make(map[string]*Job)}
}

// SetRegistry attaches the session registry the jobs execute against.
func (m *JobManager) SetRegistry(registry *tools.Registry) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry = registry
}

// Submit starts a background execution and returns its handle immediately.
//
// The job keeps the submitting turn's execution context — session, workspace,
// user, run — but not its cancellation: a job outlives the turn that started it.
func (m *JobManager) Submit(ctx context.Context, toolName string, arguments json.RawMessage) (Job, error) {
	if m == nil {
		return Job{}, fmt.Errorf("background jobs are not available in this session")
	}
	if _, ok := jobAllowedTools[toolName]; !ok {
		return Job{}, fmt.Errorf("tool %q cannot run as a background job", toolName)
	}
	if len(arguments) == 0 {
		return Job{}, fmt.Errorf("job arguments are required")
	}

	m.mu.Lock()
	registry := m.registry
	if registry == nil {
		m.mu.Unlock()
		return Job{}, fmt.Errorf("background jobs are not available in this session")
	}
	job := &Job{
		ID:        idgen.Prefixed("job_"),
		Tool:      toolName,
		Status:    JobStatusRunning,
		StartedAt: time.Now(),
	}
	m.jobs[job.ID] = job
	m.order = append(m.order, job.ID)
	m.evictLocked()
	snapshot := *job
	m.mu.Unlock()

	jobCtx := context.WithoutCancel(ctx)
	go m.run(jobCtx, job.ID, toolName, registry, append(json.RawMessage(nil), arguments...))
	return snapshot, nil
}

// run executes one job. Executions are serialized because the tools are shared
// session instances that carry their execution context in a field: two at once
// could install each other's context.
func (m *JobManager) run(ctx context.Context, jobID, toolName string, registry *tools.Registry, args json.RawMessage) {
	m.execMu.Lock()
	defer m.execMu.Unlock()

	tool, err := registry.Get(toolName)
	if err != nil {
		m.finish(jobID, "", 0, fmt.Errorf("tool %q is not available: %w", toolName, err))
		return
	}
	if aware, ok := tool.(executionContextAware); ok {
		aware.SetExecutionContext(ctx)
	}
	result, execErr := tool.Execute(args)
	if execErr != nil {
		m.finish(jobID, "", 0, execErr)
		return
	}
	resultID, spillErr := spillToolResult(result)
	if spillErr != nil {
		m.finish(jobID, "", len([]rune(result)), fmt.Errorf("job result could not be retained: %w", spillErr))
		return
	}
	m.finish(jobID, resultID, len([]rune(result)), nil)
}

func (m *JobManager) finish(jobID, resultID string, resultChars int, failure error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return
	}
	job.FinishedAt = time.Now()
	job.ResultID = resultID
	job.ResultChars = resultChars
	if failure != nil {
		job.Status = JobStatusFailed
		job.ErrorMessage = sanitizeToolError(failure.Error())
	} else {
		job.Status = JobStatusSucceeded
	}
	m.unannounced = append(m.unannounced, jobID)
}

// TakeCompletedNotices returns one factual notice per job that finished since
// the last call, and clears them so each is delivered exactly once.
//
// This is how a finished job reaches the model without being polled for: the
// notices ride the next request alongside the other runtime facts. They state
// what happened and leave the decision to the model.
func (m *JobManager) TakeCompletedNotices() []RuntimeContextBlock {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.unannounced) == 0 {
		return nil
	}
	ids := m.unannounced
	m.unannounced = nil

	notices := make([]RuntimeContextBlock, 0, len(ids))
	for _, id := range ids {
		job, ok := m.jobs[id]
		if !ok {
			// Evicted before it could be announced; the model will find the id
			// unknown if it asks, which is the same fact.
			continue
		}
		payload := map[string]interface{}{
			"job_id":       job.ID,
			"job_tool":     job.Tool,
			"status":       job.Status,
			"duration_ms":  job.Duration().Milliseconds(),
			"result_chars": job.ResultChars,
		}
		if job.ErrorMessage != "" {
			payload["error_message"] = job.ErrorMessage
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		notices = append(notices, RuntimeContextBlock{
			Name:    "background_job_finished",
			Role:    LLMRoleUser,
			Content: string(encoded),
		})
	}
	return notices
}

// Status returns a snapshot of one job.
func (m *JobManager) Status(jobID string) (Job, error) {
	if m == nil {
		return Job{}, fmt.Errorf("background jobs are not available in this session")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	if !ok {
		return Job{}, fmt.Errorf("unknown job %q: its outcome is unknown, because background jobs do not survive a process restart or session eviction", jobID)
	}
	return *job, nil
}

// evictLocked drops the oldest finished jobs once the retention bound is
// exceeded. Running jobs are never dropped. The caller holds m.mu.
func (m *JobManager) evictLocked() {
	for len(m.order) > maxRetainedJobs {
		evicted := false
		for i, id := range m.order {
			job, ok := m.jobs[id]
			if !ok {
				m.order = append(m.order[:i], m.order[i+1:]...)
				evicted = true
				break
			}
			if job.Status == JobStatusRunning {
				continue
			}
			delete(m.jobs, id)
			m.order = append(m.order[:i], m.order[i+1:]...)
			evicted = true
			break
		}
		if !evicted {
			// Everything retained is still running; the bound is advisory then.
			return
		}
	}
}
