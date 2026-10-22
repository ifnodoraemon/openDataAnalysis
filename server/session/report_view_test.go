package session

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/render"
)

type fakeReportViewBuilder struct {
	mu      sync.Mutex
	built   []int
	started chan int
	release <-chan struct{}
}

func (f *fakeReportViewBuilder) BuildView(ctx context.Context, request render.BuildRequest) *agent.ReportView {
	f.mu.Lock()
	f.built = append(f.built, request.ReportVersion)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- request.ReportVersion:
		default:
		}
	}
	if f.release != nil {
		<-f.release
	}
	return &agent.ReportView{
		ReportVersion: request.ReportVersion,
		CapturedAt:    request.CapturedAt,
		Facts:         json.RawMessage(`{"ok":true}`),
	}
}

func (f *fakeReportViewBuilder) builtVersions() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.built...)
}

func waitFor(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCoordinatorStartsEmpty(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	if view := coordinator.Current(); view != nil {
		t.Fatalf("expected no view before the first capture, got %+v", view)
	}
	coordinator.Close()
}

func TestCoordinatorDoesNotBlockAndPublishesView(t *testing.T) {
	builder := &fakeReportViewBuilder{}
	coordinator := NewReportViewCoordinator(builder, "http://client/", time.Second)
	defer coordinator.Close()

	started := time.Now()
	coordinator.Request(3, "dirty_blocks", "<html></html>", nil)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("Request blocked for %s", elapsed)
	}
	waitFor(t, "the first view", func() bool { return coordinator.Current() != nil })
	if version := coordinator.Current().ReportVersion; version != 3 {
		t.Fatalf("expected view version 3, got %d", version)
	}
}

func TestCoordinatorDeduplicatesSameVersion(t *testing.T) {
	builder := &fakeReportViewBuilder{}
	coordinator := NewReportViewCoordinator(builder, "http://client/", time.Second)
	defer coordinator.Close()

	coordinator.Request(5, "dirty_blocks", "<html></html>", nil)
	waitFor(t, "the first view", func() bool { return coordinator.Current() != nil })
	coordinator.Request(5, "dirty_blocks", "<html></html>", nil)
	coordinator.Request(4, "dirty_blocks", "<html></html>", nil)
	time.Sleep(50 * time.Millisecond)
	if versions := builder.builtVersions(); len(versions) != 1 {
		t.Fatalf("stale or duplicate versions were captured: %v", versions)
	}
}

// TestCoordinatorIsSingleFlightAndLatestWins is the core contract: a burst of
// edits must not spawn parallel browsers, and only the newest pending version is
// worth capturing once the current one finishes.
func TestCoordinatorIsSingleFlightAndLatestWins(t *testing.T) {
	release := make(chan struct{})
	builder := &fakeReportViewBuilder{started: make(chan int, 8), release: release}
	coordinator := NewReportViewCoordinator(builder, "http://client/", 2*time.Second)
	defer coordinator.Close()

	coordinator.Request(1, "dirty_blocks", "<html></html>", nil)
	waitFor(t, "capture 1 to start", func() bool { return len(builder.builtVersions()) == 1 })

	coordinator.Request(2, "dirty_blocks", "<html></html>", nil)
	coordinator.Request(3, "dirty_blocks", "<html></html>", nil)
	time.Sleep(50 * time.Millisecond)
	if versions := builder.builtVersions(); len(versions) != 1 {
		t.Fatalf("a second capture started while the first was in flight: %v", versions)
	}

	close(release)
	waitFor(t, "the newest view", func() bool {
		view := coordinator.Current()
		return view != nil && view.ReportVersion == 3
	})
	// 2 was superseded by 3 before it could run: capturing it would be wasted work.
	if versions := builder.builtVersions(); len(versions) != 2 || versions[1] != 3 {
		t.Fatalf("expected only versions [1 3], got %v", versions)
	}
}

func TestCoordinatorCloseStopsScheduling(t *testing.T) {
	builder := &fakeReportViewBuilder{}
	coordinator := NewReportViewCoordinator(builder, "http://client/", time.Second)
	coordinator.Request(1, "dirty_blocks", "<html></html>", nil)
	waitFor(t, "the first view", func() bool { return coordinator.Current() != nil })
	coordinator.Close()

	coordinator.Request(9, "dirty_blocks", "<html></html>", nil)
	time.Sleep(50 * time.Millisecond)
	if view := coordinator.Current(); view == nil || view.ReportVersion != 1 {
		t.Fatalf("a closed coordinator kept capturing: %+v", view)
	}
}

func TestCoordinatorHandlesMissingBuilder(t *testing.T) {
	coordinator := NewReportViewCoordinator(nil, "http://client/", time.Second)
	coordinator.Request(1, "dirty_blocks", "<html></html>", nil)
	if view := coordinator.Current(); view != nil {
		t.Fatalf("expected no view without a builder, got %+v", view)
	}
	coordinator.Close()
	var nilCoordinator *ReportViewCoordinator
	nilCoordinator.Request(1, "", "", nil)
	if view := nilCoordinator.Current(); view != nil {
		t.Fatal("nil coordinator must be safe to query")
	}
	nilCoordinator.Close()
}

// TestReportViewCoordinatorIsConfigGated: an unconfigured deployment must stay
// byte-identical to the text-only runtime. A coordinator that exists but always
// fails would inject a useless "capture failed" fact on every single request.
func TestReportViewCoordinatorIsConfigGated(t *testing.T) {
	previous := config.Cfg
	defer func() { config.Cfg = previous }()

	config.Cfg = &config.Config{}
	if coordinator := newSessionReportViewCoordinator(); coordinator != nil {
		coordinator.Close()
		t.Fatal("capture must stay disabled without configuration")
	}

	config.Cfg = &config.Config{RenderExecutorURL: "http://render-executor:8082", ProxyToken: "token"}
	if coordinator := newSessionReportViewCoordinator(); coordinator != nil {
		coordinator.Close()
		t.Fatal("capture must stay disabled without an asset origin")
	}

	config.Cfg = &config.Config{
		RenderExecutorURL:  "http://render-executor:8082",
		RenderAssetBaseURL: "http://client:8080/",
		ProxyToken:         "token",
	}
	coordinator := newSessionReportViewCoordinator()
	if coordinator == nil {
		t.Fatal("capture should be enabled once fully configured")
	}
	coordinator.Close()
}

// A nil coordinator must not panic the hook path when capture is disabled.
func TestSessionWithoutCaptureCoordinatorIsInert(t *testing.T) {
	session := &Session{}
	if session.ReportViewCoordinator() != nil {
		t.Fatal("expected no coordinator on a bare session")
	}
	session.CloseReportView()
}
