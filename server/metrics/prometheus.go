package metrics

import (
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
			Help: "Total number of HTTP requests processed by OpenDataAnalysis server.",
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

	ActiveWSConnections = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "oda_active_websocket_connections",
			Help: "Number of currently active WebSocket client connections.",
		},
	)

	PythonExecutionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "oda_python_executions_total",
			Help: "Total number of Python code executions dispatched to executor.",
		},
		[]string{"status"},
	)
)

func init() {
	prometheus.MustRegister(HTTPRequestsTotal)
	prometheus.MustRegister(HTTPRequestDuration)
	prometheus.MustRegister(ActiveWSConnections)
	prometheus.MustRegister(PythonExecutionsTotal)
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
