package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

// gatherFamily returns the named metric family from a fresh registry, or nil.
func familyFromRegistry(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, fam := range families {
		if fam.GetName() == name {
			return fam
		}
	}
	return nil
}

// gaugeValue returns the single unlabelled gauge's value.
func gaugeReading(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	fam := familyFromRegistry(t, reg, name)
	if fam == nil {
		t.Fatalf("metric family %q is not served at all", name)
	}
	if n := len(fam.GetMetric()); n != 1 {
		t.Fatalf("%q has %d series, want 1", name, n)
	}
	return fam.GetMetric()[0].GetGauge().GetValue()
}

// counterByKind sums the counter family per kind label.
func counterByKind(t *testing.T, reg *prometheus.Registry, name string) map[string]float64 {
	t.Helper()
	fam := familyFromRegistry(t, reg, name)
	if fam == nil {
		return map[string]float64{}
	}
	out := map[string]float64{}
	for _, m := range fam.GetMetric() {
		kind := ""
		for _, l := range m.GetLabel() {
			if l.GetName() == "kind" {
				kind = l.GetValue()
			}
		}
		out[kind] = m.GetCounter().GetValue()
	}
	return out
}

// TestClassifyGatewayCheckError pins the CLOSED kind vocabulary and the
// classification order. The kind label must never be an error string: the spec
// names four values, and a fifth would be an unbounded label.
func TestClassifyGatewayCheckError(t *testing.T) {
	// A non-200 answer from the gateway client's typed error. Declared here
	// rather than imported so this package's test proves the INTERFACE
	// contract (HTTPStatusCode), not a concrete type.
	non200 := &fakeStatusError{code: http.StatusBadGateway}

	timeoutErr := fmt.Errorf("wrap: %w", context.DeadlineExceeded)
	netTimeout := &fakeTimeoutError{}

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is not a failure", nil, ""},
		{"deadline exceeded", timeoutErr, GatewayCheckErrorTimeout},
		{"net.Error timeout", netTimeout, GatewayCheckErrorTimeout},
		{"connection refused through a wrap", fmt.Errorf("gateway health: %w",
			&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}), GatewayCheckErrorConnRefused},
		{"non-200 typed error", non200, GatewayCheckErrorNon200},
		{"unknown error class", errors.New("dns: no such host"), GatewayCheckErrorOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyGatewayCheckError(tc.err); got != tc.want {
				t.Fatalf("ClassifyGatewayCheckError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}

	// The closed set itself: every classified value must be one of the four.
	for _, err := range []error{timeoutErr, non200, errors.New("x")} {
		switch kind := ClassifyGatewayCheckError(err); kind {
		case GatewayCheckErrorTimeout, GatewayCheckErrorNon200,
			GatewayCheckErrorConnRefused, GatewayCheckErrorOther:
		default:
			t.Fatalf("classifier produced an out-of-set kind %q", kind)
		}
	}
}

type fakeStatusError struct{ code int }

func (e *fakeStatusError) Error() string       { return fmt.Sprintf("HTTP %d", e.code) }
func (e *fakeStatusError) HTTPStatusCode() int { return e.code }

type fakeTimeoutError struct{}

func (e *fakeTimeoutError) Error() string   { return "i/o timeout" }
func (e *fakeTimeoutError) Timeout() bool   { return true }
func (e *fakeTimeoutError) Temporary() bool { return true }

// TestUpstreamGatewayProbeWritesBothSeries drives the probe against a real
// HTTP server: one healthy answer, then a 503. The gauge must track
// reachability and the counter must count exactly one non_200 failure.
func TestUpstreamGatewayProbeWritesBothSeries(t *testing.T) {
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	probe := NewUpstreamGatewayProbe(m, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/health", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return &fakeStatusError{code: resp.StatusCode}
		}
		return nil
	})

	if !probe.Check(context.Background()) {
		t.Fatal("Check against a healthy server returned false")
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 1 {
		t.Fatalf("%s = %v after a healthy check, want 1", SeriesUpstreamGatewayUp, got)
	}
	if got := counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal); len(got) != 0 {
		t.Fatalf("healthy check incremented the error counter: %v", got)
	}

	fail.Store(true)
	if probe.Check(context.Background()) {
		t.Fatal("Check against a 503 server returned true")
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 0 {
		t.Fatalf("%s = %v after a failed check, want 0", SeriesUpstreamGatewayUp, got)
	}
	if got := counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal)[GatewayCheckErrorNon200]; got != 1 {
		t.Fatalf("non_200 error count = %v, want 1 (all kinds: %v)", got,
			counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal))
	}
}

// TestUpstreamGatewayProbeCountsRefusedConnections is the AC2 "errors_total
// increases on failed checks" arm for the transport failure most likely in
// production: nothing listening on the gateway URL.
func TestUpstreamGatewayProbeCountsRefusedConnections(t *testing.T) {
	// Reserve a port, then close it so the connect is refused (not a hang).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	probe := NewUpstreamGatewayProbe(m, func(ctx context.Context) error {
		d := net.Dialer{}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("gateway health: %w", err)
		}
		return conn.Close()
	}).WithTimeout(2 * time.Second)

	if probe.Check(context.Background()) {
		t.Fatal("Check against a closed port returned true")
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 0 {
		t.Fatalf("%s = %v, want 0", SeriesUpstreamGatewayUp, got)
	}
	kinds := counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal)
	if kinds[GatewayCheckErrorConnRefused] != 1 {
		t.Fatalf("conn_refused count = %v, want 1 (all kinds: %v)", kinds[GatewayCheckErrorConnRefused], kinds)
	}
}

// TestUpstreamGatewayProbeTimeoutIsClassifiedAndBounded proves the per-check
// budget is enforced (the AC2 "0 within 15s" arm can only be met if a hung
// gateway cannot hang the poll) and that a timeout is labelled timeout, not
// other.
func TestUpstreamGatewayProbeTimeoutIsClassifiedAndBounded(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	probe := NewUpstreamGatewayProbe(m, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err() // deadline → classified timeout
		case <-release:
			return nil
		}
	}).WithTimeout(50 * time.Millisecond)

	start := time.Now()
	if probe.Check(context.Background()) {
		t.Fatal("Check against a hung server returned true")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Check took %v, want it bounded by the ~50ms timeout", elapsed)
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 0 {
		t.Fatalf("%s = %v, want 0", SeriesUpstreamGatewayUp, got)
	}
	if got := counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal)[GatewayCheckErrorTimeout]; got != 1 {
		t.Fatalf("timeout count = %v, want 1", got)
	}
}

// TestUpstreamGatewayProbeDefaultsMeetFifteenSecondContract pins the arithmetic
// behind AC2's "0 within 15s": the gauge can only be stale for
// interval+timeout, so the defaults must leave margin under 15s.
func TestUpstreamGatewayProbeDefaultsMeetFifteenSecondContract(t *testing.T) {
	// A registry of its own: NewMetrics() registers on the process-global
	// default registry, and a second call from another test in this package
	// panics with "duplicate metrics collector registration attempted".
	probe := NewUpstreamGatewayProbe(NewMetricsWithRegisterer(prometheus.NewRegistry()),
		func(context.Context) error { return nil })
	if probe.Interval() <= 0 || probe.Timeout() <= 0 {
		t.Fatalf("defaults are not positive: interval=%v timeout=%v", probe.Interval(), probe.Timeout())
	}
	if worst := probe.Interval() + probe.Timeout(); worst >= 15*time.Second {
		t.Fatalf("worst-case staleness interval+timeout = %v, must stay under the 15s acceptance bound", worst)
	}
	// A hung gateway must also not stall a poll for the whole interval.
	if probe.Timeout() >= probe.Interval() {
		t.Fatalf("timeout %v must be shorter than interval %v, else checks overlap",
			probe.Timeout(), probe.Interval())
	}
}

// TestUpstreamGatewayProbeRunFlipsWithinOneInterval proves the background
// poller actually moves the gauge while the process serves traffic, and exits
// when its context is cancelled.
func TestUpstreamGatewayProbeRunFlipsWithinOneInterval(t *testing.T) {
	var up atomic.Bool
	up.Store(true)

	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	probe := NewUpstreamGatewayProbe(m, func(context.Context) error {
		if up.Load() {
			return nil
		}
		return &fakeStatusError{code: http.StatusBadGateway}
	}).WithInterval(20 * time.Millisecond).WithTimeout(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		probe.Run(ctx)
	}()

	// The first check is immediate, so the gauge is up before any scrape.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gaugeReading(t, reg, SeriesUpstreamGatewayUp) != 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 1 {
		t.Fatalf("%s = %v while healthy, want 1", SeriesUpstreamGatewayUp, got)
	}

	up.Store(false)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && gaugeReading(t, reg, SeriesUpstreamGatewayUp) != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 0 {
		t.Fatalf("%s = %v after the gateway went down, want 0", SeriesUpstreamGatewayUp, got)
	}
	if counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal)[GatewayCheckErrorNon200] == 0 {
		t.Fatal("the poller never incremented the error counter")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestUpstreamGatewayProbeShutdownIsNotAnOutage: a cancelled PARENT context is
// a graceful stop, not a gateway failure — it must write nothing, so a
// redeploy cannot paint a false outage onto the dashboard.
func TestUpstreamGatewayProbeShutdownIsNotAnOutage(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	m.SetUpstreamGatewayUp(true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already shutting down

	probe := NewUpstreamGatewayProbe(m, func(context.Context) error {
		return context.Canceled
	})
	if probe.Check(ctx) {
		t.Fatal("Check reported up on a cancelled parent context")
	}
	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 1 {
		t.Fatalf("%s = %v after a shutdown-time check, want the last real value 1", SeriesUpstreamGatewayUp, got)
	}
	if got := counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal); len(got) != 0 {
		t.Fatalf("shutdown incremented the error counter: %v", got)
	}
}

// TestUpstreamGatewayProbeNilSafety: with metrics disabled the composition root
// still constructs a probe; it must be inert rather than panic.
func TestUpstreamGatewayProbeNilSafety(t *testing.T) {
	called := false
	p := NewUpstreamGatewayProbe(nil, func(context.Context) error {
		called = true
		return nil
	})
	if p.Check(context.Background()) {
		t.Fatal("a probe with no metrics sink reported up")
	}
	if called {
		t.Fatal("a probe with no metrics sink still hit the network")
	}
	StartUpstreamGatewayProbe(context.Background(), nil) // must not panic
	var nilProbe *UpstreamGatewayProbe
	nilProbe.Check(context.Background())
	nilProbe.Run(context.Background())
	if nilProbe.Interval() != DefaultGatewayProbeInterval || nilProbe.Timeout() != DefaultGatewayProbeTimeout {
		t.Fatal("nil probe must report the documented defaults")
	}
}

// TestIncUpstreamGatewayCheckErrorClosesTheLabelSet: a stray kind must fold
// into "other" instead of minting a new series.
func TestIncUpstreamGatewayCheckErrorClosesTheLabelSet(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)

	m.IncUpstreamGatewayCheckError("dns: no such host 10.0.0.7")
	m.IncUpstreamGatewayCheckError(GatewayCheckErrorTimeout)

	kinds := counterByKind(t, reg, SeriesUpstreamGatewayCheckErrorsTotal)
	if len(kinds) != 2 {
		t.Fatalf("counter has %d kinds (%v), want exactly 2", len(kinds), kinds)
	}
	if kinds[GatewayCheckErrorOther] != 1 || kinds[GatewayCheckErrorTimeout] != 1 {
		t.Fatalf("kinds = %v, want other=1 timeout=1", kinds)
	}
	// The raw string must not appear as a label value anywhere.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, fam := range families {
		for _, metric := range fam.GetMetric() {
			for _, label := range metric.GetLabel() {
				if strings.Contains(label.GetValue(), "no such host") {
					t.Fatalf("an error string leaked into label %q = %q", label.GetName(), label.GetValue())
				}
			}
		}
	}
}

// TestSetBuildInfoKeepOneIdentity: exactly one series, and a re-stamp replaces
// rather than accumulates.
func TestSetBuildInfoKeepsOneIdentity(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)

	m.SetBuildInfo("v1.2.3", "abc1234")
	m.SetBuildInfo("v1.3.0", "def5678")

	fam := familyFromRegistry(t, reg, SeriesBuildInfo)
	if fam == nil {
		t.Fatalf("%s is not served", SeriesBuildInfo)
	}
	metrics := fam.GetMetric()
	if len(metrics) != 1 {
		t.Fatalf("%s has %d series, want exactly 1 (a ghost identity would break PromQL joins)", SeriesBuildInfo, len(metrics))
	}
	labels := map[string]string{}
	for _, l := range metrics[0].GetLabel() {
		labels[l.GetName()] = l.GetValue()
	}
	if labels["version"] != "v1.3.0" || labels["commit"] != "def5678" {
		t.Fatalf("%s labels = %v, want the latest identity", SeriesBuildInfo, labels)
	}
	if got := metrics[0].GetGauge().GetValue(); got != 1 {
		t.Fatalf("%s value = %v, want 1", SeriesBuildInfo, got)
	}
}

// TestSetBuildInfoNeverServesEmptyLabels: an unstamped build must be labelled
// with the same placeholders /version uses, not with "".
func TestSetBuildInfoNeverServesEmptyLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	m.SetBuildInfo("", "")

	fam := familyFromRegistry(t, reg, SeriesBuildInfo)
	if fam == nil {
		t.Fatalf("%s is not served", SeriesBuildInfo)
	}
	for _, l := range fam.GetMetric()[0].GetLabel() {
		if l.GetValue() == "" {
			t.Fatalf("label %q is empty on an unstamped build", l.GetName())
		}
	}
}

// scrapeBody renders the registry through promhttp — the same handler /metrics
// mounts — and returns the exposition text.
func scrapeBody(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestMinimalViableSetServesFiveFamilies is AC1 at the unit level: a scrape
// carries at least five canopy_ product families, including the three new ones.
// The live curl arm stays owner-gated; this is its code-level equivalent.
func TestMinimalViableSetServesFiveFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)

	// Give every family at least one SAMPLE: the acceptance criterion greps
	// sample lines ('^canopy_'), and a family with no sample emits only
	// "# HELP/# TYPE" comments.
	m.SetBuildInfo("v0.0.0-test", "deadbee")
	m.SetUpstreamGatewayUp(false)
	m.IncUpstreamGatewayCheckError(GatewayCheckErrorTimeout)
	m.IncUnmatchedRequest(http.MethodGet)
	m.TreeCount.Set(3)
	m.NodeCount.Set(11)

	// Drive the request families through the real middleware.
	rec := httptest.NewRecorder()
	MetricsMiddleware(m)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/widgets/{widget_id}", nil))

	// Count the canopy_-prefixed sample lines exactly as the acceptance
	// criterion's `grep -c '^canopy_'` does.
	familiesSeen := map[string]bool{}
	for _, line := range strings.Split(scrapeBody(t, reg), "\n") {
		if !strings.HasPrefix(line, "canopy_") {
			continue
		}
		name := line
		if i := strings.IndexAny(name, "{ "); i >= 0 {
			name = name[:i]
		}
		familiesSeen[name] = true
	}

	for _, want := range []string{
		SeriesBuildInfo,
		SeriesUpstreamGatewayUp,
		SeriesUpstreamGatewayCheckErrorsTotal,
		SeriesHTTPUnmatchedRequestsTotal,
		SeriesTreeCount,
		SeriesNodeCount,
		SeriesHTTPRequestsTotal,
		SeriesActiveConnections,
	} {
		if !familiesSeen[want] {
			t.Errorf("scrape is missing the product family %q; saw %v", want, keysOf(familiesSeen))
		}
	}
	// Histograms expose per-suffix families (_bucket/_sum/_count), so the
	// duration family is matched by prefix rather than by its bare name.
	if !hasPrefix(familiesSeen, SeriesHTTPRequestDuration) {
		t.Errorf("scrape is missing the %q histogram; saw %v", SeriesHTTPRequestDuration, keysOf(familiesSeen))
	}
	if len(familiesSeen) < 5 {
		t.Fatalf("only %d canopy_ families on the wire (%v), want >= 5", len(familiesSeen), familiesSeen)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// hasPrefix reports whether any gathered family name starts with prefix (used
// for histogram families, which are exposed with _bucket/_sum/_count suffixes).
func hasPrefix(names map[string]bool, prefix string) bool {
	for name := range names {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// TestProbeRunIsConcurrencySafe exercises the poller under -race with a
// concurrent scraper, matching production (Run in a goroutine, promhttp reads
// the same series).
func TestProbeRunIsConcurrencySafe(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetricsWithRegisterer(reg)
	probe := NewUpstreamGatewayProbe(m, func(context.Context) error {
		return &fakeStatusError{code: http.StatusServiceUnavailable}
	}).WithInterval(5 * time.Millisecond).WithTimeout(5 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		probe.Run(ctx)
	}()

	for i := 0; i < 20; i++ {
		if _, err := reg.Gather(); err != nil {
			cancel()
			wg.Wait()
			t.Fatalf("gather during polling: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	wg.Wait()

	if got := gaugeReading(t, reg, SeriesUpstreamGatewayUp); got != 0 {
		t.Fatalf("%s = %v, want 0", SeriesUpstreamGatewayUp, got)
	}
}
