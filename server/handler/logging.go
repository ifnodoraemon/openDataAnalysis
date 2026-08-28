package handler

import (
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

func RequestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		reqID := middleware.GetReqID(r.Context())
		if reqID != "" {
			w.Header().Set("X-Request-ID", reqID)
		}

		next.ServeHTTP(ww, r)

		if shouldSkipAccessLog(r.URL.Path) {
			return
		}

		log.Printf(
			"http req_id=%s method=%s path=%s status=%d bytes=%d duration_ms=%d remote=%s",
			reqID,
			r.Method,
			r.URL.Path,
			ww.Status(),
			ww.BytesWritten(),
			time.Since(start).Milliseconds(),
			clientIP(r),
		)
	})
}

func MaxBodySizeMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.ContentLength > maxBytes {
				http.Error(w, "请求体过大", http.StatusRequestEntityTooLarge)
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func shouldSkipAccessLog(path string) bool {
	return path == "/api/health"
}

func writeHandlerError(w http.ResponseWriter, status int, message string, err error) {
	if err != nil {
		log.Printf("handler error status=%d message=%q err=%v", status, message, err)
	}
	http.Error(w, message, status)
}

// clientIP logs the resolved client IP; proxy headers only count for trusted peers.
func clientIP(r *http.Request) string {
	return getClientIP(r)
}
