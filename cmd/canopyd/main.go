package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/config"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/server"
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

	// Create HTTP server
	srv := server.New(cfg.HTTPAddr)
	srv.Router().Get("/version", versionHandler)

	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("shutdown error")
	}

	// TODO: close DB connection when database layer is wired
	cancel()
	log.Info().Msg("canopyd stopped")
}

func versionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"version":"%s"}`, version)))
}
