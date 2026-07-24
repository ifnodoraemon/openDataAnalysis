package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/handler"
	"github.com/ifnodoraemon/openDataAnalysis/metrics"
)

func main() {
	config.Load()
	handler.Initialize()

	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(metrics.Middleware)
	r.Use(handler.RequestLoggingMiddleware)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   config.Cfg.AllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Proxy-Token"},
		AllowCredentials: true,
	}))

	r.Handle("/metrics", metrics.Handler())

	r.Group(func(authGroup chi.Router) {
		authGroup.Use(handler.IPRateLimitMiddleware(30, 10))
		authGroup.Post("/api/auth/login", handler.LoginHandler)
		authGroup.Post("/api/auth/register", handler.RegisterHandler)
		authGroup.Post("/api/auth/logout", handler.LogoutHandler)
	})

	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"ok"}`))
	})

	r.Group(func(protected chi.Router) {
		protected.Use(handler.AuthMiddleware)
		protected.Use(handler.UserRateLimitMiddleware(120, 30))

		protected.Get("/api/sse", handler.SSEHandler)

		protected.Group(func(upload chi.Router) {
			upload.Use(handler.MaxBodySizeMiddleware(100 << 20))
			upload.Post("/api/upload", handler.UploadHandler)
		})

		handler.RegisterReportExportRoutes(protected)

		protected.Group(func(api chi.Router) {
			api.Use(handler.MaxBodySizeMiddleware(1 << 20))
			api.Post("/api/sessions/{sessionID}/chat", handler.ChatHandler)
			api.Post("/api/runs/{runID}/cancel", handler.CancelRunHandler)
			api.Post("/api/runs/{runID}/input", handler.SubmitUserInputHandler)
			api.Post("/api/auth/switch-workspace", handler.SwitchWorkspaceHandler)
			api.Get("/api/bootstrap", handler.BootstrapHandler)
			api.Post("/api/sessions", handler.CreateSessionHandler)
			api.Get("/api/sessions", handler.ListSessionsHandler)
			api.Get("/api/sessions/{sessionID}", handler.GetSessionHandler)
			api.Put("/api/sessions/{sessionID}", handler.UpdateSessionHandler)
			api.Delete("/api/sessions/{sessionID}", handler.DeleteSessionHandler)
			api.Get("/api/runs", handler.ListRunsHandler)
			api.Get("/api/runs/{runID}", handler.GetRunHandler)
			api.Get("/api/runs/{runID}/report", handler.GetRunReportHandler)
			api.Get("/api/python-files/{filename}", handler.ProxyPythonFileHandler)

			api.Get("/api/sessions/{sessionID}/sources", handler.SessionSourcesHandler)
			api.Delete("/api/sessions/{sessionID}/sources/{sourceID}", handler.DeleteSessionSourceHandler)
			api.Get("/api/semantic-profiles/{profileID}", handler.SemanticProfileDetailHandler)
			api.Post("/api/semantic-profiles/{profileID}/confirm", handler.ConfirmProfileHandler)
			api.Post("/api/data-sources", handler.CreateDataSourceHandler)
			api.Get("/api/data-sources", handler.ListDataSourcesHandler)
			api.Get("/api/data-source-types", handler.ListDataSourceTypesHandler)
			api.Put("/api/data-sources/{sourceID}", handler.UpdateDataSourceHandler)
			api.Delete("/api/data-sources/{sourceID}", handler.DeleteDataSourceHandler)
			api.Post("/api/data-sources/{sourceID}/test", handler.TestDataSourceHandler)
			api.Get("/api/data-sources/{sourceID}/catalog", handler.CatalogDataSourceHandler)
			api.Post("/api/data-sources/{sourceID}/import", handler.ImportDataSourceHandler)
		})
	})

	port := config.Cfg.ServerPort
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("server started addr=0.0.0.0:%s ws_path=/ws model=%s endpoint=%s llm_debug=%t llm_debug_dir=%s",
			port,
			config.Cfg.LLMModel,
			config.Cfg.LLMAPIEndpoint,
			config.Cfg.LLMDebug,
			config.Cfg.LLMDebugDir,
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server forced shutdown: %v", err)
	}

	if handler.ShutdownEventPersistWorker != nil {
		handler.ShutdownEventPersistWorker()
	}
	handler.StopLoginCleanup()
	handler.StopSessionCleanup()

	log.Println("server exited")
}
