package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	log.Info().
		Str("version", version).
		Msg("canopyd starting")

	if err := run(); err != nil {
		log.Fatal().Err(err).Msg("fatal error")
	}
}

func run() error {
	// TODO: parse config, start server, block on signal
	log.Info().Msg("canopyd running (scaffold)")
	select {}
}
