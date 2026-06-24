package handler

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ifnodoraemon/openDataAnalysis/auth"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/tools"
)

var safeFilenamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

func ProxyPythonFileHandler(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if filename == "" {
		http.Error(w, "missing filename parameter", http.StatusBadRequest)
		return
	}
	if !safeFilenamePattern.MatchString(filename) {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	if !authorizePythonFileAccess(r, filename) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	endpoint := configuredPythonMCPURL()
	proxyToken := configuredPythonProxyToken()
	if proxyToken == "" {
		http.Error(w, "file proxy service not configured", http.StatusServiceUnavailable)
		return
	}

	url := fmt.Sprintf("%s/files/%s", endpoint, filename)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("X-Proxy-Token", proxyToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "proxy request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	io.Copy(w, resp.Body)
}

func authorizePythonFileAccess(r *http.Request, filename string) bool {
	identity, ok := auth.FromContext(r.Context())
	if !ok || strings.TrimSpace(identity.UserID) == "" || strings.TrimSpace(identity.WorkspaceID) == "" {
		return false
	}
	if runRepo == nil || config.Cfg == nil || strings.TrimSpace(config.Cfg.AuthSecret) == "" {
		return false
	}

	query := r.URL.Query()
	sessionID := strings.TrimSpace(query.Get("session_id"))
	runID := strings.TrimSpace(query.Get("run_id"))
	sig := strings.TrimSpace(query.Get("sig"))
	if sessionID == "" || runID == "" || sig == "" {
		return false
	}

	run, err := runRepo.GetByID(r.Context(), runID)
	if err != nil || run == nil {
		return false
	}
	if run.SessionID != sessionID || run.WorkspaceID != identity.WorkspaceID || run.UserID != identity.UserID {
		return false
	}

	meta := tools.ExecutionMetadata{
		WorkspaceID: identity.WorkspaceID,
		SessionID:   sessionID,
		RunID:       runID,
	}
	return tools.VerifyPythonFileAccessSignature(filename, meta, config.Cfg.AuthSecret, sig)
}

func configuredPythonMCPURL() string {
	if config.Cfg != nil && strings.TrimSpace(config.Cfg.PythonMCPURL) != "" {
		return strings.TrimRight(strings.TrimSpace(config.Cfg.PythonMCPURL), "/")
	}
	if endpoint := strings.TrimSpace(os.Getenv("PYTHON_MCP_URL")); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	return "http://python-executor:8081"
}

func configuredPythonProxyToken() string {
	if config.Cfg != nil && strings.TrimSpace(config.Cfg.ProxyToken) != "" {
		return strings.TrimSpace(config.Cfg.ProxyToken)
	}
	return strings.TrimSpace(os.Getenv("PROXY_TOKEN"))
}
