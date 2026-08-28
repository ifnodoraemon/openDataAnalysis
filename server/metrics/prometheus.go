package metrics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

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
}

// Handler returns an http.Handler for the Prometheus /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("http.ResponseWriter does not implement http.Hijacker")
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Middleware records Prometheus HTTP request counts and durations.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()
		path := r.URL.Path

		HTTPRequestsTotal.WithLabelValues(strconv.Itoa(rec.statusCode), r.Method, path).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(duration)
	})
}
