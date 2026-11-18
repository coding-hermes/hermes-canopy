package telemetry

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"
)

// Upstream-gateway check error kinds — the CLOSED label set of
// canopy_upstream_gateway_check_errors_total{kind}. The observability spec
// (docs/observability-spec.md §4, P0 table) names the first three; "other" is
// the documented catch-all (DNS failure, TLS error, malformed URL) so no raw
// error string can ever become a label value.
const (
	GatewayCheckErrorTimeout     = "timeout"
	GatewayCheckErrorNon200      = "non_200"
	GatewayCheckErrorConnRefused = "conn_refused"
	GatewayCheckErrorOther       = "other"
)

// Default gateway-probe cadence. The OBS-1 acceptance criterion is "0 within
// 15s when the gateway is unreachable"; interval+timeout must therefore stay
// under 15s with room for the timer's own skew. 10s+3s leaves ~2s of margin,
// and a 10s poll is still 40x cheaper than the cost of a silent outage.
const (
	DefaultGatewayProbeInterval = 10 * time.Second
	DefaultGatewayProbeTimeout  = 3 * time.Second
)

// GatewayStatusCoder is implemented by an upstream error that carries an HTTP
// status code (gateway.HealthError). Declared as an interface so this package
// classifies non-200 answers without importing the gateway client.
type GatewayStatusCoder interface {
	HTTPStatusCode() int
}

// ClassifyGatewayCheckError maps a gateway health-check error onto one of the
// closed kind values above.
//
// Order is load-bearing: a timeout is also a net error and may wrap a
// connection error, so it is tested first; a refused connection is a syscall
// error below the transport; a non-200 answer is the gateway's own typed
// error. Everything unmatched is "other".
func ClassifyGatewayCheckError(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return GatewayCheckErrorTimeout
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return GatewayCheckErrorConnRefused
	}
	var coder GatewayStatusCoder
	if errors.As(err, &coder) && coder.HTTPStatusCode() != 0 {
		return GatewayCheckErrorNon200
	}
	return GatewayCheckErrorOther
}

// UpstreamGatewayProbe polls the upstream Hermes gateway and keeps
// canopy_upstream_gateway_up and canopy_upstream_gateway_check_errors_total
// current (docs/observability-spec.md §3.4 — canopy answers 2xx while every AI
// run fails, which is the one outage Prometheus could not see).
//
// The health function is the gateway client's (*gateway.Client).Health, passed
// in as a func so the probe is unit-testable against an httptest server and
// this package keeps no transport dependency.
type UpstreamGatewayProbe struct {
	metrics  *Metrics
	health   func(context.Context) error
	interval time.Duration
	timeout  time.Duration
}

// NewUpstreamGatewayProbe builds a probe with the documented defaults. A nil
// metrics sink or health func makes Check/Run no-ops, so a deployment with
// metrics disabled never runs a pointless network poll.
func NewUpstreamGatewayProbe(m *Metrics, health func(context.Context) error) *UpstreamGatewayProbe {
	return &UpstreamGatewayProbe{
		metrics:  m,
		health:   health,
		interval: DefaultGatewayProbeInterval,
		timeout:  DefaultGatewayProbeTimeout,
	}
}

// WithInterval overrides the poll cadence (must be > 0 to take effect).
func (p *UpstreamGatewayProbe) WithInterval(d time.Duration) *UpstreamGatewayProbe {
	if p != nil && d > 0 {
		p.interval = d
	}
	return p
}

// WithTimeout overrides the per-check budget (must be > 0 to take effect).
func (p *UpstreamGatewayProbe) WithTimeout(d time.Duration) *UpstreamGatewayProbe {
	if p != nil && d > 0 {
		p.timeout = d
	}
	return p
}

// Interval returns the effective poll cadence.
func (p *UpstreamGatewayProbe) Interval() time.Duration {
	if p == nil || p.interval <= 0 {
		return DefaultGatewayProbeInterval
	}
	return p.interval
}

// Timeout returns the effective per-check budget.
func (p *UpstreamGatewayProbe) Timeout() time.Duration {
	if p == nil || p.timeout <= 0 {
		return DefaultGatewayProbeTimeout
	}
	return p.timeout
}

// Check runs ONE health check and writes both series. It returns the
// reachability result (true = up).
//
// A cancelled PARENT context means the process is shutting down, not that the
// gateway failed: that branch returns without writing, so a graceful stop can
// never look like an outage on the dashboard.
func (p *UpstreamGatewayProbe) Check(ctx context.Context) bool {
	if p == nil || p.metrics == nil || p.health == nil {
		return false
	}
	parent := ctx
	checkCtx, cancel := context.WithTimeout(ctx, p.Timeout())
	defer cancel()

	err := p.health(checkCtx)
	if err != nil {
		if parent.Err() != nil {
			return false
		}
		p.metrics.IncUpstreamGatewayCheckError(ClassifyGatewayCheckError(err))
		p.metrics.SetUpstreamGatewayUp(false)
		return false
	}
	p.metrics.SetUpstreamGatewayUp(true)
	return true
}

// Run polls until ctx is cancelled, starting with an immediate check so
// canopy_upstream_gateway_up is written before the first scrape rather than
// one interval later.
func (p *UpstreamGatewayProbe) Run(ctx context.Context) {
	if p == nil || p.metrics == nil || p.health == nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	p.Check(ctx)
	ticker := time.NewTicker(p.Interval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Check(ctx)
		}
	}
}

// StartUpstreamGatewayProbe runs the probe on its own goroutine. Production
// calls this from the composition root (cmd/canopyd) when metrics are enabled;
// the goroutine exits with ctx.
func StartUpstreamGatewayProbe(ctx context.Context, p *UpstreamGatewayProbe) {
	if p == nil {
		return
	}
	go p.Run(ctx)
}
