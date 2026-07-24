// Package server provides the HTTP server for canopyd.
package server

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/hlog"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/handler"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/transport"
)

// Server is the Canopy HTTP server.
type Server struct {
	httpServer      *http.Server
	router          *chi.Mux
	sseHub          sse.SSEHub
	transportMgr    *transport.ConnectionManager
	transportAdaper transport.TransportAdapter
}

// New creates a new Server with middleware and routes wired.
func New(
	addr string,
	treeSvc service.TreeService,
	nodeSvc service.NodeService,
	sseHub sse.SSEHub,
	syncEngine sync.SyncEngine,
	approvalSvc service.ApprovalService,
	transportAdaper transport.TransportAdapter,
	connMgr *transport.ConnectionManager,
	sel *transport.TransportSelector,
	configRepo db.TransportConfigRepo,
	eventRepo db.TransportEventRepo,
) *Server {
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

	// REST endpoints
	r.Mount("/trees", handler.NewTreeHandler(treeSvc, syncEngine).Routes())
	r.Mount("/", handler.NewNodeHandler(nodeSvc, syncEngine).Routes())

	// SSE endpoint per SPEC-API-01.
	sseHandler := sse.NewHandler(sseHub)
	r.Get("/trees/{tree_id}/events", sseHandler.HandleTreeEvents)

	// Sync endpoint per SPEC-DM-02 §7.
	r.Mount("/trees/{tree_id}/sync", handler.NewSyncHandler(syncEngine).Routes())

	// Approval endpoints per SPEC-API-05.
	r.Mount("/approvals", handler.NewApprovalHandler(approvalSvc).Routes())

	// Transport adapter endpoints per SPEC-FTR-04 §6.
	nodeID, _ := os.Hostname()
	if nodeID == "" {
		nodeID = "canopyd-" + time.Now().Format("20060102150405")
	}
	transHandler := handler.NewTransportHandler(transportAdaper, connMgr, configRepo, eventRepo, nodeID)
	r.Mount("/api/v1/transports", transHandler.Routes())

	// Transport health probes (unauthenticated).
	for _, tt := range transport.AllTransportTypes() {
		r.Get("/health/transports/"+string(tt), transHandler.HealthProbe(string(tt)))
	}

	return &Server{
		router:          r,
		sseHub:          sseHub,
		transportMgr:    connMgr,
		transportAdaper: transportAdaper,
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

// SSEHub returns the server's SSE hub.
func (s *Server) SSEHub() sse.SSEHub {
	return s.sseHub
}

// TransportManager returns the connection manager for transport adapters.
func (s *Server) TransportManager() *transport.ConnectionManager {
	return s.transportMgr
}

// Start begins listening and serving HTTP.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
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
