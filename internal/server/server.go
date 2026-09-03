// Package server provides the HTTP server for canopyd.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/hlog"
	"github.com/rs/zerolog/log"

	"github.com/coding-hermes/hermes-canopy/internal/collaboration"
	"github.com/coding-hermes/hermes-canopy/internal/config"
	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/federation"
	"github.com/coding-hermes/hermes-canopy/internal/gateway"
	"github.com/coding-hermes/hermes-canopy/internal/handler"
	"github.com/coding-hermes/hermes-canopy/internal/hermes"
	"github.com/coding-hermes/hermes-canopy/internal/reference"
	"github.com/coding-hermes/hermes-canopy/internal/search"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/sync"
	"github.com/coding-hermes/hermes-canopy/internal/telemetry"
	"github.com/coding-hermes/hermes-canopy/internal/transport"
)

// Server is the Canopy HTTP server.
type Server struct {
	httpServer      *http.Server
	router          *chi.Mux
	healthDB        HealthDB
	sseHub          sse.SSEHub
	transportMgr    *transport.ConnectionManager
	transportAdaper transport.TransportAdapter
	mlsHandler      *handler.MLSHandler
	metrics         *telemetry.Metrics
	relayCancel     context.CancelFunc
	transportDrain  func() error
}

// New creates a new Server with middleware and routes wired.
func New(
	healthProbe HealthDB,
	addr string,
	jwtSecret string,
	treeSvc service.TreeService,
	nodeSvc service.NodeService,
	exportSvc service.ExportService,
	sseHub sse.SSEHub,
	syncEngine sync.SyncEngine,
	approvalSvc service.ApprovalService,
	transportAdaper transport.TransportAdapter,
	connMgr *transport.ConnectionManager,
	sel *transport.TransportSelector,
	configRepo db.TransportConfigRepo,
	eventRepo db.TransportEventRepo,
	membersRepo db.TreeMemberRepo,
	userRepo db.UserRepo,
	profileRouter *hermes.PGProfileRouter,
	mlsHandler *handler.MLSHandler,
	topicSvc service.TopicService,
	cardSvc service.CardService,
	graphSvc service.GraphService,
	collabSvc collaboration.CollaborationService,
	metrics *telemetry.Metrics,
	ctxCompiler ctxpkg.Compiler,
	pluginSvc service.PluginRegistryService,
	topicSearchSvc search.TopicSearchService,
	referenceSvc reference.ReferenceService,
	federationSvc federation.FederationService,
	cfg *config.Config,
) *Server {
	r := chi.NewRouter()

	// Stale-build visibility (DF-HERMES-CANOPY-1): /health reports the
	// live schema version vs. this binary's embedded migrations.
	if healthProbe != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx := context.WithValue(req.Context(), healthSchemaKey{}, healthProbe)
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
	}

	// === Global middleware (applied to every route) ===
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(hlog.RequestIDHandler("req_id", "X-Request-Id"))
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(corsMiddleware(cfg.CORSOrigin))
	r.Use(handler.BodySizeLimit(1024 * 1024)) // 1MB per SPEC-API-02 §10.1

	// Rate limiter: 100 req/s per IP, burst 200.
	rateLimiter := handler.NewRateLimiter(100, 200)
	r.Use(handler.RateLimit(rateLimiter))

	// Prometheus metrics middleware (records request duration, count, active conns).
	if metrics != nil {
		r.Use(telemetry.MetricsMiddleware(metrics))
	}

	// Health and version endpoints (public — no auth).
	r.Get("/health", healthHandler)
	r.Get("/healthz", healthHandler)
	r.Get("/version", versionHandler)

	// Prometheus /metrics endpoint (public — standard practice for /metrics).
	if metrics != nil {
		r.Handle("/metrics", promhttp.Handler())
	}

	// === Authenticated routes ===
	authMW := handler.AuthMiddleware(jwtSecret)
	membershipMW := handler.TreeMembershipMiddleware(membersRepo)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)

		// Topic search + context injection (TM-03). Tree-scoped, membership-gated.
		// Registered BEFORE the /trees mount so chi's radix router resolves
		// these specific patterns before the wildcard subrouter.
		// Spec: SPEC-TM-03 §6.
		if topicSearchSvc != nil {
			searchHandler := handler.NewTopicSearchHandler(topicSearchSvc, sseHub)
			r.With(membershipMW).Get("/trees/{tree_id}/topics/search", searchHandler.SearchTopics)
			r.With(membershipMW).Get("/trees/{tree_id}/topics/recent", searchHandler.GetRecentTopics)
			r.With(membershipMW).Get("/trees/{tree_id}/topics/{topic_id}/preview", searchHandler.GetTopicPreview)
			r.With(membershipMW).Post("/trees/{tree_id}/context/inject", searchHandler.InjectContext)
		}

		// Reference resolution (TM-04). Tree-scoped, membership-gated.
		// Spec: SPEC-TM-04 §6.
		if referenceSvc != nil {
			refHandler := handler.NewReferenceHandler(referenceSvc, sseHub)
			r.With(membershipMW).Get("/trees/{tree_id}/references/autocomplete", refHandler.Autocomplete)
			r.With(membershipMW).Post("/trees/{tree_id}/references/resolve", refHandler.Resolve)
			r.With(membershipMW).Post("/trees/{tree_id}/references/inject", refHandler.Inject)
		}

		// Tree CRUD (SPEC-API-02).
		treeHandler := handler.NewTreeHandler(treeSvc, syncEngine).
			WithShares(userRepo, membersRepo, sseHub)
		r.Mount("/trees", treeHandler.Routes())

		// Node CRUD (SPEC-API-03) — tree-scoped routes get membership check.
		nodeHandler := handler.NewNodeHandler(nodeSvc, syncEngine).
			WithReferences(referenceSvc, sseHub)
		treeNodes := chi.NewRouter()
		treeNodes.Use(membershipMW)
		treeNodes.Mount("/", nodeHandler.TreeRoutes())
		r.Mount("/trees/{tree_id}/nodes", treeNodes)

		// Sync endpoints (SPEC-DM-02 §7) — tree-scoped, membership-gated.
		r.With(membershipMW).Mount("/trees/{tree_id}/sync", handler.NewSyncHandler(syncEngine).Routes())

		// SSE endpoint (SPEC-API-01).
		sseHandler := sse.NewHandler(sseHub)
		r.With(membershipMW).Get("/trees/{tree_id}/events", sseHandler.HandleTreeEvents)

		// Approval endpoints (SPEC-API-05).
		r.Mount("/approvals", handler.NewApprovalHandler(approvalSvc).Routes())

		// Profile routing (SPEC-FTR-07 §3.3).
		r.Mount("/workspaces/{workspace_id}/profiles",
			handler.NewProfileHandler(profileRouter).Routes()) // ProfileRouter passed via main.go wiring

		// Topic endpoints (BE-14 — real CRUD). Spec: SPEC-TM-01, SPEC-TM-03, SPEC-TM-05.
		r.Mount("/topics", handler.NewTopicHandler(topicSvc).Routes())

		// Topic detection endpoints (TM-02). Proposal-scoped routes are
		// mounted directly (no tree scope); config routes are tree-scoped.
		// Spec: SPEC-TM-02 §8.2.
		topicDetectionHandler := handler.NewTopicDetectionHandler(topicSvc)
		r.Mount("/topic-proposals", topicDetectionHandler.ProposalRoutes())
		r.With(membershipMW).Mount("/trees/{tree_id}/topic-detection", topicDetectionHandler.TreeRoutes())

		// Card endpoints (BE-15 — real CRUD). Spec: SPEC-PL-03.
		r.Mount("/cards", handler.NewCardHandler(cardSvc).Routes())

		// Collaboration endpoints (SPEC-FTR-01 §5.1/§5.2) — workspace CRUD,
		// membership, and invitations.
		r.Mount("/collab", handler.NewCollabHandler(collabSvc).Routes())

		// Federation link management (SPEC-FTR-02 P1, §5.1). User JWT auth
		// is inherited from this /api/v1 router.
		if federationSvc != nil {
			fedHandler := handler.NewFederationHandler(federationSvc, cfg.HTTPAddr, sseHub)
			if profileRouter, ok := federationSvc.(federation.ProfileRouter); ok {
				fedHandler.WithProfileRouter(profileRouter)
			}
			r.Mount("/federation/link", fedHandler.LinkRoutes())
			r.Mount("/federation/routes", fedHandler.RouteRoutes())
			r.Mount("/federation/conflicts", fedHandler.ConflictRoutes())
			r.Get("/federation/health", fedHandler.Health)
		}

		// Graph endpoints (BE-16 — real CRUD). Spec: ARCHITECTURE.md §3.
		r.Mount("/graph", handler.NewGraphHandler(graphSvc).Routes())

		// Workspace channels (SPEC-023 §5) — channel list, message POST,
		// per-channel SSE event stream. In-memory registry; no DB for MVP.
		r.Mount("/workspace/channels", handler.NewWorkspaceHandler(sseHub).Routes())

		// Agent roster (SPEC-023 §5 + §7) — list + detail with trust
		// history timeline. In-memory registry; no DB for MVP.
		r.Mount("/agents", handler.NewAgentHandler().Routes())

		// PR review panel (SPEC-023 §2 item 2, §4, §5) — review list +
		// detail incl. blast radius + Chimera verdict, and a trigger
		// endpoint that broadcasts review events on the SSE hub.
		// In-memory registry; no DB for MVP.
		r.Mount("/reviews", handler.NewReviewHandler(sseHub).Routes())

		// Export/import endpoints (GAP-003).
		// Registered directly (not via Mount) because /trees is already
		// occupied by the TreeHandler router above.
		exportHandler := handler.NewExportHandler(exportSvc)
		r.Get("/trees/{tree_id}/export", exportHandler.ExportTree)
		r.Post("/trees/import", exportHandler.ImportTree)

		// MCP endpoint — programmatic agent access.
		mcpHandler := handler.NewMCPHandler(treeSvc, nodeSvc, topicSvc, cardSvc, graphSvc, approvalSvc)
		r.Mount("/mcp", mcpHandler.Routes())

		// Context compiler (GAP-001) — budgeted context assembly with visible manifest.
		r.Get("/context/{node_id}", handler.NewContextHandler(ctxCompiler, cfg.ContextDefaultBudget).Compile)

		// Plugin sandbox (GAP-002) — register/list/source/install + instances.
		// Plugin lifecycle events use the existing SSE hub, keyed by plugin id.
		r.Get("/plugins/{tree_id}/events", sse.NewHandler(sseHub).HandleTreeEvents)
		r.Mount("/plugins", handler.NewPluginHandler(pluginSvc, sseHub).Routes())

		// Live Hermes gateway (GAP-050) — canopyd is a CLIENT of the Hermes
		// gateway api_server (hermes-webui pattern). The service is constructed
		// here from config so no main.go signature churn is needed; a bad
		// base_url falls back to the default so the UI can still show the
		// gateway as offline instead of crashing the server.
		gwClient, gwErr := gateway.NewClient(cfg.GatewayBaseURL, cfg.GatewayAPIKey)
		if gwErr != nil {
			log.Warn().Err(gwErr).Msg("gateway: invalid HERMES_WEBUI_GATEWAY_BASE_URL; using default")
			gwClient, _ = gateway.NewClient(gateway.DefaultBaseURL, cfg.GatewayAPIKey)
		}
		// Restore the persisted run registry and refresh non-terminal records
		// against the gateway (GAP-054). NewServiceWithState never fails: a
		// gateway that is down or missing endpoints only logs, so canopyd still
		// boots and the /gateway routes still mount.
		gatewaySvc := gateway.NewServiceWithState(gwClient, gateway.DefaultStateFile())
		r.Mount("/gateway", handler.NewGatewayHandler(gatewaySvc).Routes())
	})

	// Federation handshake is P2P-authenticated, so it cannot inherit the
	// user-only JWT middleware mounted on the main /api/v1 router.
	if federationSvc != nil {
		fedHandler := handler.NewFederationHandler(federationSvc, cfg.HTTPAddr, sseHub)
		if profileRouter, ok := federationSvc.(federation.ProfileRouter); ok {
			fedHandler.WithProfileRouter(profileRouter)
		}
		r.With(handler.FederationAuthMiddleware(jwtSecret, federationSvc)).Post("/api/v1/federation/handshake", fedHandler.Handshake)
		r.Get("/api/v1/federation/events", fedHandler.Events)
		r.Post("/api/v1/federation/events", fedHandler.PostEvent)
		r.Get("/api/v1/federation/events/replay", fedHandler.Replay)
	}

	// Transport adapter endpoints per SPEC-FTR-04 §6 (authenticated).
	nodeID, _ := os.Hostname()
	if nodeID == "" {
		nodeID = "canopyd-" + time.Now().Format("20060102150405")
	}
	transHandler := handler.NewTransportHandler(transportAdaper, connMgr, configRepo, eventRepo, nodeID)
	r.Route("/api/v1/transports", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/", transHandler.Routes())
	})
	if relay, ok := connMgr.Adapter(transport.TransportRelay).(*transport.RelayService); ok {
		r.With(authMW).Post("/api/v1/transport/relay", relay.Post)
		r.With(authMW).Get("/api/v1/transport/relay/poll", relay.PollHTTP)
	}

	// Workspace MLS endpoints per SPEC-FTR-03 (authenticated).
	r.Route("/api/v1/workspaces/{workspace_id}/mls", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/", mlsHandler.Routes())
	})

	// Transport health probes (unauthenticated).
	for _, tt := range transport.AllTransportTypes() {
		r.Get("/health/transports/"+string(tt), transHandler.HealthProbe(string(tt)))
	}

	var relayCancel context.CancelFunc
	if provider, ok := federationSvc.(interface {
		Relay() *federation.RelayService
	}); ok && provider.Relay() != nil {
		var relayCtx context.Context
		relayCtx, relayCancel = context.WithCancel(context.Background())
		go provider.Relay().Run(relayCtx)
	}
	return &Server{
		router:          r,
		sseHub:          sseHub,
		transportMgr:    connMgr,
		transportAdaper: transportAdaper,
		mlsHandler:      mlsHandler,
		metrics:         metrics,
		relayCancel:     relayCancel,
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

// SetTransportDrain attaches an optional production transport lifecycle.
// Nil preserves the existing HTTP/SSE-only shutdown behavior.
func (s *Server) SetTransportDrain(drain func() error) { s.transportDrain = drain }

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.relayCancel != nil {
		s.relayCancel()
	}
	var drainErr error
	if s.transportDrain != nil {
		drainErr = s.transportDrain()
	}
	if s.transportMgr != nil {
		drainErr = errors.Join(drainErr, s.transportMgr.Shutdown(ctx))
	}
	return errors.Join(drainErr, s.httpServer.Shutdown(ctx))
}

// healthHandler responds with a simple health check. schema_version and
// embedded_migrations are reported so a stale binary (older embedded
// migrations than the live DB) is visible from a single curl — the
// diagnostic DF-HERMES-CANOPY-1 lacked.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	schemaV, embeddedV := int64(-1), int64(-1)
	if dbh, ok := r.Context().Value(healthSchemaKey{}).(HealthDB); ok {
		schemaV, _ = dbh.SchemaVersion(r.Context())
		embeddedV = dbh.EmbeddedMigrations()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok","service":"canopyd","schema_version":%d,"embedded_migrations":%d}`,
		schemaV, embeddedV)
}

// healthSchemaKey/healthDB let main() inject a read-only schema-version
// probe into the health handler without widening the Server constructor.
type healthSchemaKey struct{}

// HealthDB is the read-only probe surfaced on /health (implemented by
// cmd/canopyd over *db.DB).
type HealthDB interface {
	SchemaVersion(ctx context.Context) (int64, error)
	EmbeddedMigrations() int64
}

// versionHandler responds with the server version.
func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"version":"dev"}`))
}

// corsMiddleware provides configurable CORS for local development.
func corsMiddleware(origin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
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
