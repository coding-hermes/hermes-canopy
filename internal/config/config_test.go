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
// taken as given (0 means "window-derived path disabled", never "unset"),
// unset/blank falls back to the default 60, and every malformed or
// out-of-range value is STRICTLY rejected via Validate() (QA-41) — the field
// keeps the default, and Validate() names the env var and value.
func TestContextBudgetPercentFromEnv(t *testing.T) {
	cases := []struct {
		name  string
		env   string
		want  int
		valid bool
	}{
		{"unset keeps the default", "", 60, true},
		{"explicit 60", "60", 60, true},
		{"zero is preserved (window path disabled)", "0", 0, true},
		{"100 is the inclusive upper bound", "100", 100, true},
		{"blank keeps the default", " ", 60, false},
		{"above range is rejected", "150", 60, false},
		{"negative is rejected", "-1", 60, false},
		{"non-numeric is rejected", "abc", 60, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT_BUDGET_PERCENT", tc.env)
			c := FromEnv()
			if got := c.ContextBudgetPercent; got != tc.want {
				t.Fatalf("FromEnv() with CONTEXT_BUDGET_PERCENT=%q = %d, want %d", tc.env, got, tc.want)
			}
			err := c.Validate()
			if tc.valid && err != nil {
				t.Fatalf("Validate() with CONTEXT_BUDGET_PERCENT=%q = %v, want nil", tc.env, err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatalf("Validate() with CONTEXT_BUDGET_PERCENT=%q = nil, want error", tc.env)
				}
				if !strings.Contains(err.Error(), "CONTEXT_BUDGET_PERCENT") || !strings.Contains(err.Error(), tc.env) {
					t.Fatalf("Validate() error %q does not name CONTEXT_BUDGET_PERCENT and the raw value %q", err, tc.env)
				}
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
		name  string
		env   string
		want  int
		valid bool
	}{
		{"unset keeps the default", "", 0, true},
		{"zero is accepted (explicitly disabled)", "0", 0, true},
		{"one is accepted", "1", 1, true},
		{"mid-range is accepted", "25", 25, true},
		{"upper bound is accepted", "50", 50, true},
		{"blank keeps the default", " ", 0, false},
		{"above range is rejected", "51", 0, false},
		{"negative is rejected", "-1", 0, false},
		{"non-numeric is rejected", "abc", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTEXT_RETRIEVAL_MAX", tc.env)
			c := FromEnv()
			if got := c.ContextRetrievalMax; got != tc.want {
				t.Fatalf("FromEnv() with CONTEXT_RETRIEVAL_MAX=%q = %d, want %d", tc.env, got, tc.want)
			}
			err := c.Validate()
			if tc.valid && err != nil {
				t.Fatalf("Validate() with CONTEXT_RETRIEVAL_MAX=%q = %v, want nil", tc.env, err)
			}
			if !tc.valid {
				if err == nil {
					t.Fatalf("Validate() with CONTEXT_RETRIEVAL_MAX=%q = nil, want error", tc.env)
				}
				if !strings.Contains(err.Error(), "CONTEXT_RETRIEVAL_MAX") || !strings.Contains(err.Error(), tc.env) {
					t.Fatalf("Validate() error %q does not name CONTEXT_RETRIEVAL_MAX and the raw value %q", err, tc.env)
				}
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

func TestDBDriverDefaultsToPostgres(t *testing.T) {
	t.Setenv("CANOPY_DB_DRIVER", "")
	c := FromEnv()
	if c.DBDriver != "postgres" {
		t.Fatalf("FromEnv().DBDriver = %q, want postgres", c.DBDriver)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("default config validation = %v", err)
	}
}

func TestDBDriverSQLiteAndPath(t *testing.T) {
	t.Setenv("CANOPY_DB_DRIVER", "sqlite")
	t.Setenv("CANOPY_SQLITE_PATH", "/tmp/canopy-gap-097.sqlite")
	c := FromEnv()
	if c.DBDriver != "sqlite" || c.SQLitePath != "/tmp/canopy-gap-097.sqlite" {
		t.Fatalf("SQLite config = driver %q path %q", c.DBDriver, c.SQLitePath)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("SQLite config validation = %v", err)
	}
}

func TestDBDriverRejectsUnknownValue(t *testing.T) {
	c := Default()
	c.DBDriver = "mysql"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "CANOPY_DB_DRIVER") {
		t.Fatalf("Validate(mysql) = %v, want a CANOPY_DB_DRIVER error", err)
	}
}

func TestSQLiteRequiresPath(t *testing.T) {
	c := Default()
	c.DBDriver = "sqlite"
	c.SQLitePath = "  "
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "CANOPY_SQLITE_PATH") {
		t.Fatalf("Validate(SQLite with blank path) = %v, want a CANOPY_SQLITE_PATH error", err)
	}
}

// --- strict numeric env parsing (QA-41) --------------------------------------
//
// FromEnv() must fail loud (via Validate()) on a malformed or out-of-range
// numeric env value instead of silently falling back to the default. The
// table below drives every newly-strict knob through malformed / out-of-range
// / set-and-valid / unset states.

func TestStrictNumericEnvFailFast(t *testing.T) {
	cases := []struct {
		env string
		bad string
		ood string // out-of-range value ("" = no range contract)
	}{
		{"DB_PORT", "abc", ""},
		{"CONTEXT_MAX_ANCESTORS", "abc", "0"},
		{"CONTEXT_MAX_REFS", "abc", "0"},
		{"CONTEXT_DEFAULT_BUDGET", "abc", "0"},
		{"CONTEXT_BUDGET_PERCENT", "abc", "500"},
		{"CONTEXT_RETRIEVAL_MAX", "abc", "500"},
		{"PLUGIN_MAX_SIZE", "abc", "-1"},
	}
	for _, tc := range cases {
		for _, name := range []string{"malformed", "out-of-range"} {
			val := tc.bad
			if name == "out-of-range" {
				if tc.ood == "" {
					continue
				}
				val = tc.ood
			}
			t.Run(tc.env+"/"+name, func(t *testing.T) {
				t.Setenv(tc.env, val)
				c := FromEnv()
				if err := c.Validate(); err == nil {
					t.Fatalf("Validate() with %s=%q = nil, want a startup error", tc.env, val)
				} else {
					if !strings.Contains(err.Error(), tc.env) {
						t.Fatalf("Validate() error %q does not name env var %s", err, tc.env)
					}
					if !strings.Contains(err.Error(), val) {
						t.Fatalf("Validate() error %q does not name the bad value %q", err, val)
					}
				}
			})
		}
	}
}

func TestStrictNumericEnvValidValues(t *testing.T) {
	cases := []struct {
		env  string
		val  string
		want int
	}{
		{"DB_PORT", "5444", 5444},
		{"CONTEXT_MAX_ANCESTORS", "10", 10},
		{"CONTEXT_MAX_REFS", "3", 3},
		{"CONTEXT_DEFAULT_BUDGET", "12000", 12000},
		{"CONTEXT_BUDGET_PERCENT", "0", 0}, // 0 = window-derived path disabled
		{"CONTEXT_BUDGET_PERCENT", "100", 100},
		{"CONTEXT_RETRIEVAL_MAX", "0", 0}, // 0 = tier disabled
		{"CONTEXT_RETRIEVAL_MAX", "50", 50},
		{"PLUGIN_MAX_SIZE", "2097152", 2097152},
	}
	for _, tc := range cases {
		t.Run(tc.env+"="+tc.val, func(t *testing.T) {
			t.Setenv(tc.env, tc.val)
			c := FromEnv()
			if err := c.Validate(); err != nil {
				t.Fatalf("Validate() with %s=%q = %v, want nil", tc.env, tc.val, err)
			}
			var got int
			switch tc.env {
			case "DB_PORT":
				got = c.DBPort
			case "CONTEXT_MAX_ANCESTORS":
				got = c.ContextMaxAncestors
			case "CONTEXT_MAX_REFS":
				got = c.ContextMaxRefs
			case "CONTEXT_DEFAULT_BUDGET":
				got = c.ContextDefaultBudget
			case "CONTEXT_BUDGET_PERCENT":
				got = c.ContextBudgetPercent
			case "CONTEXT_RETRIEVAL_MAX":
				got = c.ContextRetrievalMax
			case "PLUGIN_MAX_SIZE":
				got = c.PluginMaxSize
			}
			if got != tc.want {
				t.Fatalf("%s=%q parsed to %d, want %d", tc.env, tc.val, got, tc.want)
			}
		})
	}
}

// TestStrictNumericEnvUnsetFallsBack pins the documented lenient half: unset
// (or empty) numeric env vars still fall back to the defaults with a clean
// Validate() — strictness applies only to SET values.
func TestStrictNumericEnvUnsetFallsBack(t *testing.T) {
	for _, env := range []string{
		"DB_PORT", "CONTEXT_MAX_ANCESTORS", "CONTEXT_MAX_REFS",
		"CONTEXT_DEFAULT_BUDGET", "CONTEXT_BUDGET_PERCENT",
		"CONTEXT_RETRIEVAL_MAX", "PLUGIN_MAX_SIZE",
	} {
		t.Setenv(env, "")
	}
	c := FromEnv()
	d := Default()
	if c.DBPort != d.DBPort || c.ContextMaxAncestors != d.ContextMaxAncestors ||
		c.ContextMaxRefs != d.ContextMaxRefs || c.ContextDefaultBudget != d.ContextDefaultBudget ||
		c.ContextBudgetPercent != d.ContextBudgetPercent || c.ContextRetrievalMax != d.ContextRetrievalMax ||
		c.PluginMaxSize != d.PluginMaxSize {
		t.Fatalf("empty numeric env vars did not fall back to defaults: got %+v, want defaults %+v", c, d)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with all numeric env vars empty = %v, want nil", err)
	}
}

// TestValidateReturnsEnvParseError pins the mechanism: FromEnv() records the
// parse failure on the Config and Validate() surfaces it, so `canopyd serve`
// exits non-zero with an error naming the env var and the bad value.
func TestValidateReturnsEnvParseError(t *testing.T) {
	t.Setenv("CANOPY_HTTP_PORT", "abc") // not a recognized knob; must be inert
	t.Setenv("DB_PORT", "abc")
	c := FromEnv()
	if got := c.DBPort; got != Default().DBPort {
		t.Fatalf("DBPort = %d after a malformed value, want the default %d", got, Default().DBPort)
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil after a malformed DB_PORT, want an error")
	}
	want := "DB_PORT"
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "abc") {
		t.Fatalf("Validate() error %q does not name %s and the value %q", err, want, "abc")
	}
}

// TestUnrecognizedCanopyEnvVarsInert documents the QA-41 premise fix: there
// is NO CANOPY_CONFIG file knob and no CANOPY_HTTP_PORT — configuration is
// env-only. Setting an unrecognized CANOPY_* variable must neither error nor
// change the config.
func TestUnrecognizedCanopyEnvVarsInert(t *testing.T) {
	t.Setenv("CANOPY_CONFIG", "/etc/canopy.toml")
	t.Setenv("CANOPY_HTTP_PORT", "abc")
	c := FromEnv()
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() with unrecognized CANOPY_CONFIG/CANOPY_HTTP_PORT set = %v, want nil (configuration is env-only; these variables are not recognized)", err)
	}
	if got := c.HTTPAddr; got != Default().HTTPAddr {
		t.Fatalf("HTTPAddr = %q with unrecognized CANOPY_HTTP_PORT set, want the default %q", got, Default().HTTPAddr)
	}
}
