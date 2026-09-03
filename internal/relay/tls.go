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

func tlsCertificateHealth(cfg DeploymentConfig, now time.Time) (bool, string) {
	if !cfg.TLSEnabled || stringValue(cfg.TLSCertFile) == "" {
		return false, ""
	}
	pemBytes, err := os.ReadFile(stringValue(cfg.TLSCertFile))
	if err != nil {
		return true, "tls_cert_unreadable"
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return true, "tls_cert_invalid"
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
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
