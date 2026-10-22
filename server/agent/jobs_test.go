package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

// jobTestTool stands in for a long analysis tool. It is named after a real
// tool because the job allowlist is by name.
type jobTestTool struct {
	name  string
	delay time.Duration
	body  string
}

func (t *jobTestTool) Name() string        { return t.name }
func (t *jobTestTool) Description() string { return "job test tool" }
func (t *jobTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}

func (t *jobTestTool) Capability() tools.ToolCapability {
	return tools.ToolCapability{Mode: "observe"}
}

func (t *jobTestTool) Execute(args json.RawMessage) (string, error) {
	time.Sleep(t.delay)
	return `{"ok":true,"tool":"` + t.name + `","body":"` + t.body + `"}`, nil
}

func newJobTestManager(t *testing.T, tool tools.Tool) *JobManager {
	t.Helper()
	previous := config.Cfg
	config.Cfg = &config.Config{TempDir: t.TempDir()}
	t.Cleanup(func() { config.Cfg = previous })

	registry := tools.NewRegistry()
	registry.Register(tool)
	manager := NewJobManager()
	manager.SetRegistry(registry)
	return manager
}

// waitForJobStatus polls until the job reaches a terminal state.
func waitForJobStatus(t *testing.T, manager *JobManager, jobID, want string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Status(jobID)
		if err != nil {
			t.Fatalf("Status(%s): %v", jobID, err)
		}
		if job.Status == want {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %q", jobID, want)
	return Job{}
}

func TestJobRunsInBackgroundAndResultIsReadable(t *testing.T) {

	body := strings.Repeat("分析结果", 200)
	manager := newJobTestManager(t, &jobTestTool{name: "code_run_python", delay: 80 * time.Millisecond, body: body})

	job, err := manager.Submit(context.Background(), "code_run_python", json.RawMessage(`{"code":"pass"}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if job.Status != JobStatusRunning {
		t.Fatalf("submit must return immediately while the job runs, got %q", job.Status)
	}
	// A running job has no retained payload yet, which is itself the fact the
	// model sees: there is nothing to read until it finishes.
	if job.ResultID != "" {
		t.Fatalf("a running job must not expose a payload id, got %q", job.ResultID)
	}

	finished := waitForJobStatus(t, manager, job.ID, JobStatusSucceeded)
	if finished.ResultID == "" || finished.ResultChars == 0 {
		t.Fatalf("expected a retained result, got %#v", finished)
	}

	// The model reads the payload through the shared reader, by the id the
	// status tool reports.
	content, total, err := readSpilledToolResult(finished.ResultID, 0, 100)
	if err != nil {
		t.Fatalf("read job payload: %v", err)
	}
	if !strings.HasPrefix(content, `{"ok":true`) {
		t.Fatalf("unexpected content prefix %q", content)
	}
	if total != finished.ResultChars {
		t.Fatalf("expected total %d, got %d", finished.ResultChars, total)
	}

	// Paging walks the whole payload.
	var assembled strings.Builder
	offset := 0
	for {
		page, pageTotal, pageErr := readSpilledToolResult(finished.ResultID, offset, 500)
		if pageErr != nil {
			t.Fatalf("read job payload page at %d: %v", offset, pageErr)
		}
		assembled.WriteString(page)
		offset += len([]rune(page))
		if offset >= pageTotal {
			break
		}
	}
	if got, want := len([]rune(assembled.String())), len([]rune(`{"ok":true,"tool":"code_run_python","body":"`+body+`"}`)); got != want {
		t.Fatalf("paged read did not reassemble the payload: got %d runes, want %d", got, want)
	}
}

func TestJobRejectsToolsOutsideTheAllowlist(t *testing.T) {

	manager := newJobTestManager(t, &jobTestTool{name: "report_finalize"})
	if _, err := manager.Submit(context.Background(), "report_finalize", json.RawMessage(`{}`)); err == nil {
		t.Fatalf("a state-mutating tool must not run as a background job")
	}
}

func TestUnknownJobReportsUnknownOutcome(t *testing.T) {

	manager := newJobTestManager(t, &jobTestTool{name: "code_run_python"})
	_, err := manager.Status("job_missing")
	if err == nil {
		t.Fatalf("expected an error for an unknown job")
	}
	if !strings.Contains(err.Error(), "unknown job") {
		t.Fatalf("the error must state the outcome is unknown, got %v", err)
	}
}

// Two submitted jobs must not share state or results.
func TestConcurrentJobsStayIndependent(t *testing.T) {

	previous := config.Cfg
	config.Cfg = &config.Config{TempDir: t.TempDir()}
	t.Cleanup(func() { config.Cfg = previous })

	registry := tools.NewRegistry()
	registry.Register(&jobTestTool{name: "code_run_python", delay: 50 * time.Millisecond, body: "first"})
	manager := NewJobManager()
	manager.SetRegistry(registry)

	first, err := manager.Submit(context.Background(), "code_run_python", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Submit first: %v", err)
	}
	second, err := manager.Submit(context.Background(), "code_run_python", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Submit second: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("job ids must be distinct")
	}
	waitForJobStatus(t, manager, first.ID, JobStatusSucceeded)
	waitForJobStatus(t, manager, second.ID, JobStatusSucceeded)

	firstJob := waitForJobStatus(t, manager, first.ID, JobStatusSucceeded)
	content, _, err := readSpilledToolResult(firstJob.ResultID, 0, 500)
	if err != nil {
		t.Fatalf("read first job payload: %v", err)
	}
	if !strings.Contains(content, "first") {
		t.Fatalf("unexpected first job content %q", content)
	}
}

// A failed job reports the failure instead of pretending to have a result.
func TestFailedJobReportsFailure(t *testing.T) {

	previous := config.Cfg
	config.Cfg = &config.Config{TempDir: t.TempDir()}
	t.Cleanup(func() { config.Cfg = previous })

	registry := tools.NewRegistry()
	registry.Register(&failingJobTool{})
	manager := NewJobManager()
	manager.SetRegistry(registry)

	job, err := manager.Submit(context.Background(), "data_query_sql", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	finished := waitForJobStatus(t, manager, job.ID, JobStatusFailed)
	if finished.ErrorMessage == "" {
		t.Fatalf("a failed job must carry its failure reason")
	}
	if finished.ResultID != "" {
		t.Fatalf("a failed job must not retain a payload, got %q", finished.ResultID)
	}
}

type failingJobTool struct{ jobTestTool }

func (t *failingJobTool) Name() string { return "data_query_sql" }
func (t *failingJobTool) Execute(json.RawMessage) (string, error) {
	return "", &testAlwaysError{}
}

type testAlwaysError struct{}

func (e *testAlwaysError) Error() string { return "query timed out" }

// The job tools must be registered for a session that has a job manager, and
// their contracts must describe facts rather than prescribe a workflow.
func TestJobToolsAreRegisteredAndContractShaped(t *testing.T) {

	registry := tools.NewRegistry()
	registry.LoadGlobalTools(tools.ToolContext{Jobs: NewJobManager()})

	for _, name := range []string{"job_submit", "state_job_inspect"} {
		tool, err := registry.Get(name)
		if err != nil {
			t.Fatalf("expected %s to be registered: %v", name, err)
		}
		description := tool.Description()
		for _, forbidden := range []string{"next_action", "you should", "first call"} {
			if strings.Contains(strings.ToLower(description), forbidden) {
				t.Fatalf("%s description prescribes a path: %q", name, description)
			}
		}
	}

	// A session without a job manager must not advertise job tools.
	plainRegistry := tools.NewRegistry()
	plainRegistry.LoadGlobalTools(tools.ToolContext{})
	if _, err := plainRegistry.Get("job_submit"); err == nil {
		t.Fatalf("job tools must not be registered without a job manager")
	}
}

// A finished job is announced once, as a fact, on the next request's runtime
// context. It must not require polling to be noticed, and must not repeat.
func TestCompletedJobIsAnnouncedOnceAsRuntimeFact(t *testing.T) {
	manager := newJobTestManager(t, &jobTestTool{name: "code_run_python", delay: 20 * time.Millisecond, body: "done"})

	job, err := manager.Submit(context.Background(), "code_run_python", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForJobStatus(t, manager, job.ID, JobStatusSucceeded)

	notices := manager.TakeCompletedNotices()
	if len(notices) != 1 {
		t.Fatalf("expected one completion notice, got %d", len(notices))
	}
	notice := notices[0]
	if notice.Name != "background_job_finished" || notice.Role != LLMRoleUser {
		t.Fatalf("unexpected notice block: %#v", notice)
	}
	if !strings.Contains(notice.Content, job.ID) || !strings.Contains(notice.Content, JobStatusSucceeded) {
		t.Fatalf("notice must state the job and its outcome: %q", notice.Content)
	}
	if again := manager.TakeCompletedNotices(); len(again) != 0 {
		t.Fatalf("a completion must be announced exactly once, got %#v", again)
	}
}

// The engine carries the notice on the request it builds, so the model sees the
// completion without polling for it.
func TestEngineInjectsJobCompletionIntoRuntimeContext(t *testing.T) {
	manager := newJobTestManager(t, &jobTestTool{name: "code_run_python", delay: 10 * time.Millisecond, body: "done"})

	job, err := manager.Submit(context.Background(), "code_run_python", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitForJobStatus(t, manager, job.ID, JobStatusSucceeded)

	engine := &Engine{policy: "system", jobs: manager}
	bundle := engine.buildBundle("", nil, nil, nil)

	found := false
	for _, block := range bundle.RuntimeContext {
		if block.Name == "background_job_finished" {
			found = true
			if !strings.Contains(block.Content, job.ID) {
				t.Fatalf("unexpected notice content %q", block.Content)
			}
		}
	}
	if !found {
		t.Fatalf("expected a background_job_finished block, got %#v", bundle.RuntimeContext)
	}

	// The next request must not repeat it.
	next := engine.buildBundle("", nil, nil, nil)
	for _, block := range next.RuntimeContext {
		if block.Name == "background_job_finished" {
			t.Fatalf("the completion notice must be delivered once, got %#v", block)
		}
	}
}
