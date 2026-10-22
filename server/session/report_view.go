package session

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
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
	pending      *reportViewRequest
	running      bool
	closed       bool
	wg           sync.WaitGroup
}

// ReportViewBuilder is the render.Builder contract, kept as an interface so the
// coordinator can be tested without a browser.
type ReportViewBuilder interface {
	BuildView(ctx context.Context, request render.BuildRequest) *agent.ReportView
}

type reportViewRequest struct {
	version    int
	capturedAt string
	html       string
	hashes     map[string]string
}

func NewReportViewCoordinator(builder ReportViewBuilder, assetBaseURL string, timeout time.Duration) *ReportViewCoordinator {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &ReportViewCoordinator{builder: builder, assetBaseURL: assetBaseURL, timeout: timeout}
}

// Request schedules a capture for the given report version. It never blocks.
func (c *ReportViewCoordinator) Request(version int, capturedAt, html string, hashes map[string]string) {
	if c == nil || c.builder == nil {
		return
	}
	if version <= 0 {
		return
	}
	request := reportViewRequest{version: version, capturedAt: capturedAt, html: html, hashes: hashes}

	c.mu.Lock()
	defer c.mu.Unlock()
	// A view at or beyond this version is already available; nothing to do.
	if c.closed || version <= c.readyVersion {
		return
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

// Current returns the newest ready view, or nil. Called by the engine on every
// request, so it only reads a pointer.
func (c *ReportViewCoordinator) Current() *agent.ReportView {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.view
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
		if view != nil && !c.closed && request.version > c.readyVersion {
			c.view = view
			c.readyVersion = request.version
		}
		c.mu.Unlock()
	}
}

// capture runs one build under its own timeout, so a stuck browser cannot keep
// the session alive.
func (c *ReportViewCoordinator) capture(request reportViewRequest) *agent.ReportView {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return c.builder.BuildView(ctx, render.BuildRequest{
		HTML:          request.html,
		AssetBaseURL:  c.assetBaseURL,
		ReportVersion: request.version,
		CapturedAt:    request.capturedAt,
		BlockHashes:   request.hashes,
	})
}

// newSessionReportViewCoordinator builds a coordinator from configuration, or
// returns nil when capture is not configured. Returning nil (rather than a
// coordinator that always fails) is what keeps an unconfigured deployment
// byte-identical to the text-only runtime: no provider, no view, no extra facts.
func newSessionReportViewCoordinator() *ReportViewCoordinator {
	if config.Cfg == nil {
		return nil
	}
	if strings.TrimSpace(config.Cfg.RenderExecutorURL) == "" {
		return nil
	}
	if strings.TrimSpace(config.Cfg.RenderAssetBaseURL) == "" {
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
	builder := render.NewBuilder(client, cache, render.BuilderOptions{})
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
