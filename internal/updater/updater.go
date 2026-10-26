// Package updater implements the signed self-update flow for canopyd.
package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ReleaseChannel selects the release stream used by the updater.
type ReleaseChannel string

const (
	ChannelStable  ReleaseChannel = "stable"
	ChannelBeta    ReleaseChannel = "beta"
	ChannelNightly ReleaseChannel = "nightly"

	defaultManifestURLBase = "https://releases.canopy.app"
)

// Valid reports whether c is a supported release channel.
func (c ReleaseChannel) Valid() bool {
	switch c {
	case ChannelStable, ChannelBeta, ChannelNightly:
		return true
	default:
		return false
	}
}

// ValidateChannel validates a command-line or configuration channel value.
func ValidateChannel(channel string) error {
	if !ReleaseChannel(channel).Valid() {
		return fmt.Errorf("updater: invalid channel %q (want stable, beta, or nightly)", channel)
	}
	return nil
}

// TargetPlatform identifies the platform and packaging mode of an artifact.
type TargetPlatform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
	Mode string `json:"mode"`
}

// ReleaseArtifact describes one downloadable release binary.
type ReleaseArtifact struct {
	Version   string         `json:"version"`
	Platform  TargetPlatform `json:"platform"`
	URL       string         `json:"url"`
	SHA256    string         `json:"sha256"`
	Signature []byte         `json:"signature"`
	// Ed25519Signature is accepted for compatibility with the original
	// release-metadata field name. New manifests should use Signature.
	Ed25519Signature []byte         `json:"ed25519_signature,omitempty"`
	Channel          ReleaseChannel `json:"channel"`
	PublishedAt      time.Time      `json:"published_at"`
	MinVersion       string         `json:"min_version,omitempty"`
	ReleaseNotesURL  string         `json:"release_notes_url,omitempty"`
}

func (a ReleaseArtifact) signature() []byte {
	if len(a.Signature) != 0 {
		return a.Signature
	}
	return a.Ed25519Signature
}

// ReleaseManifest is the JSON document served by the release endpoint.
type ReleaseManifest struct {
	Channel           ReleaseChannel    `json:"channel"`
	Artifacts         []ReleaseArtifact `json:"artifacts"`
	GeneratedAt       time.Time         `json:"generated_at"`
	ManifestSignature []byte            `json:"manifest_signature"`
}

// UpdateCheckResult is emitted by RunPeriodicChecks.
type UpdateCheckResult struct {
	Available bool
	Artifact  *ReleaseArtifact
	CheckedAt time.Time
	Error     error
}

var (
	// ErrInvalidSignature indicates that the downloaded bytes were not signed
	// by the configured release key.
	ErrInvalidSignature = errors.New("updater: invalid Ed25519 signature")
	// ErrCheckInProgress prevents overlapping manifest requests.
	ErrCheckInProgress = errors.New("updater: update check already in progress")
)

// Updater handles signed updates for the currently running binary.
type Updater struct {
	CurrentVersion string
	Channel        ReleaseChannel
	ManifestURL    string
	PublicKey      ed25519.PublicKey
	CheckInterval  time.Duration
	OnStartupCheck bool
	StateDir       string

	// HTTPClient is optional. The default client has a bounded timeout.
	HTTPClient *http.Client
	// BinaryName is useful for tests and launchers. When empty, canopyd (or
	// canopyd.exe on Windows) is used when StateDir is explicitly provided.
	BinaryName string
	// BinaryPath overrides the executable path used for swapping.
	BinaryPath string

	checkMu sync.Mutex
}

// CheckForUpdate fetches the channel manifest and selects a newer artifact for
// this OS, architecture, and embed mode. A malformed or unverifiable response
// is an error; a valid response without a newer artifact is not.
func (u *Updater) CheckForUpdate(ctx context.Context) (*ReleaseArtifact, bool, error) {
	if !u.checkMu.TryLock() {
		return nil, false, ErrCheckInProgress
	}
	defer u.checkMu.Unlock()

	channel := u.channel()
	if err := ValidateChannel(string(channel)); err != nil {
		return nil, false, err
	}
	current, err := parseVersion(u.CurrentVersion)
	if err != nil {
		return nil, false, fmt.Errorf("updater: current version: %w", err)
	}
	url := u.ManifestURL
	if url == "" {
		url = defaultManifestURL(channel)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, fmt.Errorf("updater: create manifest request: %w", err)
	}
	resp, err := u.httpClient().Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("updater: fetch manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("updater: manifest returned HTTP %s", resp.Status)
	}
	var manifest ReleaseManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&manifest); err != nil {
		return nil, false, fmt.Errorf("updater: decode manifest: %w", err)
	}
	if manifest.Channel != "" && manifest.Channel != channel {
		return nil, false, fmt.Errorf("updater: manifest channel %q does not match requested channel %q", manifest.Channel, channel)
	}
	if err := VerifyManifestSignature(u.PublicKey, manifest); err != nil {
		return nil, false, err
	}

	platform := TargetPlatform{OS: runtime.GOOS, Arch: runtime.GOARCH, Mode: "embed"}
	for i := range manifest.Artifacts {
		artifact := manifest.Artifacts[i]
		if artifact.Channel != "" && artifact.Channel != channel {
			continue
		}
		if artifact.Platform.OS != platform.OS || artifact.Platform.Arch != platform.Arch {
			continue
		}
		if artifact.Platform.Mode != "" && artifact.Platform.Mode != platform.Mode {
			continue
		}
		remote, err := parseVersion(artifact.Version)
		if err != nil {
			return nil, false, fmt.Errorf("updater: artifact version: %w", err)
		}
		if channel == ChannelNightly && !artifact.PublishedAt.IsZero() && time.Since(artifact.PublishedAt) > 7*24*time.Hour {
			continue
		}
		if compareVersions(remote, current) <= 0 {
			continue
		}
		if artifact.MinVersion != "" {
			minimum, err := parseVersion(artifact.MinVersion)
			if err != nil {
				return nil, false, fmt.Errorf("updater: artifact minimum version: %w", err)
			}
			_ = minimum // The artifact itself is already newer than current.
		}
		return &artifact, true, nil
	}
	return nil, false, nil
}

// DownloadUpdate streams artifact.URL into a partial file, verifies its
// SHA-256 digest and Ed25519 signature, and atomically publishes canopyd.new.
func (u *Updater) DownloadUpdate(ctx context.Context, artifact *ReleaseArtifact) error {
	if artifact == nil {
		return errors.New("updater: nil release artifact")
	}
	if artifact.URL == "" {
		return errors.New("updater: release artifact has no download URL")
	}
	if err := validateSHA256(artifact.SHA256); err != nil {
		return err
	}
	if len(u.PublicKey) != ed25519.PublicKeySize {
		return errors.New("updater: Ed25519 public key is required")
	}
	if len(artifact.signature()) == 0 {
		return ErrInvalidSignature
	}
	stateDir, err := u.stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("updater: create state directory: %w", err)
	}
	partial := filepath.Join(stateDir, u.newFileName()+".partial")
	staged := filepath.Join(stateDir, u.newFileName())
	_ = os.Remove(partial)
	_ = os.Remove(staged)
	defer func() { _ = os.Remove(partial) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return fmt.Errorf("updater: create download request: %w", err)
	}
	resp, err := u.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("updater: download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("updater: download returned HTTP %s", resp.Status)
	}

	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("updater: create partial download: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("updater: stream download: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("updater: close partial download: %w", closeErr)
	}
	digest := hash.Sum(nil)
	if !strings.EqualFold(hex.EncodeToString(digest), artifact.SHA256) {
		return fmt.Errorf("updater: %w: got %s, want %s", ErrChecksumMismatch, hex.EncodeToString(digest), artifact.SHA256)
	}
	if !VerifySignature(u.PublicKey, digest, artifact.signature()) {
		return ErrInvalidSignature
	}
	if err := os.Rename(partial, staged); err != nil {
		return fmt.Errorf("updater: publish staged update: %w", err)
	}
	return nil
}

// ApplyUpdate atomically swaps the running binary for canopyd.new and keeps
// the previous binary as canopyd.old for one rollback cycle.
func (u *Updater) ApplyUpdate() (string, error) {
	stateDir, err := u.stateDir()
	if err != nil {
		return "", err
	}
	current, err := u.currentPath(stateDir)
	if err != nil {
		return "", err
	}
	old := filepath.Join(stateDir, u.oldFileName())
	staged := filepath.Join(stateDir, u.newFileName())
	if _, err := os.Stat(staged); err != nil {
		return "", fmt.Errorf("updater: staged update is unavailable: %w", err)
	}
	if err := applySwap(current, old, staged); err != nil {
		return "", fmt.Errorf("updater: apply update: %w", err)
	}
	return current, nil
}

// Rollback restores the retained old binary over the current binary.
func (u *Updater) Rollback() error {
	stateDir, err := u.stateDir()
	if err != nil {
		return err
	}
	current, err := u.currentPath(stateDir)
	if err != nil {
		return err
	}
	old := filepath.Join(stateDir, u.oldFileName())
	if _, err := os.Stat(old); err != nil {
		return fmt.Errorf("updater: rollback binary is unavailable: %w", err)
	}
	if err := replaceFile(old, current); err != nil {
		return fmt.Errorf("updater: rollback: %w", err)
	}
	return nil
}

// Cleanup removes the one-cycle rollback binary after a successful restart.
func (u *Updater) Cleanup() error {
	stateDir, err := u.stateDir()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stateDir, u.oldFileName())); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("updater: cleanup old binary: %w", err)
	}
	return nil
}

// RunPeriodicChecks performs an optional startup check and then checks at the
// requested interval. It returns when ctx is canceled; no goroutine is left
// running after that point.
func (u *Updater) RunPeriodicChecks(ctx context.Context, interval time.Duration, onResult func(UpdateCheckResult)) {
	if interval <= 0 {
		return
	}
	check := func() {
		artifact, available, err := u.CheckForUpdate(ctx)
		result := UpdateCheckResult{Available: available, Artifact: artifact, CheckedAt: time.Now().UTC(), Error: err}
		if onResult != nil {
			onResult(result)
		}
	}
	if u.OnStartupCheck {
		check()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func defaultManifestURL(channel ReleaseChannel) string {
	return fmt.Sprintf("%s/%s/latest.json", defaultManifestURLBase, channel)
}

// DefaultManifestURL returns the built-in per-channel manifest URL
// (https://releases.canopy.app/<channel>/latest.json). Exported for the
// desktop-shell feed adapter (internal/wailsbridge), which must resolve the
// same default as Updater when --update-manifest-url is empty; keeping one
// source of truth stops the two surfaces from drifting.
func DefaultManifestURL(channel ReleaseChannel) string {
	return defaultManifestURL(channel)
}

// DefaultPlatform and DefaultArch expose the runtime platform identity the
// updater matches artifacts against. Exported for the desktop-shell feed
// adapter so its artifact selection cannot silently use a different
// platform spelling than the embed updater.
func DefaultPlatform() string { return runtime.GOOS }
func DefaultArch() string     { return runtime.GOARCH }

// CompareVersions compares remote against current with the same semantics
// CheckForUpdate uses: semver with an optional leading "v" on either side.
// Returns -1 when remote < current, 0 when equal, 1 when remote > current.
// Exported for the desktop-shell feed adapter (internal/wailsbridge), which
// must apply the identical version policy as the embed updater.
func CompareVersions(remote, current string) (int, error) {
	r, err := parseVersion(remote)
	if err != nil {
		return 0, fmt.Errorf("updater: remote version: %w", err)
	}
	c, err := parseVersion(current)
	if err != nil {
		return 0, fmt.Errorf("updater: current version: %w", err)
	}
	return compareVersions(r, c), nil
}

func (u *Updater) channel() ReleaseChannel {
	if u.Channel == "" {
		return ChannelStable
	}
	return u.Channel
}

func (u *Updater) httpClient() *http.Client {
	if u.HTTPClient != nil {
		return u.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (u *Updater) stateDir() (string, error) {
	if u.StateDir != "" {
		return filepath.Clean(u.StateDir), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("updater: locate executable: %w", err)
	}
	return filepath.Dir(executable), nil
}

func (u *Updater) binaryName() string {
	if u.BinaryName != "" {
		return u.BinaryName
	}
	if runtime.GOOS == "windows" {
		return "canopyd.exe"
	}
	return "canopyd"
}

func (u *Updater) newFileName() string { return u.binaryName() + ".new" }
func (u *Updater) oldFileName() string { return u.binaryName() + ".old" }

func (u *Updater) currentPath(stateDir string) (string, error) {
	if u.BinaryPath != "" {
		return filepath.Clean(u.BinaryPath), nil
	}
	if u.StateDir != "" {
		return filepath.Join(stateDir, u.binaryName()), nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("updater: locate executable: %w", err)
	}
	return executable, nil
}

// ErrChecksumMismatch is returned when downloaded bytes do not match metadata.
var ErrChecksumMismatch = errors.New("checksum mismatch")

func validateSHA256(value string) error {
	if len(value) != 64 {
		return fmt.Errorf("updater: sha256 must be 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("updater: sha256 is not hexadecimal: %w", err)
	}
	return nil
}

func compareVersions(a, b version) int {
	for i := range a.core {
		if a.core[i] != b.core[i] {
			if a.core[i] < b.core[i] {
				return -1
			}
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		left, right := a.pre[i], b.pre[i]
		if left == right {
			continue
		}
		ln, lerr := numericIdentifier(left)
		rn, rerr := numericIdentifier(right)
		if lerr == nil && rerr == nil {
			if ln < rn {
				return -1
			}
			return 1
		}
		if lerr == nil {
			return -1
		}
		if rerr == nil {
			return 1
		}
		if left < right {
			return -1
		}
		return 1
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}

func numericIdentifier(value string) (uint64, error) {
	var n uint64
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("not numeric")
		}
		n = n*10 + uint64(r-'0')
	}
	return n, nil
}

type version struct {
	core [3]uint64
	pre  []string
}

func parseVersion(raw string) (version, error) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	if value == "" {
		return version{}, fmt.Errorf("invalid semver %q", raw)
	}
	value = strings.SplitN(value, "+", 2)[0]
	parts := strings.SplitN(value, "-", 2)
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return version{}, fmt.Errorf("invalid semver %q", raw)
	}
	var result version
	for i, part := range core {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return version{}, fmt.Errorf("invalid semver %q", raw)
		}
		n, err := numericIdentifier(part)
		if err != nil {
			return version{}, fmt.Errorf("invalid semver %q", raw)
		}
		result.core[i] = n
	}
	if len(parts) == 2 {
		if parts[1] == "" {
			return version{}, fmt.Errorf("invalid semver %q", raw)
		}
		for _, identifier := range strings.Split(parts[1], ".") {
			if identifier == "" {
				return version{}, fmt.Errorf("invalid semver %q", raw)
			}
			for _, r := range identifier {
				if r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
					continue
				}
				return version{}, fmt.Errorf("invalid semver %q", raw)
			}
			if len(identifier) > 1 && identifier[0] == '0' {
				if _, err := numericIdentifier(identifier); err == nil {
					return version{}, fmt.Errorf("invalid semver %q", raw)
				}
			}
			result.pre = append(result.pre, identifier)
		}
	}
	return result, nil
}
