package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// recordingTransport records every request URL the CLI attempts and answers
// from an in-process responder, so no test ever touches a real network.
type recordingTransport struct {
	mu      sync.Mutex
	urls    []string
	respond func(req *http.Request) *http.Response
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.urls = append(t.urls, req.URL.String())
	respond := t.respond
	t.mu.Unlock()
	if respond != nil {
		return respond(req), nil
	}
	// No canned responder: fall through to the real transport (used only by
	// tests that point an explicit target at an httptest server).
	return http.DefaultTransport.RoundTrip(req)
}

func (t *recordingTransport) recorded() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.urls...)
}

// installRecordingHTTPClient swaps the package HTTP client for a client whose
// transport records request URLs, and restores it when the test ends.
func installRecordingHTTPClient(t *testing.T, respond func(req *http.Request) *http.Response) *recordingTransport {
	t.Helper()
	rt := &recordingTransport{respond: respond}
	old := httpClient
	httpClient = &http.Client{Transport: rt}
	t.Cleanup(func() { httpClient = old })
	return rt
}

// jsonResponse builds a canned HTTP response for the recording transport.
func jsonResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}
}

// clearCLITargetEnv blanks every environment variable the CLI target resolver
// consults, so a test observes exactly the variables it sets itself.
func clearCLITargetEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"CANOPY_SERVER_URL", "CANOPY_TOKEN"}, serverOnlyEnvVars...) {
		t.Setenv(name, "")
	}
}

// --- resolver unit tests ------------------------------------------------------

// TestResolveServerURLExplicitWins asserts an explicit CANOPY_SERVER_URL is
// honored — including while DB_* / HTTP_ADDR are set, which is the documented
// isolation path — and that it is normalized (trailing slashes trimmed).
func TestResolveServerURLExplicitWins(t *testing.T) {
	tests := []struct {
		name  string
		value string
		extra map[string]string
		want  string
	}{
		{"plain", "http://localhost:8092", nil, "http://localhost:8092"},
		{"trailing slash", "http://localhost:8092/", nil, "http://localhost:8092"},
		{"https with path", "https://canopy.example.com/api/", nil, "https://canopy.example.com/api"},
		{"ipv6 host", "http://[::1]:8091", nil, "http://[::1]:8091"},
		{
			"wins over DB_PORT", "http://localhost:15440", map[string]string{"DB_PORT": "15440"},
			"http://localhost:15440",
		},
		{
			"wins over HTTP_ADDR and DB_*",
			"http://localhost:18092",
			map[string]string{
				"HTTP_ADDR": ":18092", "DB_HOST": "127.0.0.1", "DB_PORT": "15440",
				"DB_USER": "scratch", "DB_PASSWORD": "scratch", "DB_NAME": "scratch",
			},
			"http://localhost:18092",
		},
		{
			"surrounding whitespace tolerated", "  http://localhost:8093  ",
			map[string]string{"CANOPY_DB_URL": "postgres://u:p@127.0.0.1:15440/scratch"},
			"http://localhost:8093",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCLITargetEnv(t)
			for k, v := range tt.extra {
				t.Setenv(k, v)
			}
			t.Setenv("CANOPY_SERVER_URL", tt.value)

			got, err := resolveServerURL(os.Getenv)
			if err != nil {
				t.Fatalf("resolveServerURL(%q) error = %v, want nil", tt.value, err)
			}
			if got != tt.want {
				t.Errorf("resolveServerURL(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

// TestResolveServerURLInvalidExplicitFailsClosed asserts a malformed explicit
// target is refused with an actionable error instead of silently falling back
// to the default (which would target a different instance).
func TestResolveServerURLInvalidExplicitFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"bare host and port", "localhost:8091"},
		{"scheme only", "http://"},
		{"unsupported scheme", "ftp://canopy.example.com"},
		{"garbage", "not a url"},
		{"scheme-relative", "//localhost:8091"},
		{"empty scheme separator", "://localhost:8091"},
		{"port without host", "http://:8091"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCLITargetEnv(t)
			t.Setenv("CANOPY_SERVER_URL", tt.value)

			got, err := resolveServerURL(os.Getenv)
			if err == nil {
				t.Fatalf("resolveServerURL(%q) error = nil, want a refusal", tt.value)
			}
			if !errors.Is(err, ErrCLITargetUnresolved) {
				t.Errorf("error %v does not wrap ErrCLITargetUnresolved", err)
			}
			if got != "" {
				t.Errorf("resolveServerURL(%q) = %q, want empty (no fallback)", tt.value, got)
			}
			if !strings.Contains(err.Error(), "CANOPY_SERVER_URL") {
				t.Errorf("error does not mention CANOPY_SERVER_URL: %v", err)
			}
			if !strings.Contains(err.Error(), defaultServerURL) {
				t.Errorf("error does not explain the refused fallback to %s: %v", defaultServerURL, err)
			}
			if !strings.Contains(err.Error(), "http") {
				t.Errorf("error does not name the accepted URL shape: %v", err)
			}
		})
	}
}

// TestResolveServerURLAmbiguousServerOnlyEnvFailsClosed asserts every
// server-only variable (HTTP_ADDR, DB_*, CANOPY_DB_URL) alone is refused with
// an actionable message and no fallback target.
func TestResolveServerURLAmbiguousServerOnlyEnvFailsClosed(t *testing.T) {
	for _, name := range serverOnlyEnvVars {
		t.Run(name, func(t *testing.T) {
			clearCLITargetEnv(t)
			t.Setenv(name, "15440")

			got, err := resolveServerURL(os.Getenv)
			if err == nil {
				t.Fatalf("resolveServerURL() with only %s set: error = nil, want refusal", name)
			}
			if !errors.Is(err, ErrCLITargetUnresolved) {
				t.Errorf("error %v does not wrap ErrCLITargetUnresolved", err)
			}
			if got != "" {
				t.Errorf("resolveServerURL() = %q, want empty (no fallback)", got)
			}
			msg := err.Error()
			for _, want := range []string{name, "CANOPY_SERVER_URL", "HTTP client"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error does not mention %q: %v", want, err)
				}
			}
		})
	}

	t.Run("multiple names listed", func(t *testing.T) {
		clearCLITargetEnv(t)
		t.Setenv("DB_HOST", "127.0.0.1")
		t.Setenv("DB_PORT", "15440")
		got, err := resolveServerURL(os.Getenv)
		if err == nil {
			t.Fatal("resolveServerURL() error = nil, want refusal")
		}
		if got != "" {
			t.Errorf("resolveServerURL() = %q, want empty", got)
		}
		for _, want := range []string{"DB_HOST", "DB_PORT"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not list %s: %v", want, err)
			}
		}
	})
}

// TestResolveServerURLErrorsHideSecrets asserts neither a DB password nor URL
// credentials can leak into a refusal message.
func TestResolveServerURLErrorsHideSecrets(t *testing.T) {
	const dbSecret = "sup3r-s3cret-db-pass"

	t.Run("db password not echoed", func(t *testing.T) {
		clearCLITargetEnv(t)
		t.Setenv("DB_PASSWORD", dbSecret)

		_, err := resolveServerURL(os.Getenv)
		if err == nil {
			t.Fatal("resolveServerURL() error = nil, want refusal")
		}
		if strings.Contains(err.Error(), dbSecret) {
			t.Fatalf("error echoed DB_PASSWORD: %v", err)
		}
		if !strings.Contains(err.Error(), "DB_PASSWORD") {
			t.Errorf("error should name DB_PASSWORD as the offending setting: %v", err)
		}
	})

	t.Run("url credentials redacted", func(t *testing.T) {
		clearCLITargetEnv(t)
		t.Setenv("CANOPY_SERVER_URL", "http://user:"+dbSecret+"@localhost:notaport/")

		_, err := resolveServerURL(os.Getenv)
		if err == nil {
			t.Fatal("resolveServerURL() error = nil, want a refusal")
		}
		if strings.Contains(err.Error(), dbSecret) {
			t.Fatalf("error echoed URL credentials: %v", err)
		}
		if !strings.Contains(err.Error(), "***@") {
			t.Errorf("error did not mark the redacted userinfo: %v", err)
		}
	})
}

// TestResolveServerURLUnrelatedEnvKeepsDefault asserts unrelated environment
// (auth token, log level) does not make the target ambiguous.
func TestResolveServerURLUnrelatedEnvKeepsDefault(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("CANOPY_TOKEN", "some-jwt")
	t.Setenv("LOG_LEVEL", "debug")

	got, err := resolveServerURL(os.Getenv)
	if err != nil {
		t.Fatalf("resolveServerURL() error = %v, want nil", err)
	}
	if got != defaultServerURL {
		t.Errorf("resolveServerURL() = %q, want %q", got, defaultServerURL)
	}
}

// --- command-level tests ------------------------------------------------------

// TestCLIAmbiguousTargetMakesNoRequest is the DF-HERMES-CANOPY-6 regression: a
// server-only setting (DB_PORT / HTTP_ADDR) with no CANOPY_SERVER_URL must make
// the CLI fail before any HTTP request instead of silently targeting the
// default http://localhost:8091 and writing into whatever instance is live
// there.
func TestCLIAmbiguousTargetMakesNoRequest(t *testing.T) {
	for _, env := range []struct {
		name  string
		value string
	}{
		{"DB_PORT", "15440"},
		{"HTTP_ADDR", ":18092"},
	} {
		t.Run(env.name, func(t *testing.T) {
			clearCLITargetEnv(t)
			t.Setenv(env.name, env.value)
			rt := installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
				return jsonResponse(req, http.StatusOK, `{"id":"11111111-1111-4111-8111-111111111111","title":"x","root_node_id":"22222222-2222-4222-8222-222222222222"}`)
			})

			code, out := captureStderr(t, func() int {
				return runTreeCmdE([]string{"create", "scratch tree", "--content", "hello"})
			})

			if got := rt.recorded(); len(got) != 0 {
				t.Errorf("CLI issued HTTP requests with only %s set: %v", env.name, got)
			}
			if code == 0 {
				t.Errorf("runTreeCmdE(create) exit = 0 with only %s set, want nonzero", env.name)
			}
			if !strings.Contains(out, "CANOPY_SERVER_URL") {
				t.Errorf("error output does not mention CANOPY_SERVER_URL: %q", out)
			}
		})
	}
}

// TestCLIEveryHTTPCommandRefusesAmbiguousTarget asserts the refusal is a
// property of the shared client helper, not of the mutating command alone:
// list (read), delete (mutate), navigate (two requests) and the topic
// subcommands all fail before any request.
func TestCLIEveryHTTPCommandRefusesAmbiguousTarget(t *testing.T) {
	tests := []struct {
		name string
		run  func() int
	}{
		{"tree list", func() int { return runTreeCmdE([]string{"list"}) }},
		{"tree delete", func() int { return runTreeCmdE([]string{"delete", "11111111-1111-4111-8111-111111111111"}) }},
		{"tree navigate", func() int { return runTreeCmdE([]string{"navigate", "11111111-1111-4111-8111-111111111111"}) }},
		{"topic proposals", func() int { return topicProposalsE([]string{"--tree", "11111111-1111-4111-8111-111111111111"}) }},
		{"topic config get", func() int { return topicConfigE([]string{"--tree", "11111111-1111-4111-8111-111111111111"}) }},
		{"topic config put", func() int {
			return topicConfigE([]string{"--tree", "11111111-1111-4111-8111-111111111111", "--level", "full"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCLITargetEnv(t)
			t.Setenv("DB_PORT", "15440")
			rt := installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
				return jsonResponse(req, http.StatusOK, `{"trees":[],"pagination":{"hasMore":false,"total":0}}`)
			})

			code, out := captureStderr(t, tt.run)

			if got := rt.recorded(); len(got) != 0 {
				t.Errorf("HTTP request issued despite ambiguous target: %v", got)
			}
			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			if !strings.Contains(out, "CANOPY_SERVER_URL") {
				t.Errorf("stderr lacks actionable guidance: %q", out)
			}
		})
	}
}

// TestAPIRequestERefusesAmbiguousTargetInProcess asserts the shared client
// helper itself returns the refusal (no os.Exit inside it, no request), which
// is what makes every present and future HTTP subcommand fail closed.
func TestAPIRequestERefusesAmbiguousTargetInProcess(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("DB_PORT", "15440")
	rt := installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(req, http.StatusOK, `{}`)
	})

	_, _, err := apiRequestE(http.MethodGet, "/api/v1/trees", nil)
	if err == nil {
		t.Fatal("apiRequestE returned nil error for an ambiguous target")
	}
	if !errors.Is(err, ErrCLITargetUnresolved) {
		t.Errorf("error %v does not wrap ErrCLITargetUnresolved", err)
	}
	if !strings.Contains(err.Error(), "CANOPY_SERVER_URL") {
		t.Errorf("error lacks actionable guidance: %v", err)
	}
	if got := rt.recorded(); len(got) != 0 {
		t.Errorf("apiRequestE issued an HTTP request despite an ambiguous target: %v", got)
	}
}

// TestCLIInvalidExplicitTargetFailsBeforeNetwork asserts a malformed explicit
// target fails nonzero without any request (no fallback to the default).
func TestCLIInvalidExplicitTargetFailsBeforeNetwork(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("CANOPY_SERVER_URL", "localhost:8091")
	rt := installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(req, http.StatusOK, `{"trees":[],"pagination":{"hasMore":false,"total":0}}`)
	})

	code, out := captureStderr(t, func() int { return runTreeCmdE([]string{"list"}) })

	if got := rt.recorded(); len(got) != 0 {
		t.Errorf("HTTP request issued for an invalid target: %v", got)
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "CANOPY_SERVER_URL") || !strings.Contains(out, "localhost:8091") {
		t.Errorf("stderr does not quote the invalid target: %q", out)
	}
}

// TestCLIExplicitTargetHitsOnlyThatServer asserts an explicit CANOPY_SERVER_URL
// wins while DB_*/HTTP_ADDR are set: the create request reaches exactly that
// server, the default destination receives nothing, and the camelCase body the
// live API requires is unchanged.
func TestCLIExplicitTargetHitsOnlyThatServer(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("HTTP_ADDR", ":18092")
	t.Setenv("DB_HOST", "127.0.0.1")
	t.Setenv("DB_PORT", "15440")
	t.Setenv("DB_NAME", "canopy_scratch")

	var (
		mu       sync.Mutex
		method   string
		path     string
		body     map[string]any
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)

		mu.Lock()
		method, path, body, requests = r.Method, r.URL.Path, decoded, requests+1
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"33333333-3333-4333-8333-333333333333","title":"Scratch","root_node_id":"44444444-4444-4444-8444-444444444444"}`)
	}))
	defer srv.Close()
	t.Setenv("CANOPY_SERVER_URL", srv.URL)

	rt := installRecordingHTTPClient(t, nil) // real transport, recording URLs

	code, out := captureStderr(t, func() int {
		return runTreeCmdE([]string{"create", "Scratch", "--content", "hello scratch"})
	})

	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %q)", code, out)
	}
	mu.Lock()
	gotMethod, gotPath, gotBody, gotRequests := method, path, body, requests
	mu.Unlock()
	if gotRequests != 1 {
		t.Fatalf("test server received %d requests, want 1", gotRequests)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/trees" {
		t.Errorf("test server saw %s %s, want POST /api/v1/trees", gotMethod, gotPath)
	}
	if gotBody["title"] != "Scratch" {
		t.Errorf("body title = %v, want Scratch", gotBody["title"])
	}
	if _, ok := gotBody["rootMessage"]; !ok {
		t.Errorf("body is missing the camelCase rootMessage object: %v", gotBody)
	}

	recorded := rt.recorded()
	if len(recorded) != 1 {
		t.Fatalf("recorded %d request URL(s), want 1: %v", len(recorded), recorded)
	}
	if !strings.HasPrefix(recorded[0], srv.URL) {
		t.Errorf("request went to %s, want the explicit target %s", recorded[0], srv.URL)
	}
	for _, u := range recorded {
		if strings.Contains(u, "localhost:8091") {
			t.Errorf("request targeted the DEFAULT destination despite an explicit target: %s", u)
		}
	}
}

// TestCLIExplicitTargetOnlyTrimsTrailingSlash asserts the resolved URL keeps
// its path prefix and drops only the trailing slash.
func TestCLIExplicitTargetOnlyTrimsTrailingSlash(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("DB_PORT", "15440")
	t.Setenv("CANOPY_SERVER_URL", "http://127.0.0.1:1/")
	rt := installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(req, http.StatusOK, `{"trees":[],"pagination":{"hasMore":false,"total":0}}`)
	})

	if code := runTreeCmdE([]string{"list"}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	rec := rt.recorded()
	if len(rec) != 1 || rec[0] != "http://127.0.0.1:1/api/v1/trees" {
		t.Fatalf("recorded = %v, want [http://127.0.0.1:1/api/v1/trees]", rec)
	}
}

// TestCLIHelpIgnoresAmbiguousTarget asserts help stays available (exit 0,
// usage text, no error) even when the target configuration is ambiguous and
// without any HTTP request.
func TestCLIHelpIgnoresAmbiguousTarget(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantUse string
	}{
		{"tree", []string{"--help"}, "Usage: canopyd tree <create|list|delete|navigate>"},
		{"tree create", []string{"create", "--help"}, "Usage: canopyd tree create"},
		{"tree list", []string{"list", "-h"}, "Usage: canopyd tree list"},
		{"tree delete", []string{"delete", "--help"}, "Usage: canopyd tree delete"},
		{"tree navigate", []string{"navigate", "-h"}, "Usage: canopyd tree navigate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearCLITargetEnv(t)
			t.Setenv("DB_PORT", "15440")
			t.Setenv("HTTP_ADDR", ":18092")
			rt := installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
				return jsonResponse(req, http.StatusOK, `{}`)
			})

			code, out := captureStderr(t, func() int { return runTreeCmdE(tt.args) })

			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if !strings.Contains(out, tt.wantUse) {
				t.Errorf("stderr missing %q: %q", tt.wantUse, out)
			}
			if strings.Contains(out, "Error:") {
				t.Errorf("help printed a configuration error: %q", out)
			}
			if got := rt.recorded(); len(got) != 0 {
				t.Errorf("help issued HTTP requests: %v", got)
			}
		})
	}
}

// TestCLIAPIErrorShapeUnchanged asserts a 4xx payload is still rendered with
// its [CODE] message and a nonzero exit — the helper now returns the error
// instead of exiting, but the user-visible text must not change.
func TestCLIAPIErrorShapeUnchanged(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("CANOPY_SERVER_URL", "http://127.0.0.1:1")
	installRecordingHTTPClient(t, func(req *http.Request) *http.Response {
		return jsonResponse(req, http.StatusBadRequest, `{"error":{"code":"TITLE_REQUIRED","message":"title is required"}}`)
	})

	code, out := captureStderr(t, func() int { return runTreeCmdE([]string{"list"}) })

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "Error: [TITLE_REQUIRED] title is required") {
		t.Errorf("API error rendering changed: %q", out)
	}
}

// TestCLITransportFailureRendersTarget asserts an unreachable explicit target
// reports the target it tried, and still exits nonzero. The transport fails
// in-process so no socket is opened.
func TestCLITransportFailureRendersTarget(t *testing.T) {
	clearCLITargetEnv(t)
	t.Setenv("CANOPY_SERVER_URL", "http://127.0.0.1:1")
	old := httpClient
	httpClient = &http.Client{Transport: failingTransport{err: errors.New("dial tcp 127.0.0.1:1: connect: connection refused")}}
	t.Cleanup(func() { httpClient = old })

	code, out := captureStderr(t, func() int { return runTreeCmdE([]string{"list"}) })

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "failed to reach server at http://127.0.0.1:1") {
		t.Errorf("transport failure does not name the target: %q", out)
	}
}

// failingTransport fails every request in-process.
type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }
