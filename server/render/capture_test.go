package render

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 1, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buffer.Bytes()
}

func TestCaptureSendsContractAndDecodesResponse(t *testing.T) {
	pngBytes := onePixelPNG(t)
	var seen struct {
		path      string
		token     string
		method    string
		body      captureWireRequest
		contentTy string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.path = r.URL.Path
		seen.method = r.Method
		seen.token = r.Header.Get("X-Proxy-Token")
		seen.contentTy = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&seen.body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(captureWireResponse{
			PNGBase64: base64.StdEncoding.EncodeToString(pngBytes),
			Rects: Layout{
				Scroll: Size{W: 1200, H: 2400},
				Blocks: []BlockRect{{BlockID: "b1", Kind: "markdown", Y: 0, H: 2400, W: 1200}},
			},
			Facts:     json.RawMessage(`{"charts_mounted":2}`),
			SettleMS:  800,
			CaptureMS: 3400,
		})
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{BaseURL: server.URL, ProxyToken: "token-1", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	response, err := client.Capture(context.Background(), CaptureRequest{
		HTML:          "<html></html>",
		AssetBaseURL:  "http://client/",
		ViewportWidth: 1200,
	})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if seen.method != http.MethodPost || seen.path != "/capture" {
		t.Fatalf("unexpected request %s %s", seen.method, seen.path)
	}
	if seen.token != "token-1" {
		t.Fatalf("proxy token not sent, got %q", seen.token)
	}
	if !strings.HasPrefix(seen.contentTy, "application/json") {
		t.Fatalf("unexpected content type %q", seen.contentTy)
	}
	if seen.body.HTML != "<html></html>" || seen.body.AssetBaseURL != "http://client/" {
		t.Fatalf("request body lost fields: %+v", seen.body)
	}
	if seen.body.Viewport.Width != 1200 || seen.body.Viewport.DPR != 1 {
		t.Fatalf("viewport not defaulted: %+v", seen.body.Viewport)
	}
	if !bytes.Equal(response.PNG, pngBytes) {
		t.Fatal("PNG was not decoded intact")
	}
	if response.Layout.Scroll.H != 2400 || len(response.Layout.Blocks) != 1 {
		t.Fatalf("layout was not decoded: %+v", response.Layout)
	}
	if !strings.Contains(string(response.Facts), "charts_mounted") {
		t.Fatalf("facts were not passed through: %s", response.Facts)
	}
}

func TestCaptureSurfacesAuthAndServerErrors(t *testing.T) {
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer unauthorized.Close()
	client, err := NewClient(ClientConfig{BaseURL: unauthorized.URL, ProxyToken: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Capture(context.Background(), CaptureRequest{HTML: "<html></html>"}); err == nil ||
		!strings.Contains(err.Error(), "proxy token") {
		t.Fatalf("expected a token error, got %v", err)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"chromium crashed"}`))
	}))
	defer failing.Close()
	client, err = NewClient(ClientConfig{BaseURL: failing.URL, ProxyToken: "t"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.Capture(context.Background(), CaptureRequest{HTML: "<html></html>"}); err == nil ||
		!strings.Contains(err.Error(), "chromium crashed") {
		t.Fatalf("expected the executor error to surface, got %v", err)
	}
}

func TestNewClientValidatesConfig(t *testing.T) {
	if _, err := NewClient(ClientConfig{BaseURL: "", ProxyToken: "t"}); err == nil {
		t.Fatal("empty base URL must be rejected")
	}
	if _, err := NewClient(ClientConfig{BaseURL: "http://x", ProxyToken: "  "}); err == nil {
		t.Fatal("empty token must be rejected")
	}
	if _, err := NewClient(ClientConfig{BaseURL: "http://x", ProxyToken: " padded "}); err == nil {
		t.Fatal("padded token must be rejected")
	}
}

func TestHealthReportsUnreachable(t *testing.T) {
	client, err := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1", ProxyToken: "t", Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := client.Health(context.Background()); err == nil {
		t.Fatal("expected an error for an unreachable executor")
	}
}
