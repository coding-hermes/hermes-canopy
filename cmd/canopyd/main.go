package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/config"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/handler"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/hermes"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/mls"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/server"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/transport"
)

// version is injected at build time via -ldflags.
// Example: go build -ldflags="-X main.version=v0.1.0" ./cmd/canopyd
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Init logger
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Load config
	cfg := config.FromEnv()

	// Set log level
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	log.Info().
		Str("version", version).
		Str("http_addr", cfg.HTTPAddr).
		Str("db_host", cfg.DBHost).
		Msg("canopyd starting")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize the database and inject the tree service into HTTP routes.
	database, err := db.New(ctx, db.PoolConfig{DSN: cfg.DSN()})
	if err != nil {
		log.Fatal().Err(err).Msg("database initialization failed")
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		log.Fatal().Err(err).Msg("database migration failed")
	}

	treeService := service.NewTreeService(
		database.Trees,
		database.Nodes,
		database.Edges,
		database.Pool,
	)
	nodeService := service.NewNodeService(
		database.Nodes,
		database.Edges,
		database.Pool,
	)

	// SSE hub — in-memory ring buffer + per-tree subscriber map per
	// SPEC-API-01 §9 / §11. Bounded to 10k connections, 1h retention,
	// 1000-event ring per tree.
	sseHub := sse.NewHub()

	// Sync engine — coordinates event logging, snapshot creation, and
	// SSE broadcast after every mutation. Per SPEC-DM-02 §8.3.
	syncEngine := sync.NewEngine(database.Events, database.Snapshots, sseHub,
		sync.DefaultEngineConfig())

	approvalSvc := service.NewApprovalService(
		database.Approvals,
		database.AuditLog,
		database.Users,
		database.Profiles,
		database.Members,
		sseHub,
	)

	mlsSvc := mls.NewMLSService(
		database.Pool,
		database.MLSGroups,
		database.MLSMembers,
		database.MLSKeyPackages,
		database.MLSPendingProposals,
		sseHub,
	)
	kpMgr := newPGMLSKeyPackageManager(database.MLSKeyPackages)
	mlsHandler := handler.NewMLSHandler(mlsSvc, kpMgr)

	// Profile router — maps workspaces to Hermes profiles (SPEC-FTR-07 §3.3).
	profileRouter := hermes.NewPGProfileRouter(
		database.Pool,
		[]byte("dev-secret-change-me-production!"),
	)

	// Transport adapter layer per SPEC-FTR-04.
	ss := transport.NewTransportSelector(transport.ModeLocal, transport.TopologyLoopback)
	connMgr := transport.NewConnectionManager(ss)
	tptAdapter := transport.NewSSEAdapter(sseHub)

	srv := server.New(cfg.HTTPAddr, treeService, nodeService, sseHub, syncEngine, approvalSvc,
		tptAdapter, connMgr, ss,
		database.TransportConfigs, database.TransportEvents, mlsHandler)

	srv.Router().Get("/version", versionHandler)
	srv.Router().Mount(
		"/api/v1/workspaces/{workspace_id}/profiles",
		handler.NewProfileHandler(profileRouter).Routes(),
	)

	// Start server in background

	go func() {
		log.Info().Str("addr", cfg.HTTPAddr).Msg("HTTP server listening")
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Err(err).Msg("HTTP server error")
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("shutting down")

	// Graceful shutdown with 30s timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 30*time.Second)
	defer shutdownCancel()

	// Drain SSE first so connected clients receive a "done" event.
	if err := sseHub.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("sse hub shutdown error")
	}

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("shutdown error")
	}

	cancel()
	log.Info().Msg("canopyd stopped")
}

type pgMLSKeyPackageManager struct {
	db.MLSKeyPackageRepo
}

func newPGMLSKeyPackageManager(repo db.MLSKeyPackageRepo) *pgMLSKeyPackageManager {
	return &pgMLSKeyPackageManager{MLSKeyPackageRepo: repo}
}

func (m *pgMLSKeyPackageManager) GenerateKeyPackage(
	ctx context.Context,
	profileID uuid.UUID,
	credential mls.MLSCredential,
	keyPair mls.Ed25519KeyPair,
) (mls.MLSKeyPackage, error) {
	if credential.ProfileID == uuid.Nil || credential.ProfileID != profileID {
		return mls.MLSKeyPackage{}, mls.ErrInvalidCredential
	}
	if len(credential.Identity) == 0 || credential.CredentialType == "" ||
		len(credential.SignaturePublicKey) != ed25519.PublicKeySize ||
		len(keyPair.PublicKey) != ed25519.PublicKeySize ||
		len(keyPair.PrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(credential.SignaturePublicKey, keyPair.PublicKey) ||
		!bytes.Equal(keyPair.PrivateKey.Public().(ed25519.PublicKey), keyPair.PublicKey) {
		return mls.MLSKeyPackage{}, mls.ErrInvalidCredential
	}

	packageBytes := make([]byte, 64)
	if _, err := rand.Read(packageBytes); err != nil {
		return mls.MLSKeyPackage{}, err
	}
	digest := sha256.Sum256(packageBytes)
	now := time.Now().UTC()
	keyPackage := mls.MLSKeyPackage{
		ID:              uuid.New(),
		ProfileID:       profileID,
		KeyPackageBytes: packageBytes,
		Hash:            digest[:],
		CipherSuite:     "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519",
		CreatedAt:       now,
		ExpiresAt:       now.Add(24 * time.Hour),
	}
	if err := m.Create(ctx, &db.MLSKeyPackage{
		ID:              keyPackage.ID,
		ProfileID:       keyPackage.ProfileID,
		KeyPackageBytes: keyPackage.KeyPackageBytes,
		Hash:            keyPackage.Hash,
		CipherSuite:     keyPackage.CipherSuite,
		CreatedAt:       keyPackage.CreatedAt,
		ExpiresAt:       keyPackage.ExpiresAt,
	}); err != nil {
		return mls.MLSKeyPackage{}, err
	}
	return keyPackage, nil
}

func (m *pgMLSKeyPackageManager) GetKeyPackage(ctx context.Context, profileID uuid.UUID) (mls.MLSKeyPackage, error) {
	keyPackage, err := m.GetLatest(ctx, profileID)
	if err != nil {
		return mls.MLSKeyPackage{}, err
	}
	if !keyPackage.ExpiresAt.After(time.Now().UTC()) {
		return mls.MLSKeyPackage{}, mls.ErrKeyPackageExpired
	}
	return mls.MLSKeyPackage{
		ID:              keyPackage.ID,
		ProfileID:       keyPackage.ProfileID,
		KeyPackageBytes: keyPackage.KeyPackageBytes,
		Hash:            keyPackage.Hash,
		CipherSuite:     keyPackage.CipherSuite,
		CreatedAt:       keyPackage.CreatedAt,
		ExpiresAt:       keyPackage.ExpiresAt,
	}, nil
}

func (m *pgMLSKeyPackageManager) ExpireKeyPackage(ctx context.Context, keyPackageID uuid.UUID) error {
	return m.Expire(ctx, keyPackageID)
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"version":"%s"}`, version)))
}
