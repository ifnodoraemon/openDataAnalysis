package session

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/render"
	"github.com/prometheus/client_golang/prometheus"
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
	// The stamp is a monotonic capture generation; the request carried report
	// version 3, which is what the builder (and its slice cache) keys on.
	if version := coordinator.Current().ReportVersion; version != 1 {
		t.Fatalf("expected the first capture generation to be 1, got %d", version)
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

	// The switch is the gate: a fully configured executor must not enable capture.
	config.Cfg = &config.Config{
		RenderExecutorURL:     "http://render-executor:8082",
		RenderAssetBaseURL:    "http://client:8080/",
		ProxyToken:            "token",
		ReportViewCaptureMode: config.CaptureModeOff,
	}
	if coordinator := newSessionReportViewCoordinator(); coordinator != nil {
		coordinator.Close()
		t.Fatal("capture must stay disabled when the switch is off")
	}

	config.Cfg = &config.Config{
		RenderExecutorURL:     "http://render-executor:8082",
		ProxyToken:            "token",
		ReportViewCaptureMode: config.CaptureModeImages,
	}
	if coordinator := newSessionReportViewCoordinator(); coordinator != nil {
		coordinator.Close()
		t.Fatal("capture must stay disabled without an asset origin")
	}

	// facts mode is a valid, lighter configuration.
	config.Cfg = &config.Config{
		RenderExecutorURL:     "http://render-executor:8082",
		RenderAssetBaseURL:    "http://client:8080/",
		ProxyToken:            "token",
		ReportViewCaptureMode: config.CaptureModeFacts,
	}
	factsCoordinator := newSessionReportViewCoordinator()
	if factsCoordinator == nil {
		t.Fatal("facts mode should be enabled once fully configured")
	}
	factsCoordinator.Close()

	config.Cfg = &config.Config{
		RenderExecutorURL:     "http://render-executor:8082",
		RenderAssetBaseURL:    "http://client:8080/",
		ProxyToken:            "token",
		ReportViewCaptureMode: config.CaptureModeImages,
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

// publish installs a view as if a capture had completed, for delivery tests.
func publish(coordinator *ReportViewCoordinator, view *agent.ReportView) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.view = view
	coordinator.readyVersion = view.ReportVersion
	// A real capture also advances the publish sequence and the "last requested
	// report version" marker; without that the next request looks stale.
	coordinator.publishSequence = view.ReportVersion
	coordinator.lastRequestedReportVersion = view.ReportVersion
}

func viewWithHashes(t *testing.T, version int, hashes map[string]string) *agent.ReportView {
	t.Helper()
	view := &agent.ReportView{
		ReportVersion: version,
		CapturedAt:    "dirty_blocks",
		ViewportWidth: 1200,
		ViewportDPR:   1,
		Facts:         json.RawMessage(`{"ok":true}`),
	}
	blockIDs := make([]string, 0, len(hashes))
	for blockID := range hashes {
		blockIDs = append(blockIDs, blockID)
	}
	sort.Strings(blockIDs)
	for _, blockID := range blockIDs {
		view.Slices = append(view.Slices, agent.ReportViewSlice{
			BlockID:     blockID,
			Kind:        "markdown",
			Part:        "1/1",
			Width:       1200,
			Height:      960,
			PNG:         []byte{0x89, 'P'},
			ContentHash: hashes[blockID],
		})
	}
	return view
}

func sliceBlockIDs(view *agent.ReportView) []string {
	if view == nil {
		return nil
	}
	ids := make([]string, 0, len(view.Slices))
	for _, slice := range view.Slices {
		ids = append(ids, slice.BlockID)
	}
	return ids
}

// TestForRequestSendsFullViewFirstThenOnlyDirtyBlocks is the cost contract of the
// whole feature: unchanged images must not be re-sent every turn.
func TestForRequestSendsFullViewFirstThenOnlyDirtyBlocks(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	defer coordinator.Close()

	publish(coordinator, viewWithHashes(t, 1, map[string]string{"b1": "h1", "b2": "h2", "b3": "h3"}))
	first := coordinator.ForRequest()
	if len(first.Slices) != 3 {
		t.Fatalf("the first delivery must be the full view, got %d slices", len(first.Slices))
	}
	if len(first.UnchangedBlocks) != 0 {
		t.Fatalf("the first delivery cannot have unchanged blocks: %v", first.UnchangedBlocks)
	}

	// Same version again: the model already saw it, so nothing is sent at all.
	if again := coordinator.ForRequest(); again != nil {
		t.Fatalf("an unchanged version must not be re-delivered, got %d slices", len(again.Slices))
	}

	// Only b2 changed.
	publish(coordinator, viewWithHashes(t, 2, map[string]string{"b1": "h1", "b2": "h2-changed", "b3": "h3"}))
	dirty := coordinator.ForRequest()
	if ids := sliceBlockIDs(dirty); len(ids) != 1 || ids[0] != "b2" {
		t.Fatalf("expected only b2 to be sent, got %v", ids)
	}
	if len(dirty.UnchangedBlocks) != 2 || dirty.UnchangedBlocks[0] != "b1" || dirty.UnchangedBlocks[1] != "b3" {
		t.Fatalf("unchanged blocks must be named, got %v", dirty.UnchangedBlocks)
	}
	if dirty.ReportVersion != 2 {
		t.Fatalf("dirty view lost its version stamp: %d", dirty.ReportVersion)
	}

	// A layout change invalidates every block: the delivery is the full view again.
	publish(coordinator, viewWithHashes(t, 3, map[string]string{"b1": "h1x", "b2": "h2x", "b3": "h3x"}))
	full := coordinator.ForRequest()
	if len(full.Slices) != 3 {
		t.Fatalf("expected all blocks after a global change, got %d", len(full.Slices))
	}
}

// TestForRequestFallsBackToFullWithoutHashes: a capture without block hashes
// cannot prove a block is unchanged, so the full view is the only safe answer.
func TestForRequestFallsBackToFullWithoutHashes(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	defer coordinator.Close()

	coordinator.mu.Lock()
	coordinator.view = viewWithHashes(t, 1, map[string]string{"b1": "", "b2": ""})
	coordinator.readyVersion = 1
	coordinator.mu.Unlock()
	if view := coordinator.ForRequest(); len(view.Slices) != 2 {
		t.Fatalf("expected a full first delivery, got %d", len(view.Slices))
	}

	publish(coordinator, viewWithHashes(t, 2, map[string]string{"b1": "", "b2": ""}))
	if view := coordinator.ForRequest(); len(view.Slices) != 2 {
		t.Fatalf("without hashes every block is dirty, got %d", len(view.Slices))
	}
}

// TestForRequestStillDeliversFactsOnlyView: a failed capture advances the version
// but has no images; the model must still learn that, exactly once.
func TestForRequestStillDeliversFactsOnlyView(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	defer coordinator.Close()

	failed := &agent.ReportView{ReportVersion: 1, Facts: json.RawMessage(`{"ok":false,"reason":"capture_failed"}`)}
	publish(coordinator, failed)
	view := coordinator.ForRequest()
	if view == nil || len(view.Slices) != 0 {
		t.Fatalf("expected a facts-only delivery, got %+v", view)
	}
	if blocks := view.RuntimeContextBlocks(); len(blocks) != 1 || len(blocks[0].Parts) != 0 {
		t.Fatalf("facts-only view should render one image-free block, got %+v", blocks)
	}
	if again := coordinator.ForRequest(); again != nil {
		t.Fatal("the same failure must not be delivered twice")
	}
}

func TestForRequestAfterCloseSendsNothing(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	publish(coordinator, viewWithHashes(t, 1, map[string]string{"b1": "h1"}))
	coordinator.Close()
	if view := coordinator.ForRequest(); view != nil {
		t.Fatalf("a closed coordinator must not deliver views, got %+v", view)
	}
}

func TestRequestManualForcesRedeliveryOfSameVersion(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	defer coordinator.Close()

	publish(coordinator, viewWithHashes(t, 1, map[string]string{"b1": "h1", "b2": "h2"}))
	if view := coordinator.ForRequest(); len(view.Slices) != 2 {
		t.Fatalf("expected the full first delivery, got %d", len(view.Slices))
	}
	if again := coordinator.ForRequest(); again != nil {
		t.Fatal("the same version must not be delivered twice on its own")
	}

	// The user asked to look at the report: same version, but deliver it again.
	coordinator.RequestManual(1, "<html></html>", map[string]string{"b1": "h1", "b2": "h2"})
	manual := coordinator.ForRequest()
	if manual == nil || len(manual.Slices) != 2 {
		t.Fatalf("a manual request must re-deliver the full view, got %+v", manual)
	}
}

func TestRequestManualCapturesWhenViewIsStale(t *testing.T) {
	builder := &fakeReportViewBuilder{}
	coordinator := NewReportViewCoordinator(builder, "http://client/", time.Second)
	defer coordinator.Close()

	publish(coordinator, viewWithHashes(t, 1, map[string]string{"b1": "h1"}))
	coordinator.ForRequest()

	coordinator.RequestManual(2, "<html></html>", map[string]string{"b1": "h2"})
	waitFor(t, "a capture for version 2", func() bool {
		return len(builder.builtVersions()) == 1 && builder.builtVersions()[0] == 2
	})
	waitFor(t, "the version 2 view", func() bool {
		view := coordinator.Current()
		return view != nil && view.ReportVersion == 2
	})
}

type blockingBuilder struct {
	canceled bool
}

func (b *blockingBuilder) BuildView(ctx context.Context, request render.BuildRequest) *agent.ReportView {
	if err := ctx.Err(); err != nil {
		b.canceled = true
		return nil
	}
	return &agent.ReportView{ReportVersion: request.ReportVersion, Facts: json.RawMessage(`{"ok":true}`)}
}

// hashedViewBuilder returns a view carrying one slice whose hash tracks the
// version, so delivery filtering can be exercised without a browser.
type hashedViewBuilder struct{}

func (hashedViewBuilder) BuildView(ctx context.Context, request render.BuildRequest) *agent.ReportView {
	return &agent.ReportView{
		ReportVersion: request.ReportVersion,
		CapturedAt:    request.CapturedAt,
		Facts:         json.RawMessage(`{"ok":true}`),
		Slices: []agent.ReportViewSlice{{
			BlockID:     "b1",
			Kind:        "markdown",
			Part:        "1/1",
			Width:       1200,
			Height:      900,
			PNG:         []byte{0x89},
			ContentHash: "h" + strconv.Itoa(request.ReportVersion),
		}},
	}
}

func TestCaptureNowPublishesAndForcesDelivery(t *testing.T) {
	builder := hashedViewBuilder{}
	coordinator := NewReportViewCoordinator(builder, "http://client/", time.Second)
	defer coordinator.Close()

	view := coordinator.CaptureNow(context.Background(), 4, "finalize_precheck", "<html></html>", map[string]string{"b1": "h4"})
	if view == nil || view.ReportVersion <= 0 {
		t.Fatalf("expected a published view, got %+v", view)
	}
	if current := coordinator.Current(); current == nil || current.ReportVersion != view.ReportVersion {
		t.Fatalf("the capture should have been published, got %+v", current)
	}
	delivered := coordinator.ForRequest()
	if delivered == nil || len(delivered.Slices) != 1 {
		t.Fatalf("the finalize capture must be force-delivered, got %+v", delivered)
	}
}

func TestCaptureNowHonorsContextCancellation(t *testing.T) {
	builder := &blockingBuilder{}
	coordinator := NewReportViewCoordinator(builder, "http://client/", time.Second)
	defer coordinator.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if view := coordinator.CaptureNow(ctx, 6, "finalize_precheck", "<html></html>", nil); view != nil {
		t.Fatalf("a canceled capture must not produce a view, got %+v", view)
	}
	if !builder.canceled {
		t.Fatal("the builder should observe the canceled context")
	}
	if current := coordinator.Current(); current != nil {
		t.Fatalf("a failed capture must not publish, got %+v", current)
	}
}

func deliveryMetric(t *testing.T, kind string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "oda_report_view_delivery_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, pair := range metric.GetLabel() {
				if pair.GetName() == "kind" && pair.GetValue() == kind {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestForRequestRecordsDeliveryKind: full vs dirty vs none has to be visible in
// metrics, otherwise nobody can tell whether the incremental path is working.
func TestForRequestRecordsDeliveryKind(t *testing.T) {
	coordinator := NewReportViewCoordinator(&fakeReportViewBuilder{}, "http://client/", time.Second)
	defer coordinator.Close()

	noneBefore := deliveryMetric(t, "none")
	fullBefore := deliveryMetric(t, "full")
	dirtyBefore := deliveryMetric(t, "dirty")

	publish(coordinator, viewWithHashes(t, 1, map[string]string{"b1": "h1", "b2": "h2"}))
	coordinator.ForRequest() // first delivery: full
	if got := deliveryMetric(t, "full"); got != fullBefore+1 {
		t.Fatalf("full delivery not counted: %v -> %v", fullBefore, got)
	}

	coordinator.ForRequest() // nothing new
	if got := deliveryMetric(t, "none"); got != noneBefore+1 {
		t.Fatalf("empty delivery not counted: %v -> %v", noneBefore, got)
	}

	publish(coordinator, viewWithHashes(t, 2, map[string]string{"b1": "h1", "b2": "h2-changed"}))
	coordinator.ForRequest() // one block changed: dirty
	if got := deliveryMetric(t, "dirty"); got != dirtyBefore+1 {
		t.Fatalf("dirty delivery not counted: %v -> %v", dirtyBefore, got)
	}
}
