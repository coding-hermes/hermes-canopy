// Package config provides configuration types and loading for canopyd.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Config holds all configuration for the canopyd server.
type Config struct {
	// Database
	DBHost     string
	DBPort     int
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
	DBSchema   string
	// DBDriver selects the runtime database backend. The default is postgres so
	// an unset CANOPY_DB_DRIVER keeps the historical boot path unchanged.
	DBDriver string
	// SQLitePath is used when DBDriver is sqlite. It defaults below the canopy
	// data directory and may be overridden with CANOPY_SQLITE_PATH.
	SQLitePath string

	// HTTP
	HTTPAddr string

	// Logging
	LogLevel  string
	LogFormat string

	// CORS
	CORSOrigin string

	// JWT
	JWTSecret string

	// Multi-message reference model (SPEC-PL-06 §14.1): HMAC secret for
	// preflight selection tokens. Empty falls back to JWTSecret.
	ReferenceSelectionSecret string

	// Metrics
	MetricsEnabled bool

	// Context compiler
	ContextMaxAncestors  int // CONTEXT_MAX_ANCESTORS, default 50
	ContextMaxRefs       int // CONTEXT_MAX_REFS, default 5 (soft) — hard cap is 2x this
	ContextDefaultBudget int // CONTEXT_DEFAULT_BUDGET, default 8000 tokens

	// ContextRetrievalMax caps how many topic-search results the context
	// compiler's retrieved tier may fold into a payload (GAP-080 phase 4a):
	// CONTEXT_RETRIEVAL_MAX, default 0 (tier disabled). 0..50 inclusive is
	// accepted at parse time (0 = disabled); anything else keeps the
	// default, matching the silent-keep behaviour of the sibling context
	// knobs. A programmatically-built Config outside 0..50 is rejected by
	// Validate().
	ContextRetrievalMax int

	// ContextBudgetPercent is the percentage of the SELECTED model's context
	// window used as the default compilation budget (GAP-080 phase 2a):
	// CONTEXT_BUDGET_PERCENT, default 60. It is a ceiling-free default — the
	// derived budget replaces ContextDefaultBudget only when the model is
	// known to the gateway's model catalog. 0 DISABLES the window-derived
	// path entirely, so the budget is always ContextDefaultBudget. Out of
	// range or non-numeric values keep the default at parse time; a
	// programmatically-built Config outside 0..100 is rejected by Validate().
	ContextBudgetPercent int

	// ContextModelWindows declares the context window of a model for which
	// the gateway reports none (GAP-080 phase 2a follow-up —
	// CONTEXT_MODEL_WINDOWS). The live Hermes gateway answers /v1/models
	// with an OpenAI-style envelope that carries no context_length, so the
	// window-derived budget above is INERT on this deployment and every
	// call falls back to ContextDefaultBudget; this knob is how an operator
	// declares the window locally.
	//
	// Format: comma-separated `model=window` pairs, e.g.
	//
	//	CONTEXT_MODEL_WINDOWS="Hermes Agent=200000,probe-big=128000"
	//
	// Whitespace around a pair, around the model name and around the number
	// is ignored, the LAST `=` of a pair separates the two (a model id may
	// itself contain `=`), and a later pair for the same model replaces an
	// earlier one. A window the gateway actually reports always wins over
	// the declaration here, so this can only ever fill a gap. Unset or
	// empty means "no overrides", which leaves the derivation exactly as it
	// behaves without the knob. A malformed pair (no `=`, a blank model
	// name, a non-integer window, a window <= 0) is a STARTUP ERROR from
	// Validate() — a typo must fail loudly, never silently do nothing.
	ContextModelWindows map[string]int

	// contextModelWindowsErr is the failure FromEnv() hit while parsing
	// CONTEXT_MODEL_WINDOWS (the message names the offending entry and the
	// raw value). FromEnv() parses the knob once so the map above is usable
	// immediately; carrying the error here is what lets Validate() refuse
	// the startup instead of leaving the operator to wonder why the
	// derivation stayed inert.
	contextModelWindowsErr error

	// Plugin sandbox (GAP-002 §4.1)
	PluginMaxSize int // PLUGIN_MAX_SIZE, default 1048576 (1MB)

	// Hermes gateway (GAP-050 — live gateway client)
	GatewayBaseURL string // HERMES_WEBUI_GATEWAY_BASE_URL, default http://127.0.0.1:8642
	GatewayAPIKey  string // HERMES_WEBUI_GATEWAY_API_KEY, fallback API_SERVER_KEY
	NATSURL        string
	NATSCreds      string

	// File viewer storage (SPEC-PL-02 §7.6). CANOPY_FILE_ROOT, default
	// ~/.canopy/files. Uploaded file bytes live at <root>/files/<aa>/<sha256>
	// where <aa> is the first two hex chars of the content SHA-256.
	FileRoot string

	// TrustedProxies lists the CIDR prefixes of reverse proxies whose
	// X-Forwarded-For header may be trusted when deriving the client IP.
	// Empty (the default) means trust no proxy: XFF is ignored and the TCP
	// peer address is authoritative. Required for rate limiting to bucket
	// real clients when canopyd runs behind a reverse proxy
	// (CANOPY_TRUSTED_PROXIES, comma-separated).
	TrustedProxies []string
}

// DSN returns the PostgreSQL connection string.
func (c *Config) DSN() string {
	sslmode := c.DBSSLMode
	if sslmode == "" {
		sslmode = "disable"
	}
	schema := c.DBSchema
	if schema == "" {
		schema = "public"
	}
	return "postgres://" + c.DBUser + ":" + c.DBPassword +
		"@" + c.DBHost + ":" + strconv.Itoa(c.DBPort) +
		"/" + c.DBName + "?sslmode=" + sslmode +
		"&search_path=" + schema
}

// Default returns a Config with sensible development defaults.
func Default() *Config {
	return &Config{
		DBHost:               "localhost",
		DBPort:               5432,
		DBUser:               "canopy",
		DBPassword:           "canopy",
		DBName:               "canopy",
		DBSSLMode:            "disable",
		DBSchema:             "public",
		DBDriver:             "postgres",
		SQLitePath:           defaultSQLitePath(),
		HTTPAddr:             ":8080",
		LogLevel:             "info",
		LogFormat:            "text",
		CORSOrigin:           "*",
		JWTSecret:            "dev-secret-change-me",
		MetricsEnabled:       false,
		ContextMaxAncestors:  50,
		ContextMaxRefs:       5,
		ContextDefaultBudget: 8000,
		ContextRetrievalMax:  0,
		ContextBudgetPercent: 60,
		PluginMaxSize:        1048576,
		GatewayBaseURL:       "http://127.0.0.1:8642",
	}
}

// parseContextModelWindows parses the CONTEXT_MODEL_WINDOWS value: a
// comma-separated list of `model=window` pairs (GAP-080 phase 2a follow-up).
//
// It is strict on purpose — a typo in this knob has to fail loudly at startup
// rather than leave the window-derived budget silently inert, which is the
// exact failure the knob exists to fix:
//
//   - an empty (or whitespace-only) value means "no overrides" and is NOT an
//     error: the caller left the knob alone;
//   - an empty entry (a stray or doubled comma) is an error;
//   - a pair with no `=` at all is an error;
//   - the LAST `=` separates model from window, so a model id may contain
//     `=` (`weird=model=1000` declares the model "weird=model");
//   - a blank model name after trimming is an error;
//   - a window that is not an integer, or is <= 0, is an error.
//
// It returns a nil map together with the error, so a rejected value can never
// half-populate the overrides.
func parseContextModelWindows(raw string) (map[string]int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	windows := make(map[string]int)
	for _, pair := range strings.Split(trimmed, ",") {
		entry := strings.TrimSpace(pair)
		if entry == "" {
			return nil, fmt.Errorf("config: CONTEXT_MODEL_WINDOWS %q has an empty entry (a stray comma?)", raw)
		}
		sep := strings.LastIndex(entry, "=")
		if sep < 0 {
			return nil, fmt.Errorf("config: CONTEXT_MODEL_WINDOWS entry %q is not a model=window pair", entry)
		}
		model := strings.TrimSpace(entry[:sep])
		if model == "" {
			return nil, fmt.Errorf("config: CONTEXT_MODEL_WINDOWS entry %q has an empty model name", entry)
		}
		value := strings.TrimSpace(entry[sep+1:])
		window, err := strconv.Atoi(value)
		if err != nil {
			return nil, fmt.Errorf("config: CONTEXT_MODEL_WINDOWS entry %q has a non-integer window %q", entry, value)
		}
		if window <= 0 {
			return nil, fmt.Errorf("config: CONTEXT_MODEL_WINDOWS entry %q has a non-positive window %d", entry, window)
		}
		windows[model] = window
	}
	return windows, nil
}

// defaultSQLitePath returns the conventional canopy runtime database path.
// It is deliberately derived without consulting CANOPY_SQLITE_PATH so Default
// remains a stable, side-effect-free baseline for tests and callers.
func defaultSQLitePath() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".canopy", "canopy.sqlite")
	}
	return filepath.Join(".canopy", "canopy.sqlite")
}

// parseDBDriver returns the configured backend, preserving an explicit value so
// Validate can reject typos rather than silently falling back to PostgreSQL.
func parseDBDriver(raw string) string {
	if raw == "" {
		return "postgres"
	}
	return raw
}

// FromEnv loads configuration from environment variables,
// falling back to Default() values when unset.
func FromEnv() *Config {
	c := Default()
	c.DBDriver = parseDBDriver(os.Getenv("CANOPY_DB_DRIVER"))
	if v := os.Getenv("CANOPY_SQLITE_PATH"); v != "" {
		c.SQLitePath = v
	}
	if v := os.Getenv("DB_HOST"); v != "" {
		c.DBHost = v
	}
	if v := os.Getenv("DB_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.DBPort = p
		}
	}
	if v := os.Getenv("DB_USER"); v != "" {
		c.DBUser = v
	}
	if v := os.Getenv("DB_PASSWORD"); v != "" {
		c.DBPassword = v
	}
	if v := os.Getenv("DB_NAME"); v != "" {
		c.DBName = v
	}
	if v := os.Getenv("DB_SSLMODE"); v != "" {
		c.DBSSLMode = v
	}
	if v := os.Getenv("HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		c.LogFormat = v
	}
	if v := os.Getenv("CORS_ORIGIN"); v != "" {
		c.CORSOrigin = v
	}
	if v := os.Getenv("DB_SCHEMA"); v != "" {
		c.DBSchema = v
	}
	if v := os.Getenv("JWT_SECRET"); v != "" {
		c.JWTSecret = v
	}
	if v := os.Getenv("REFERENCE_SELECTION_SECRET"); v != "" {
		c.ReferenceSelectionSecret = v
	}
	if v := os.Getenv("METRICS_ENABLED"); v == "true" || v == "1" {
		c.MetricsEnabled = true
	}
	if v := os.Getenv("CONTEXT_MAX_ANCESTORS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ContextMaxAncestors = n
		}
	}
	if v := os.Getenv("CONTEXT_MAX_REFS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ContextMaxRefs = n
		}
	}
	if v := os.Getenv("CONTEXT_DEFAULT_BUDGET"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.ContextDefaultBudget = n
		}
	}
	// CONTEXT_BUDGET_PERCENT (GAP-080 phase 2a): the window-derived default
	// budget as a percentage of the selected model's context window. 0..100
	// inclusive is accepted (0 = disabled, i.e. always the flat default);
	// anything else keeps the default 60, matching the silent-keep behaviour
	// of the sibling context knobs above.
	if v := os.Getenv("CONTEXT_BUDGET_PERCENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 100 {
			c.ContextBudgetPercent = n
		}
	}
	// CONTEXT_RETRIEVAL_MAX (GAP-080 phase 4a): max topic-search results the
	// retrieved tier may fold into a payload. 0..50 inclusive is accepted
	// (0 = tier disabled); anything else keeps the default 0, matching the
	// silent-keep behaviour of the sibling context knobs.
	if v := os.Getenv("CONTEXT_RETRIEVAL_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 50 {
			c.ContextRetrievalMax = n
		}
	}
	// CONTEXT_MODEL_WINDOWS (GAP-080 phase 2a follow-up): locally declared
	// context windows for models the gateway reports no window for. Unset
	// or empty means "no overrides". A parse failure is kept on the Config
	// so Validate() can refuse the startup: a typo in this knob must be
	// loud, not an inert derivation.
	if v := os.Getenv("CONTEXT_MODEL_WINDOWS"); v != "" {
		windows, err := parseContextModelWindows(v)
		c.contextModelWindowsErr = err
		c.ContextModelWindows = windows
	}
	if v := os.Getenv("PLUGIN_MAX_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			// Negative values are rejected at startup (Validate); zero falls
			// back to the 1MB default.
			if n > 0 {
				c.PluginMaxSize = n
			}
		}
	}

	// Hermes gateway (GAP-050). base_url defaults to the hermes-webui
	// convention (http://127.0.0.1:8642); the API key comes from
	// HERMES_WEBUI_GATEWAY_API_KEY with API_SERVER_KEY as fallback so a
	// canopyd running next to a `hermes gateway run` picks up the key with
	// no extra configuration.
	if v := os.Getenv("HERMES_WEBUI_GATEWAY_BASE_URL"); v != "" {
		c.GatewayBaseURL = v
	}
	if v := os.Getenv("HERMES_WEBUI_GATEWAY_API_KEY"); v != "" {
		c.GatewayAPIKey = v
	} else if v := os.Getenv("API_SERVER_KEY"); v != "" {
		c.GatewayAPIKey = v
	}

	// CANOPY_DB_URL overrides all individual DB_* fields when set.
	// This matches the documented env var in SELF_HOST.md and avoids
	// silent misconfiguration when users set CANOPY_DB_URL expecting
	// it to take effect.
	if v := os.Getenv("CANOPY_DB_URL"); v != "" {
		dsn := v
		// postgres://user:pass@host:port/dbname?sslmode=...
		if len(dsn) > 11 && dsn[:11] == "postgres://" {
			rest := dsn[11:]
			// user:password@host:port/dbname?params
			if atIdx := strings.Index(rest, "@"); atIdx > 0 {
				userInfo := rest[:atIdx]
				hostPart := rest[atIdx+1:]
				if colonIdx := strings.Index(userInfo, ":"); colonIdx > 0 {
					c.DBUser = userInfo[:colonIdx]
					c.DBPassword = userInfo[colonIdx+1:]
				} else {
					c.DBUser = userInfo
				}
				// host:port/dbname?params
				if slashIdx := strings.Index(hostPart, "/"); slashIdx > 0 {
					hostPort := hostPart[:slashIdx]
					dbPart := hostPart[slashIdx+1:]
					if colonIdx := strings.Index(hostPort, ":"); colonIdx > 0 {
						c.DBHost = hostPort[:colonIdx]
						if p, err := strconv.Atoi(hostPort[colonIdx+1:]); err == nil {
							c.DBPort = p
						}
					} else {
						c.DBHost = hostPort
					}
					// dbname?params
					if qIdx := strings.Index(dbPart, "?"); qIdx > 0 {
						c.DBName = dbPart[:qIdx]
						params := dbPart[qIdx+1:]
						for _, pair := range strings.Split(params, "&") {
							if kv := strings.SplitN(pair, "=", 2); len(kv) == 2 && kv[0] == "sslmode" {
								c.DBSSLMode = kv[1]
							}
						}
					} else {
						c.DBName = dbPart
					}
				}
			}
		}
	}
	c.NATSURL = os.Getenv("CANOPY_NATS_URL")
	c.NATSCreds = os.Getenv("CANOPY_NATS_CREDS")

	// File viewer storage root (SPEC-PL-02 §7.6): CANOPY_FILE_ROOT, default
	// ~/.canopy/files. Resolved eagerly so the effective path is visible in
	// logs and validation.
	if v := os.Getenv("CANOPY_FILE_ROOT"); v != "" {
		c.FileRoot = v
	} else {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			c.FileRoot = filepath.Join(home, ".canopy", "files")
		} else {
			c.FileRoot = ".canopy/files"
		}
	}

	// Trusted proxies: comma-separated CIDR prefixes of reverse proxies
	// whose X-Forwarded-For may be trusted. Empty (unset) = trust no proxy
	// (fail-closed). Entries are validated in Validate(), which fails loud
	// at startup instead of panicking inside chi's ClientIPFromXFF.
	if v := os.Getenv("CANOPY_TRUSTED_PROXIES"); v != "" {
		var proxies []string
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				proxies = append(proxies, item)
			}
		}
		c.TrustedProxies = proxies
	}
	return c
}

// Validate checks configuration invariants that must fail fast at startup.
// A negative PLUGIN_MAX_SIZE is a hard error (GAP-002 §4.1); zero falls back
// to the 1MB default in FromEnv. A CONTEXT_BUDGET_PERCENT outside 0..100 is
// likewise a hard error (GAP-080 phase 2a) — FromEnv already ignores an
// out-of-range env value, so reaching Validate() with one means the Config was
// built in code. A malformed CONTEXT_MODEL_WINDOWS is a hard error too
// (GAP-080 phase 2a follow-up): the knob exists to make the window-derived
// budget activate, so a typo that silently resolved to no overrides would
// reproduce the very inertness it was added to remove. TrustedProxies entries
// must each parse as a valid CIDR — a malformed entry is a startup error
// rather than a panic inside chi's ClientIPFromXFF (which panics on invalid
// prefixes).
func (c *Config) Validate() error {
	if c.DBDriver != "postgres" && c.DBDriver != "sqlite" {
		return fmt.Errorf("config: CANOPY_DB_DRIVER must be postgres or sqlite (got %q)", c.DBDriver)
	}
	if c.DBDriver == "sqlite" && strings.TrimSpace(c.SQLitePath) == "" {
		return fmt.Errorf("config: CANOPY_SQLITE_PATH must not be empty when CANOPY_DB_DRIVER=sqlite")
	}
	if c.PluginMaxSize < 0 {
		return fmt.Errorf("config: PLUGIN_MAX_SIZE must not be negative (got %d)", c.PluginMaxSize)
	}
	// GAP-080 phase 2a: CONTEXT_BUDGET_PERCENT is a percentage and is never
	// silently clamped here — an out-of-range value on a programmatically
	// built Config is a programming error, not a user preference.
	if c.ContextBudgetPercent < 0 || c.ContextBudgetPercent > 100 {
		return fmt.Errorf("config: CONTEXT_BUDGET_PERCENT must be between 0 and 100 (got %d)", c.ContextBudgetPercent)
	}
	// GAP-080 phase 4a: same posture for CONTEXT_RETRIEVAL_MAX — FromEnv
	// already ignores an out-of-range env value, so reaching Validate()
	// with one means the Config was built in code.
	if c.ContextRetrievalMax < 0 || c.ContextRetrievalMax > 50 {
		return fmt.Errorf("config: CONTEXT_RETRIEVAL_MAX must be between 0 and 50 (got %d)", c.ContextRetrievalMax)
	}
	// GAP-080 phase 2a follow-up: the parse failure FromEnv() recorded for
	// CONTEXT_MODEL_WINDOWS, reported here so the server refuses to start.
	if c.contextModelWindowsErr != nil {
		return c.contextModelWindowsErr
	}
	// The same knob built in code has no env value to re-parse, so the map
	// itself is checked. Keys are sorted so a Config with several bad
	// entries always reports the same one.
	for _, model := range sortedKeys(c.ContextModelWindows) {
		window := c.ContextModelWindows[model]
		if strings.TrimSpace(model) == "" {
			return fmt.Errorf("config: CONTEXT_MODEL_WINDOWS has an empty model name")
		}
		if window <= 0 {
			return fmt.Errorf("config: CONTEXT_MODEL_WINDOWS entry %q has a non-positive window %d", model, window)
		}
	}
	for _, p := range c.TrustedProxies {
		if _, _, err := net.ParseCIDR(p); err != nil {
			return fmt.Errorf("config: CANOPY_TRUSTED_PROXIES entry %q is not a valid CIDR: %w", p, err)
		}
	}
	return nil
}

// sortedKeys returns a map's keys in ascending order. It exists for
// deterministic validation errors: a map range would otherwise pick an
// arbitrary offending entry to report.
func sortedKeys(m map[string]int) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
