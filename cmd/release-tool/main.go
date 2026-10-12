package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/dist"
	"github.com/coding-hermes/hermes-canopy/internal/updater"
)

func main() {
	artifactDir := flag.String("artifacts", "", "directory containing release assets")
	manifestPath := flag.String("manifest", "", "output path for latest.json")
	version := flag.String("version", "", "release version, normally the tag without v")
	channel := flag.String("channel", "stable", "release channel")
	baseURL := flag.String("base-url", "", "base URL for release assets")
	keyEnv := flag.String("signing-key-env", "RELEASE_SIGNING_KEY", "environment variable containing the Ed25519 signing key")
	flag.Parse()

	if err := run(*artifactDir, *manifestPath, *version, *channel, *baseURL, *keyEnv); err != nil {
		fmt.Fprintf(os.Stderr, "release-tool: %v\n", err)
		os.Exit(1)
	}
}

func run(artifactDir, manifestPath, version, channel, baseURL, keyEnv string) error {
	if strings.TrimSpace(artifactDir) == "" || strings.TrimSpace(manifestPath) == "" || strings.TrimSpace(version) == "" || strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("artifacts, manifest, version, and base-url are required")
	}
	key, ok := os.LookupEnv(keyEnv)
	if !ok {
		return fmt.Errorf("signing environment variable %s is not set; refusing to publish an unsigned manifest", keyEnv)
	}
	privateKey, err := dist.ParsePrivateKey(key)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	metadata := dist.Metadata{
		Version:     version,
		Channel:     updater.ReleaseChannel(channel),
		GeneratedAt: now,
		PublishedAt: now,
	}
	manifest, err := dist.BuildManifestFromDir(metadata, artifactDir, baseURL, privateKey)
	if err != nil {
		return err
	}
	if err := dist.WriteManifest(manifestPath, manifest); err != nil {
		return err
	}
	return nil
}
