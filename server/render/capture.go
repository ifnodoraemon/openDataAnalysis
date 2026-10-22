package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the render-executor service.
type Client struct {
	baseURL    string
	proxyToken string
	httpClient *http.Client
}

type ClientConfig struct {
	BaseURL    string
	ProxyToken string
	// Timeout bounds one capture end to end. A capture that takes longer must
	// degrade to facts, never stall the agent step that asked for it.
	Timeout time.Duration
}

func NewClient(cfg ClientConfig) (*Client, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("render executor base URL is required")
	}
	if strings.HasSuffix(baseURL, "/") {
		baseURL = strings.TrimSuffix(baseURL, "/")
	}
	token := strings.TrimSpace(cfg.ProxyToken)
	if token == "" {
		return nil, fmt.Errorf("PROXY_TOKEN is not configured")
	}
	if token != cfg.ProxyToken {
		return nil, fmt.Errorf("PROXY_TOKEN must not contain leading or trailing whitespace")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	return &Client{
		baseURL:    baseURL,
		proxyToken: token,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

// CaptureRequest is one render of one report document.
type CaptureRequest struct {
	HTML string
	// AssetBaseURL is the origin the report's /assets paths resolve against. It is
	// also the only origin the executor will load subresources from.
	AssetBaseURL  string
	ViewportWidth int
	ViewportDPR   int
	TimeoutMS     int
}

// CaptureResponse is one rendered page: the screenshot, the geometry, and the
// facts observed while rendering.
type CaptureResponse struct {
	PNG       []byte
	Layout    Layout
	Facts     json.RawMessage
	SettleMS  int
	CaptureMS int
}

type captureWireRequest struct {
	HTML         string          `json:"html"`
	AssetBaseURL string          `json:"asset_base_url,omitempty"`
	Viewport     captureWireView `json:"viewport"`
	TimeoutMS    int             `json:"timeout_ms,omitempty"`
}

type captureWireView struct {
	Width int     `json:"width"`
	DPR   float64 `json:"dpr"`
}

type captureWireResponse struct {
	PNGBase64 string          `json:"png_base64"`
	Rects     Layout          `json:"rects"`
	Facts     json.RawMessage `json:"render_facts"`
	SettleMS  int             `json:"settle_ms"`
	CaptureMS int             `json:"capture_ms"`
	Error     string          `json:"error"`
}

func (c *Client) Capture(ctx context.Context, request CaptureRequest) (*CaptureResponse, error) {
	if strings.TrimSpace(request.HTML) == "" {
		return nil, fmt.Errorf("capture requires report HTML")
	}
	width := request.ViewportWidth
	if width <= 0 {
		width = 1200
	}
	dpr := request.ViewportDPR
	if dpr <= 0 {
		dpr = 1
	}
	payload, err := json.Marshal(captureWireRequest{
		HTML:         request.HTML,
		AssetBaseURL: request.AssetBaseURL,
		Viewport:     captureWireView{Width: width, DPR: float64(dpr)},
		TimeoutMS:    request.TimeoutMS,
	})
	if err != nil {
		return nil, fmt.Errorf("encode capture request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/capture", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build capture request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Proxy-Token", c.proxyToken)

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("capture call failed: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		detail := strings.TrimSpace(string(body))
		if response.StatusCode == http.StatusUnauthorized {
			return nil, fmt.Errorf("capture rejected the proxy token")
		}
		return nil, fmt.Errorf("capture failed: status=%d body=%s", response.StatusCode, detail)
	}

	// The screenshot dominates the payload; 64 MiB leaves room for a very long
	// report without allowing an unbounded read.
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read capture response: %w", err)
	}
	var wire captureWireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("decode capture response: %w", err)
	}
	if wire.Error != "" {
		return nil, fmt.Errorf("capture failed: %s", wire.Error)
	}
	pngBytes, err := base64.StdEncoding.DecodeString(wire.PNGBase64)
	if err != nil {
		return nil, fmt.Errorf("decode capture PNG: %w", err)
	}
	if len(pngBytes) == 0 {
		return nil, fmt.Errorf("capture returned no image")
	}
	return &CaptureResponse{
		PNG:       pngBytes,
		Layout:    wire.Rects,
		Facts:     wire.Facts,
		SettleMS:  wire.SettleMS,
		CaptureMS: wire.CaptureMS,
	}, nil
}

// Health checks whether the executor is reachable and has a browser.
func (c *Client) Health(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}
	request.Header.Set("X-Proxy-Token", c.proxyToken)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("render executor unreachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("render executor unhealthy: status=%d", response.StatusCode)
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payload); err != nil {
		return fmt.Errorf("decode health response: %w", err)
	}
	if !payload.OK {
		return errors.New("render executor reported not ok")
	}
	return nil
}
