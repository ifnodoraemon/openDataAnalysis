package domain

import (
	"encoding/json"
	"time"
)

type Report struct {
	ID                  string
	RunID               string
	WorkspaceID         string
	Title               string
	Author              string
	HTMLStorageProvider string
	HTMLBucket          string
	HTMLStorageKey      string
	SnapshotJSON        string
	CreatedAt           time.Time
}

type ReportSnapshot struct {
	Version       string                `json:"version"`
	GeneratedAt   time.Time             `json:"generatedAt"`
	Title         string                `json:"title"`
	Author        string                `json:"author,omitempty"`
	NeedsFinalize bool                  `json:"needsFinalize,omitempty"`
	Layout        ReportSnapshotLayout  `json:"layout,omitempty"`
	Blocks        []ReportSnapshotBlock `json:"blocks,omitempty"`
	Charts        []ReportSnapshotChart `json:"charts"`
	Results       json.RawMessage       `json:"results,omitempty"`
	Artifacts     json.RawMessage       `json:"artifacts,omitempty"`
}

type ReportSnapshotLayout struct {
	CustomCSS string `json:"customCss,omitempty"`
	BodyClass string `json:"bodyClass,omitempty"`
}

type ReportSnapshotBlock struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Title   string          `json:"title,omitempty"`
	Content string          `json:"content,omitempty"`
	ChartID string          `json:"chartId,omitempty"`
	Sources json.RawMessage `json:"sources,omitempty"`
}

type ReportSnapshotChart struct {
	ID      string          `json:"id"`
	Option  json.RawMessage `json:"option"`
	Width   string          `json:"width,omitempty"`
	Height  string          `json:"height,omitempty"`
	Sources json.RawMessage `json:"sources,omitempty"`
}
