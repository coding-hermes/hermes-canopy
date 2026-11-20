package relay

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrNoRelayCertificate means the relay has no TLS certificate to report: TLS
// is disabled, or no certificate path is configured. Callers must treat it as
// "nothing to publish" rather than publishing a zero expiry (OBS-5).
var ErrNoRelayCertificate = errors.New("relay: no relay TLS certificate configured")

// errTLSCertUnreadable and errTLSCertInvalid split the two failure classes
// tlsCertificateHealth reports (tls_cert_unreadable / tls_cert_invalid) so the
// /health reason and the expiry gauge can share one parser without either of
// them re-deriving "which kind of broken is this".
var (
	errTLSCertUnreadable = errors.New("relay: TLS certificate unreadable")
	errTLSCertInvalid    = errors.New("relay: TLS certificate invalid")
)

// parseTLSCertificate decodes the first PEM block of path and parses it as an
// X.509 certificate. It is the ONE place the relay turns a certificate file
// into a certificate: /health's TLS degradation check and the expiry gauge
// therefore always read the same bytes and cannot disagree about the leaf.
func parseTLSCertificate(path string) (*x509.Certificate, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errTLSCertUnreadable, path, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("%w: %s contains no PEM block", errTLSCertInvalid, path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errTLSCertInvalid, path, err)
	}
	return cert, nil
}

// CertificateExpiry reports the relay TLS leaf certificate's notAfter from the
// deployment config's TLSCertFile.
//
// It returns ErrNoRelayCertificate when the relay presents no certificate (TLS
// disabled or no cert path) — the same predicate tlsCertificateHealth uses, so
// "degraded: no cert" and "no expiry gauge" can never contradict each other.
// Any other error means the configured certificate could not be read or parsed.
func CertificateExpiry(cfg DeploymentConfig) (time.Time, error) {
	path := stringValue(cfg.TLSCertFile)
	if !cfg.TLSEnabled || path == "" {
		return time.Time{}, ErrNoRelayCertificate
	}
	cert, err := parseTLSCertificate(path)
	if err != nil {
		return time.Time{}, err
	}
	return cert.NotAfter, nil
}

func tlsCertificateHealth(cfg DeploymentConfig, now time.Time) (bool, string) {
	if !cfg.TLSEnabled || stringValue(cfg.TLSCertFile) == "" {
		return false, ""
	}
	cert, err := parseTLSCertificate(stringValue(cfg.TLSCertFile))
	if err != nil {
		if errors.Is(err, errTLSCertUnreadable) {
			return true, "tls_cert_unreadable"
		}
		return true, "tls_cert_invalid"
	}
	if !now.Before(cert.NotAfter) {
		return true, "tls_cert_expired"
	}
	if cert.NotAfter.Sub(now) <= 7*24*time.Hour {
		return true, "tls_cert_expiring_soon"
	}
	return false, ""
}

func relayServerTLSConfig(cfg DeploymentConfig) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(stringValue(cfg.TLSCertFile), stringValue(cfg.TLSKeyFile))
	if err != nil {
		return nil, fmt.Errorf("relay: load TLS certificate/key: %w", err)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	if cfg.TLSMutual {
		pool, err := loadCertPool(stringValue(cfg.TLSCAFile))
		if err != nil {
			return nil, err
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsCfg, nil
}

func relayClientTLSConfig(cfg DeploymentConfig, serverName string) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
	if stringValue(cfg.TLSCAFile) != "" {
		pool, err := loadCertPool(stringValue(cfg.TLSCAFile))
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	}
	if cfg.TLSMutual {
		cert, err := tls.LoadX509KeyPair(stringValue(cfg.TLSCertFile), stringValue(cfg.TLSKeyFile))
		if err != nil {
			return nil, fmt.Errorf("relay: load TLS client certificate/key: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("relay: read TLS CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("relay: TLS CA file contains no certificates")
	}
	return pool, nil
}
