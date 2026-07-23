// Package server provides the HTTP server for canopyd.
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/hlog"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/handler"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
)

// Server is the Canopy HTTP server.
type Server struct {
	httpServer *http.Server
	router     *chi.Mux
}

// New creates a new Server with middleware and routes wired.
func New(addr string, svc service.TreeService) *Server {
	r := chi.NewRouter()

	// Middleware stack (order matters)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(hlog.RequestIDHandler("req_id", "X-Request-Id"))
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(corsMiddleware())

	// Health endpoint
	r.Get("/health", healthHandler)
	r.Get("/healthz", healthHandler)
	r.Mount("/trees", handler.NewTreeHandler(svc).Routes())

	return &Server{
		router: r,
		httpServer: &http.Server{
			Addr:         addr,
			Handler:      r,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
	}
}

// Router returns the underlying chi router for registering routes.
func (s *Server) Router() *chi.Mux {
	return s.router
}

// Start begins listening and serving HTTP.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the server with a timeout.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// healthHandler responds with a simple health check.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","service":"canopyd"}`))
}

// corsMiddleware provides permissive CORS for local development.
func corsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-Id")
			w.Header().Set("Access-Control-Max-Age", "86400")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
