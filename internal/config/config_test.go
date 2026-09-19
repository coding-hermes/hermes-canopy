package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultJWTSecret(t *testing.T) {
	if got := Default().JWTSecret; got != "dev-secret-change-me" {
		t.Fatalf("Default().JWTSecret = %q, want development default", got)
	}
}

func TestFromEnvJWTSecret(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	if got := FromEnv().JWTSecret; got != "test-secret" {
		t.Fatalf("FromEnv().JWTSecret = %q, want %q", got, "test-secret")
	}
}

func TestFromEnvNATSOptionalWiring(t *testing.T) {
	t.Setenv("CANOPY_NATS_URL", "")
	t.Setenv("CANOPY_NATS_CREDS", "")
	c := FromEnv()
	if c.NATSURL != "" || c.NATSCreds != "" {
		t.Fatalf("unset NATS environment enabled wiring: %#v", c)
	}

	t.Setenv("CANOPY_NATS_URL", "nats://example:4222")
	t.Setenv("CANOPY_NATS_CREDS", "/run/secrets/nats.creds")
	c = FromEnv()
	if c.NATSURL != "nats://example:4222" || c.NATSCreds != "/run/secrets/nats.creds" {
		t.Fatalf("NATS environment was not mapped: URL=%q creds=%q", c.NATSURL, c.NATSCreds)
	}
}

func TestFromEnv_CANOPY_DB_URL(t *testing.T) {
	t.Setenv("CANOPY_DB_URL", "postgres://myuser:mypass@myhost:5433/mydb?sslmode=require")
	c := FromEnv()
	if c.DBHost != "myhost" {
		t.Fatalf("DBHost = %q, want %q", c.DBHost, "myhost")
	}
	if c.DBPort != 5433 {
		t.Fatalf("DBPort = %d, want %d", c.DBPort, 5433)
	}
	if c.DBUser != "myuser" {
		t.Fatalf("DBUser = %q, want %q", c.DBUser, "myuser")
	}
	if c.DBPassword != "mypass" {
		t.Fatalf("DBPassword = %q, want %q", c.DBPassword, "mypass")
	}
	if c.DBName != "mydb" {
		t.Fatalf("DBName = %q, want %q", c.DBName, "mydb")
	}
	if c.DBSSLMode != "require" {
		t.Fatalf("DBSSLMode = %q, want %q", c.DBSSLMode, "require")
	}
}

func TestFromEnv_CANOPY_DB_URL_Empty(t *testing.T) {
	// When CANOPY_DB_URL is not set, individual env vars still work.
	t.Setenv("DB_HOST", "otherhost")
	t.Setenv("DB_PORT", "5444")
	c := FromEnv()
	if c.DBHost != "otherhost" {
		t.Fatalf("DBHost = %q, want %q", c.DBHost, "otherhost")
	}
	if c.DBPort != 5444 {
		t.Fatalf("DBPort = %d, want %d", c.DBPort, 5444)
	}
}

func TestDefaultDBSchema(t *testing.T) {
	if got := Default().DBSchema; got != "public" {
		t.Fatalf("Default().DBSchema = %q, want %q", got, "public")
	}
}

func TestFromEnvDBSchema(t *testing.T) {
	t.Setenv("DB_SCHEMA", "myschema")
	if got := FromEnv().DBSchema; got != "myschema" {
		t.Fatalf("FromEnv().DBSchema = %q, want %q", got, "myschema")
	}
}

func TestDSNIncludesSearchPath(t *testing.T) {
	c := Default()
	c.DBSchema = "myschema"
	dsn := c.DSN()
	if !strings.Contains(dsn, "search_path=myschema") {
		t.Fatalf("DSN() = %q, want search_path=myschema", dsn)
	}
	// Default schema should also appear in the DSN.
	c2 := Default()
	dsn2 := c2.DSN()
	if !strings.Contains(dsn2, "search_path=public") {
		t.Fatalf("DSN() = %q, want search_path=public", dsn2)
	}
}

func TestDefaultLogFormat(t *testing.T) {
	if got := Default().LogFormat; got != "text" {
		t.Fatalf("Default().LogFormat = %q, want %q", got, "text")
	}
}

func TestFromEnvLogFormat(t *testing.T) {
	t.Setenv("LOG_FORMAT", "json")
	if got := FromEnv().LogFormat; got != "json" {
		t.Fatalf("FromEnv().LogFormat = %q, want %q", got, "json")
	}
}

func TestDefaultCORSOrigin(t *testing.T) {
	if got := Default().CORSOrigin; got != "*" {
		t.Fatalf("Default().CORSOrigin = %q, want %q", got, "*")
	}
}

func TestFromEnvCORSOrigin(t *testing.T) {
	t.Setenv("CORS_ORIGIN", "http://example.com")
	if got := FromEnv().CORSOrigin; got != "http://example.com" {
		t.Fatalf("FromEnv().CORSOrigin = %q, want %q", got, "http://example.com")
	}
}

func TestDefaultGatewayConfig(t *testing.T) {
	c := Default()
	if c.GatewayBaseURL != "http://127.0.0.1:8642" {
		t.Fatalf("Default().GatewayBaseURL = %q, want http://127.0.0.1:8642", c.GatewayBaseURL)
	}
	if c.GatewayAPIKey != "" {
		t.Fatalf("Default().GatewayAPIKey = %q, want empty", c.GatewayAPIKey)
	}
}

func TestFromEnvGatewayConfig(t *testing.T) {
	t.Setenv("HERMES_WEBUI_GATEWAY_BASE_URL", "http://example.com:9999")
	t.Setenv("HERMES_WEBUI_GATEWAY_API_KEY", "webui-key")
	c := FromEnv()
	if c.GatewayBaseURL != "http://example.com:9999" {
		t.Fatalf("GatewayBaseURL = %q, want env override", c.GatewayBaseURL)
	}
	if c.GatewayAPIKey != "webui-key" {
		t.Fatalf("GatewayAPIKey = %q, want webui-key", c.GatewayAPIKey)
	}
}

func TestFromEnvGatewayAPIKeyFallsBackToAPIServerKey(t *testing.T) {
	t.Setenv("HERMES_WEBUI_GATEWAY_BASE_URL", "")
	t.Setenv("HERMES_WEBUI_GATEWAY_API_KEY", "")
	t.Setenv("API_SERVER_KEY", "api-server-key")
	c := FromEnv()
	if c.GatewayAPIKey != "api-server-key" {
		t.Fatalf("GatewayAPIKey = %q, want API_SERVER_KEY fallback", c.GatewayAPIKey)
	}
}

func TestDefaultTrustedProxiesEmpty(t *testing.T) {
	// Fail-closed: no trusted proxies by default, XFF is ignored.
	if got := Default().TrustedProxies; len(got) != 0 {
		t.Fatalf("Default().TrustedProxies = %v, want empty", got)
	}
}

func TestFromEnvTrustedProxiesUnsetIsEmpty(t *testing.T) {
	t.Setenv("CANOPY_TRUSTED_PROXIES", "")
	c := FromEnv()
	if len(c.TrustedProxies) != 0 {
		t.Fatalf("FromEnv().TrustedProxies = %v, want empty when env unset", c.TrustedProxies)
	}
}

func TestFromEnvTrustedProxiesParsesListAndTrimsWhitespace(t *testing.T) {
	t.Setenv("CANOPY_TRUSTED_PROXIES", " 10.0.0.0/8 , 192.168.0.0/16 ,,")
	c := FromEnv()
	want := []string{"10.0.0.0/8", "192.168.0.0/16"}
	if len(c.TrustedProxies) != len(want) {
		t.Fatalf("FromEnv().TrustedProxies = %v, want %v", c.TrustedProxies, want)
	}
	for i, w := range want {
		if c.TrustedProxies[i] != w {
			t.Fatalf("TrustedProxies[%d] = %q, want %q", i, c.TrustedProxies[i], w)
		}
	}
}

// --- CONTEXT_BUDGET_PERCENT (GAP-080 phase 2a) ----------------------------

func TestContextBudgetPercentDefault(t *testing.T) {
	if got := Default().ContextBudgetPercent; got != 60 {
		t.Fatalf("Default().ContextBudgetPercent = %d, want 60", got)
	}
}

// TestContextBudgetPercentFromEnv pins the parse contract: 0..100 inclusive is
// taken as given (0 means "window-derived path disabled", never "unset"), and
// every out-of-range or non-numeric value silently keeps the default 60 —
// matching the sibling context knobs, which never error at parse time.
func TestContextBudgetPercentFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset keeps the default", "", 60},
		{"explicit 60", "60", 60},
		{"zero is preserved (window path disabled)", "0", 0},
		{"100 is the inclusive upper bound", "100", 100},
		{"above range keeps the default", "150", 60},
		{"negative keeps the default", "-1", 60},
		{"non-numeric keeps the default", "abc", 60},
		{"blank keeps the default", " ", 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT_BUDGET_PERCENT", tc.env)
			if got := FromEnv().ContextBudgetPercent; got != tc.want {
				t.Fatalf("FromEnv() with CONTEXT_BUDGET_PERCENT=%q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

// TestValidateContextBudgetPercent pins the other half of the contract: a
// value FromEnv would have ignored is a hard error on a Config built in code.
func TestValidateContextBudgetPercent(t *testing.T) {
	for _, pct := range []int{0, 1, 60, 100} {
		c := Default()
		c.ContextBudgetPercent = pct
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate() with CONTEXT_BUDGET_PERCENT=%d = %v, want nil", pct, err)
		}
	}
	for _, pct := range []int{-1, 101, 1000} {
		c := Default()
		c.ContextBudgetPercent = pct
		err := c.Validate()
		if err == nil {
			t.Fatalf("Validate() with CONTEXT_BUDGET_PERCENT=%d = nil, want error", pct)
		}
		if !strings.Contains(err.Error(), "CONTEXT_BUDGET_PERCENT") {
			t.Fatalf("Validate() error %q does not name CONTEXT_BUDGET_PERCENT", err)
		}
	}
}

func TestValidateTrustedProxies(t *testing.T) {
	valid := Default()
	valid.TrustedProxies = []string{"10.0.0.0/8", "192.168.0.0/16", "2001:db8::/32"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() with valid CIDRs = %v, want nil", err)
	}

	for _, bad := range []string{"10.0.0.0/99", "not-a-cidr"} {
		c := Default()
		c.TrustedProxies = []string{"10.0.0.0/8", bad}
		err := c.Validate()
		if err == nil {
			t.Fatalf("Validate() with %q = nil, want error", bad)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Fatalf("Validate() error %q does not name offending entry %q", err, bad)
		}
	}
}

// --- CONTEXT_MODEL_WINDOWS (GAP-080 phase 2a follow-up) ----------------------

// TestFromEnvContextModelWindowsParsesPairs pins the knob's documented format:
// comma-separated `model=window` pairs, a model name that may contain spaces,
// whitespace trimmed around the pair, the name and the number, and the LAST
// `=` of a pair as the separator (a model id may itself contain `=`).
func TestFromEnvContextModelWindowsParsesPairs(t *testing.T) {
	t.Setenv("CONTEXT_MODEL_WINDOWS", "Hermes Agent=200000,probe-big=128000")
	c := FromEnv()
	want := map[string]int{"Hermes Agent": 200000, "probe-big": 128000}
	if !reflect.DeepEqual(c.ContextModelWindows, want) {
		t.Fatalf("ContextModelWindows = %#v, want %#v", c.ContextModelWindows, want)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with a well-formed knob = %v, want nil", err)
	}

	// Trimming, a model id containing '=', and a duplicate (last wins).
	t.Setenv("CONTEXT_MODEL_WINDOWS", "  Hermes Agent = 200000 , weird=model=4096 ,   probe-big=1000  ,probe-big=128000")
	c = FromEnv()
	want = map[string]int{
		"Hermes Agent": 200000,
		"weird=model":  4096,
		"probe-big":    128000,
	}
	if !reflect.DeepEqual(c.ContextModelWindows, want) {
		t.Fatalf("ContextModelWindows = %#v, want %#v", c.ContextModelWindows, want)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with a padded/duplicate list = %v, want nil", err)
	}
}

// TestFromEnvContextModelWindowsUnsetMeansNoOverrides pins the empty case: an
// unset, empty or blank value is "no overrides", NOT an error — the knob is
// optional and leaving it alone must leave the derivation exactly as it was.
func TestFromEnvContextModelWindowsUnsetMeansNoOverrides(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		unset bool
	}{
		{"unset", "", true},
		{"empty", "", false},
		{"blank", "   ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT_MODEL_WINDOWS", tc.value)
			if tc.unset {
				if err := os.Unsetenv("CONTEXT_MODEL_WINDOWS"); err != nil {
					t.Fatal(err)
				}
			}
			c := FromEnv()
			if len(c.ContextModelWindows) != 0 {
				t.Fatalf("ContextModelWindows = %#v, want no overrides", c.ContextModelWindows)
			}
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate() with no overrides = %v, want nil", err)
			}
		})
	}
}

// TestValidateContextModelWindowsFailsLoudly is the point of the knob's
// strictness: a typo must refuse the startup instead of silently resolving to
// no overrides, which would reproduce the inert derivation the knob exists to
// fix. Each malformed form is asserted through the real startup path
// (FromEnv -> Validate).
func TestValidateContextModelWindowsFailsLoudly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"a pair with no =", "Hermes Agent"},
		{"a blank model name", "=200000"},
		{"a whitespace-only model name", "   =200000"},
		{"a non-integer window", "probe-big=128k"},
		{"a zero window", "probe-big=0"},
		{"a negative window", "probe-big=-5"},
		{"an empty entry after a comma", "probe-big=128000,,probe-small=1000"},
		{"a trailing comma", "probe-big=128000,"},
		{"a non-numeric window after a valid pair", "ok=1000,bad=abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT_MODEL_WINDOWS", tc.raw)
			c := FromEnv()
			if len(c.ContextModelWindows) != 0 {
				t.Fatalf("a rejected knob must not half-populate the overrides: %#v", c.ContextModelWindows)
			}
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() with %q = nil, want an error", tc.raw)
			}
			if !strings.Contains(err.Error(), "CONTEXT_MODEL_WINDOWS") {
				t.Fatalf("Validate() error %q does not name CONTEXT_MODEL_WINDOWS", err)
			}
		})
	}
}

// TestValidateContextModelWindowsProgrammatic covers the same knob built in
// code, which has no env value to re-parse: the map itself is checked, so a
// non-positive window or a blank model name can never reach the catalog.
func TestValidateContextModelWindowsProgrammatic(t *testing.T) {
	valid := Default()
	valid.ContextModelWindows = map[string]int{"Hermes Agent": 200000, "probe-big": 128000}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() with well-formed overrides = %v, want nil", err)
	}

	none := Default()
	if none.ContextModelWindows != nil {
		t.Fatalf("Default().ContextModelWindows = %#v, want nil", none.ContextModelWindows)
	}
	if err := none.Validate(); err != nil {
		t.Fatalf("Validate() with no overrides = %v, want nil", err)
	}

	zero := Default()
	zero.ContextModelWindows = map[string]int{"probe-big": 0}
	if err := zero.Validate(); err == nil || !strings.Contains(err.Error(), "CONTEXT_MODEL_WINDOWS") {
		t.Fatalf("Validate() with a zero window = %v, want an error naming CONTEXT_MODEL_WINDOWS", err)
	}

	negative := Default()
	negative.ContextModelWindows = map[string]int{"probe-big": -1}
	if err := negative.Validate(); err == nil || !strings.Contains(err.Error(), "CONTEXT_MODEL_WINDOWS") {
		t.Fatalf("Validate() with a negative window = %v, want an error naming CONTEXT_MODEL_WINDOWS", err)
	}

	blank := Default()
	blank.ContextModelWindows = map[string]int{"   ": 200000}
	if err := blank.Validate(); err == nil || !strings.Contains(err.Error(), "CONTEXT_MODEL_WINDOWS") {
		t.Fatalf("Validate() with a blank model name = %v, want an error naming CONTEXT_MODEL_WINDOWS", err)
	}
}

// --- CONTEXT_RETRIEVAL_MAX (GAP-080 phase 4a) -------------------------------

func TestContextRetrievalMaxDefault(t *testing.T) {
	if got := Default().ContextRetrievalMax; got != 0 {
		t.Fatalf("Default().ContextRetrievalMax = %d, want 0 (tier disabled)", got)
	}
}

func TestFromEnvContextRetrievalMax(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset keeps the default", "", 0},
		{"zero is accepted (explicitly disabled)", "0", 0},
		{"one is accepted", "1", 1},
		{"mid-range is accepted", "25", 25},
		{"upper bound is accepted", "50", 50},
		{"above range keeps the default", "51", 0},
		{"negative keeps the default", "-1", 0},
		{"non-numeric keeps the default", "abc", 0},
		{"blank keeps the default", " ", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT_RETRIEVAL_MAX", tc.env)
			if got := FromEnv().ContextRetrievalMax; got != tc.want {
				t.Fatalf("FromEnv() with CONTEXT_RETRIEVAL_MAX=%q = %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}

// TestValidateContextRetrievalMax pins the other half of the contract: a
// value FromEnv would have ignored is a hard error on a Config built in code.
func TestValidateContextRetrievalMax(t *testing.T) {
	for _, n := range []int{0, 1, 25, 50} {
		c := Default()
		c.ContextRetrievalMax = n
		if err := c.Validate(); err != nil {
			t.Fatalf("Validate() with CONTEXT_RETRIEVAL_MAX=%d = %v, want nil", n, err)
		}
	}
	for _, n := range []int{-1, 51, 100} {
		c := Default()
		c.ContextRetrievalMax = n
		err := c.Validate()
		if err == nil {
			t.Fatalf("Validate() with CONTEXT_RETRIEVAL_MAX=%d = nil, want error", n)
		}
		if !strings.Contains(err.Error(), "CONTEXT_RETRIEVAL_MAX") {
			t.Fatalf("Validate() error %q does not name CONTEXT_RETRIEVAL_MAX", err)
		}
	}
}
