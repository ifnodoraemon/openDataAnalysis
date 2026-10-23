package metrics

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "oda_http_requests_total",
			Help: "Total number of HTTP requests processed by the OpenDataAnalysis server.",
		},
		[]string{"code", "method", "path"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "oda_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	ActiveEventStreamConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "oda_active_event_stream_connections",
			Help: "Number of currently active SSE client connections.",
		},
	)

	PythonExecutionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "oda_python_executions_total",
			Help: "Total number of Python code executions dispatched to executor.",
		},
		[]string{"status"},
	)
	AgentRunsTotal             = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_agent_runs_total", Help: "Agent runs by terminal event."}, []string{"status"})
	ToolCallsTotal             = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_tool_calls_total", Help: "Tool calls by tool name and result."}, []string{"tool", "status"})
	ToolCallDuration           = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "oda_tool_call_duration_seconds", Help: "Tool execution latency.", Buckets: prometheus.DefBuckets}, []string{"tool"})
	AnalysisResultsTotal       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_analysis_results_total", Help: "Structured analysis results recorded by tool."}, []string{"tool"})
	ReportFinalizeTotal        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_report_finalize_total", Help: "Report finalize attempts by result."}, []string{"status"})
	SemanticConfirmationsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_semantic_confirmations_total", Help: "Semantic confirmations by scope and provenance."}, []string{"scope", "provenance"})

	// Report view capture: the only way to answer "which reports keep failing to
	// render" without grepping container logs.
	ReportCaptureTotal    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_report_capture_total", Help: "Report view capture attempts by result."}, []string{"result"})
	ReportCaptureFailure  = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_report_capture_failures_total", Help: "Report view capture failures by reason."}, []string{"reason"})
	ReportCaptureDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "oda_report_capture_duration_seconds",
		Help:    "Wall time of one capture, including render settlement and cropping.",
		Buckets: []float64{0.5, 1, 2, 3, 5, 8, 12, 20, 30},
	}, nil)
	ReportCaptureSlices = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "oda_report_capture_slices",
		Help:    "Slice count per successful capture (0 in facts-only mode).",
		Buckets: []float64{0, 1, 2, 4, 8, 16, 32, 64},
	}, nil)
	ReportViewDelivery = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "oda_report_view_delivery_total", Help: "Report view deliveries by kind."}, []string{"kind"})
)

func init() {
	prometheus.MustRegister(HTTPRequestsTotal)
	prometheus.MustRegister(HTTPRequestDuration)
	prometheus.MustRegister(ActiveEventStreamConnections)
	prometheus.MustRegister(PythonExecutionsTotal)
	prometheus.MustRegister(AgentRunsTotal)
	prometheus.MustRegister(ToolCallsTotal)
	prometheus.MustRegister(ToolCallDuration)
	prometheus.MustRegister(AnalysisResultsTotal)
	prometheus.MustRegister(ReportFinalizeTotal)
	prometheus.MustRegister(SemanticConfirmationsTotal)
	prometheus.MustRegister(ReportCaptureTotal)
	prometheus.MustRegister(ReportCaptureFailure)
	prometheus.MustRegister(ReportCaptureDuration)
	prometheus.MustRegister(ReportCaptureSlices)
	prometheus.MustRegister(ReportViewDelivery)
}

// Handler returns the unauthenticated Prometheus handler. Prefer
// ProtectedHandler, which gates scraping behind a bearer token.
func Handler() http.Handler {
	return promhttp.Handler()
}

// ProtectedHandler returns the Prometheus handler, gated by a constant-time
// bearer-token check. An empty token keeps the endpoint unauthenticated and
// logs a warning; production readiness validation rejects that combination.
func ProtectedHandler(token string) http.Handler {
	scrape := promhttp.Handler()
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		log.Printf("metrics: METRICS_EXPOSE is enabled without METRICS_AUTH_TOKEN; the endpoint is unauthenticated")
		return scrape
	}
	expected := []byte("Bearer " + trimmed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := []byte(strings.TrimSpace(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(provided, expected) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			if _, err := w.Write([]byte(`{"error":"metrics 访问未授权"}`)); err != nil {
				log.Printf("metrics: write unauthorized response: %v", err)
			}
			return
		}
		scrape.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer, so
// long-lived handlers (the SSE stream clears its write deadline) keep working
// through this middleware.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// routeLabel returns a bounded-cardinality path label. Registered route
// patterns (e.g. "/api/sessions/{sessionID}") collapse URL parameters;
// unmatched requests collapse to a single "unmatched" label so arbitrary
// request paths cannot grow the label set.
func routeLabel(r *http.Request) string {
	if routeCtx := chi.RouteContext(r.Context()); routeCtx != nil {
		if pattern := routeCtx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}

// Middleware records Prometheus HTTP request counts and durations.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()
		path := routeLabel(r)

		HTTPRequestsTotal.WithLabelValues(strconv.Itoa(rec.statusCode), r.Method, path).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}
