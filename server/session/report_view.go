package session

import (
	"context"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
	"github.com/ifnodoraemon/openDataAnalysis/render"
)

// ReportViewCoordinator owns the "current rendered view" of one session.
//
// The engine asks for a view once per request and must never wait for one: a
// capture costs seconds, and a report edit can arrive while the previous capture
// is still running. So requests are fire-and-forget with two rules:
//
//   - single-flight: one capture at a time, so a burst of edits cannot pile up
//     browsers;
//   - latest-wins: a request that arrives mid-capture replaces the queued one,
//     because an older view is worthless once a newer version exists.
type ReportViewCoordinator struct {
	builder      ReportViewBuilder
	assetBaseURL string
	timeout      time.Duration

	mu           sync.Mutex
	view         *agent.ReportView
	readyVersion int

	// report versions are per-process counters: a session restored from a
	// snapshot has MutationVersion 0 until something mutates it. So the version
	// used to skip duplicate work and the version stamped on the view are kept
	// apart, and the published one is always a monotonic capture generation.
	lastRequestedReportVersion int
	publishSequence            int

	// deliveredVersion and deliveredHashes describe what the model has already
	// been shown. The view never enters history, so "already shown" is the only
	// way to avoid re-sending unchanged images on every single turn.
	deliveredVersion int
	deliveredHashes  map[string]string

	pending *reportViewRequest
	running bool
	closed  bool
	wg      sync.WaitGroup
}

// ReportViewBuilder is the render.Builder contract, kept as an interface so the
// coordinator can be tested without a browser.
type ReportViewBuilder interface {
	BuildView(ctx context.Context, request render.BuildRequest) *agent.ReportView
}

type reportViewRequest struct {
	// reportVersion is the report's own version, used only to skip work that is
	// already done. It may be 0 for a restored session.
	reportVersion int
	// publishVersion is the monotonic capture generation stamped on the view.
	publishVersion int
	capturedAt     string
	html           string
	hashes         map[string]string
}

func NewReportViewCoordinator(builder ReportViewBuilder, assetBaseURL string, timeout time.Duration) *ReportViewCoordinator {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &ReportViewCoordinator{
		builder:                    builder,
		assetBaseURL:               assetBaseURL,
		timeout:                    timeout,
		lastRequestedReportVersion: -1,
	}
}

// Request schedules a capture for the given report version. It never blocks.
//
// A report version of 0 means the session has not mutated since it was restored
// from a snapshot; that is a normal state and must still be capturable.
func (c *ReportViewCoordinator) Request(version int, capturedAt, html string, hashes map[string]string) {
	if c == nil || c.builder == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	// Same report version means the pixels cannot have changed; skip the work.
	// A report version of 0 is a normal state (restored session, no mutation yet)
	// and is capturable once, which is why the sentinel starts at -1.
	if version <= c.lastRequestedReportVersion {
		return
	}
	c.lastRequestedReportVersion = version
	c.publishSequence++
	request := reportViewRequest{
		reportVersion:  version,
		publishVersion: c.publishSequence,
		capturedAt:     capturedAt,
		html:           html,
		hashes:         hashes,
	}
	if c.running {
		c.pending = &request
		return
	}
	c.pending = &request
	c.running = true
	c.wg.Add(1)
	go c.drain()
}

// Current returns the newest ready view without touching delivery state. Used by
// tests and diagnostics; the engine uses ForRequest.
func (c *ReportViewCoordinator) Current() *agent.ReportView {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.view
}

// ForRequest returns the view to inject into the next request and records it as
// delivered.
//
// Delivery state is what makes this affordable. The view is rebuilt per request
// and never stored in history, so the naive behaviour is to re-send every slice
// on every turn (~36k tokens for a 29-slice report). Instead:
//
//   - nothing new since the last delivered version -> nil, no images at all;
//   - first delivery of a session -> the full view;
//   - afterwards -> only slices whose block content hash changed, with the rest
//     named in unchanged_blocks so a partial view stays honest.
//
// Recording at return time is a deliberate trade: a request that fails after this
// point loses those images for the next turn too. Re-sending is the more
// expensive failure, and a stale-but-cheap view is recoverable.
func (c *ReportViewCoordinator) ForRequest() *agent.ReportView {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.view == nil {
		metrics.ReportViewDelivery.WithLabelValues("none").Inc()
		return nil
	}
	if c.view.ReportVersion <= c.deliveredVersion {
		metrics.ReportViewDelivery.WithLabelValues("none").Inc()
		return nil
	}
	view := c.dirtyOnlyLocked(c.view)
	c.deliveredVersion = c.view.ReportVersion
	c.deliveredHashes = hashesOfView(c.view)
	// Distinguishing full from dirty is how an operator can tell whether the
	// incremental path is actually working in production.
	switch {
	case len(view.Slices) == 0:
		metrics.ReportViewDelivery.WithLabelValues("facts").Inc()
	case len(view.Slices) == len(c.view.Slices):
		metrics.ReportViewDelivery.WithLabelValues("full").Inc()
	default:
		metrics.ReportViewDelivery.WithLabelValues("dirty").Inc()
	}
	return view
}

func hashesOfView(view *agent.ReportView) map[string]string {
	hashes := make(map[string]string, len(view.Slices))
	for _, slice := range view.Slices {
		hashes[slice.BlockID] = slice.ContentHash
	}
	return hashes
}

// dirtyOnlyLocked narrows a view to the blocks that changed since the last
// delivery. Without a delivery baseline (first view, or a capture without block
// hashes) the full view is the only correct answer.
func (c *ReportViewCoordinator) dirtyOnlyLocked(view *agent.ReportView) *agent.ReportView {
	if len(c.deliveredHashes) == 0 {
		return view
	}
	dirty := make([]agent.ReportViewSlice, 0, len(view.Slices))
	skipped := make(map[string]struct{}, len(view.Slices))
	for _, slice := range view.Slices {
		if slice.ContentHash != "" && slice.ContentHash == c.deliveredHashes[slice.BlockID] {
			skipped[slice.BlockID] = struct{}{}
			continue
		}
		dirty = append(dirty, slice)
	}
	unchanged := make([]string, 0, len(skipped))
	for blockID := range skipped {
		unchanged = append(unchanged, blockID)
	}
	sort.Strings(unchanged)
	return &agent.ReportView{
		ReportVersion:   view.ReportVersion,
		CapturedAt:      view.CapturedAt,
		ViewportWidth:   view.ViewportWidth,
		ViewportDPR:     view.ViewportDPR,
		Facts:           view.Facts,
		Slices:          dirty,
		NotCaptured:     view.NotCaptured,
		UnchangedBlocks: unchanged,
	}
}

// Close stops scheduling and waits for the in-flight capture, bounded by the
// per-capture timeout.
func (c *ReportViewCoordinator) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	c.pending = nil
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *ReportViewCoordinator) drain() {
	defer c.wg.Done()
	for {
		c.mu.Lock()
		request := c.pending
		c.pending = nil
		if request == nil || c.closed {
			c.running = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		view := c.capture(*request)

		c.mu.Lock()
		if view != nil && !c.closed && request.publishVersion > c.readyVersion {
			c.view = view
			c.readyVersion = request.publishVersion
		}
		c.mu.Unlock()
	}
}

// capture runs one build under its own timeout, so a stuck browser cannot keep
// the session alive.
func (c *ReportViewCoordinator) capture(request reportViewRequest) *agent.ReportView {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	view := c.builder.BuildView(ctx, render.BuildRequest{
		HTML:         request.html,
		AssetBaseURL: c.assetBaseURL,
		// The builder keys its slice cache on this, so it must be the report
		// version: a per-capture sequence would miss the cache every single time.
		ReportVersion: request.reportVersion,
		CapturedAt:    request.capturedAt,
		BlockHashes:   request.hashes,
	})
	if view == nil {
		return nil
	}
	// Publish under a monotonic generation: the model needs a stamp that always
	// moves forward, while the report version can sit at 0 for a restored session.
	view.ReportVersion = request.publishVersion
	return view
}

// newSessionReportViewCoordinator builds a coordinator from configuration, or
// returns nil when capture is not configured. Returning nil (rather than a
// coordinator that always fails) is what keeps an unconfigured deployment
// byte-identical to the text-only runtime: no provider, no view, no extra facts.
func newSessionReportViewCoordinator() *ReportViewCoordinator {
	if config.Cfg == nil {
		return nil
	}
	// The switch is the whole gate: configuring an executor must not silently
	// enable a feature that costs seconds and tens of thousands of tokens per
	// report edit, and operators need a way to degrade without unconfiguring.
	mode := config.Cfg.ReportViewCaptureMode
	if mode == "" || mode == config.CaptureModeOff {
		return nil
	}
	if strings.TrimSpace(config.Cfg.RenderExecutorURL) == "" {
		log.Printf("report view capture is %q but RENDER_EXECUTOR_URL is empty; capture disabled", mode)
		return nil
	}
	if strings.TrimSpace(config.Cfg.RenderAssetBaseURL) == "" {
		log.Printf("report view capture is %q but RENDER_ASSET_BASE_URL is empty; capture disabled", mode)
		return nil
	}
	client, err := render.NewClient(render.ClientConfig{
		BaseURL:    config.Cfg.RenderExecutorURL,
		ProxyToken: config.Cfg.ProxyToken,
	})
	if err != nil {
		// A misconfigured capture service must not stop sessions from working.
		log.Printf("report view capture disabled: %v", err)
		return nil
	}
	var cache render.Cache = render.NewMemoryCache(256)
	builder := render.NewBuilder(client, cache, render.BuilderOptions{
		FactsOnly: mode == config.CaptureModeFacts,
	})
	return NewReportViewCoordinator(builder, config.Cfg.RenderAssetBaseURL, 20*time.Second)
}

// ReportViewCoordinator exposes the session's coordinator so the run hooks can
// schedule captures. It returns nil when capture is not configured.
func (s *Session) ReportViewCoordinator() *ReportViewCoordinator {
	if s == nil {
		return nil
	}
	return s.reportView
}

// CloseReportView stops scheduled captures when the session is detached.
func (s *Session) CloseReportView() {
	if s == nil {
		return
	}
	s.reportView.Close()
}

// RequestManual forces the next delivery to include the view even if the model
// has already seen this version: the user explicitly asked to look at it.
//
// A capture is scheduled only when the current view is older than the report;
// asking to see a fresh view must not cost a five-second render for nothing.
func (c *ReportViewCoordinator) RequestManual(version int, html string, hashes map[string]string) {
	if c == nil || c.builder == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.deliveredVersion = 0
	c.deliveredHashes = nil
	current := c.view

	// Capture only when the report moved on since the last capture; otherwise the
	// forced re-delivery above is enough.
	needsCapture := current == nil || version > c.lastRequestedReportVersion
	c.mu.Unlock()
	if needsCapture {
		c.Request(version, "manual_screen", html, hashes)
	}
	return
}

// CaptureNow runs one capture and waits for it.
//
// This is the only blocking capture path, used at the delivery boundary: the
// model should get one look at the finalized report before it writes its final
// answer, and by then nothing else will trigger a capture. Everywhere else stays
// asynchronous. The caller bounds it with its own context.
func (c *ReportViewCoordinator) CaptureNow(ctx context.Context, version int, capturedAt, html string, hashes map[string]string) *agent.ReportView {
	if c == nil || c.builder == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	if version > c.lastRequestedReportVersion {
		c.lastRequestedReportVersion = version
	}
	c.publishSequence++
	publishVersion := c.publishSequence
	c.mu.Unlock()
	view := c.builder.BuildView(ctx, render.BuildRequest{
		HTML:          html,
		AssetBaseURL:  c.assetBaseURL,
		ReportVersion: version,
		CapturedAt:    capturedAt,
		BlockHashes:   hashes,
	})
	if view == nil {
		return nil
	}
	view.ReportVersion = publishVersion
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return view
	}
	// Keep the freshest published view; equal versions still replace, because the
	// finalize step can change the title while leaving the block hashes alone.
	if view.ReportVersion >= c.readyVersion {
		c.view = view
		c.readyVersion = view.ReportVersion
	}
	// Force the next request to carry it in full.
	c.deliveredVersion = 0
	c.deliveredHashes = nil
	return view
}
