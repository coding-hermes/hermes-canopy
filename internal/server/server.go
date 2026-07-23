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
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
)

// Server is the Canopy HTTP server.
type Server struct {
	httpServer *http.Server
	router     *chi.Mux
	sseHub     sse.SSEHub
}

// New creates a new Server with middleware and routes wired.
//
// The sseHub must be non-nil — if you don't need SSE, pass a hub created
// with sse.NewHub(); the hub is cheap to construct and can be ignored
// from the outside if no route ever subscribes a client.
func New(addr string, treeSvc service.TreeService, nodeSvc service.NodeService, sseHub sse.SSEHub) *Server {
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
	r.Mount("/trees", handler.NewTreeHandler(treeSvc).Routes())
	r.Mount("/", handler.NewNodeHandler(nodeSvc).Routes())

	// SSE endpoint per SPEC-API-01.
	sseHandler := sse.NewHandler(sseHub)
	r.Get("/trees/{tree_id}/events", sseHandler.HandleTreeEvents)

	return &Server{
		router: r,
		sseHub: sseHub,
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

// SSEHub returns the server's SSE hub. Useful for tests that need to
// broadcast from service handlers.
func (s *Server) SSEHub() sse.SSEHub {
	return s.sseHub
}

// Start begins listening and serving HTTP.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server. SSE clients receive a
// "done" event first; cancel via the hub directly if you need to drain
// before HTTP shutdown completes.
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
