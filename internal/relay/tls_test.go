package relay

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayTLSHandshake(t *testing.T) {
	ca, caKey, caFile := testCA(t)
	serverCert, serverKey := testCertificate(t, ca, caKey, "server", true)
	cfg := testTLSConfig(t, serverCert, serverKey, caFile)
	h, auth, addr := startTLSHub(t, cfg)
	clientCfg := cfg
	clientCfg.ConnectAddr, clientCfg.ListenAddr = "tls://"+addr, ""
	clientCfg.TLSCertFile, clientCfg.TLSKeyFile = &serverCert, &serverKey
	client := NewRelayClient(clientCfg)
	if err := client.Start(context.Background(), clientCfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(); _ = h.Close() })
	waitClientConnected(t, client)
	if auth == nil || h.ActiveSessions() != 1 {
		t.Fatalf("TLS HELLO did not establish: sessions=%d", h.ActiveSessions())
	}
}

func TestRelayMutualTLS(t *testing.T) {
	ca, caKey, caFile := testCA(t)
	serverCert, serverKey := testCertificate(t, ca, caKey, "server", true)
	clientCert, clientKey := testCertificate(t, ca, caKey, "client", false)
	cfg := testTLSConfig(t, serverCert, serverKey, caFile)
	cfg.TLSMutual = true
	h, _, addr := startTLSHub(t, cfg)
	t.Cleanup(func() { _ = h.Close() })

	good := cfg
	good.ListenAddr, good.ConnectAddr = "", "tls://"+addr
	good.TLSCertFile, good.TLSKeyFile = &clientCert, &clientKey
	client := NewRelayClient(good)
	if err := client.Start(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	waitClientConnected(t, client)

	bad := good
	bad.TLSMutual = false
	bad.TLSCertFile, bad.TLSKeyFile = &serverCert, &serverKey
	tlsCfg, err := relayClientTLSConfig(bad, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := (&tlsDialer{config: tlsCfg}).dial(addr)
	if err == nil {
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, err = conn.Write([]byte{0})
		if err == nil {
			// TLS 1.3: the server's client-cert rejection arrives as an alert
			// surfaced on the next read, not on Write (which buffers into the
			// TLS record layer). Read must fail with the handshake alert.
			_, err = conn.Read(make([]byte, 1))
		}
	}
	if err == nil {
		t.Fatal("mutual TLS accepted a client without a certificate")
	}
}

// tlsDialer keeps the negative assertion at the transport layer, before HELLO.
type tlsDialer struct{ config *tls.Config }

func (d *tlsDialer) dial(addr string) (net.Conn, error) { return tls.Dial("tcp", addr, d.config) }

func startTLSHub(t *testing.T, cfg DeploymentConfig) (*RelayHub, *FrameAuthenticator, string) {
	t.Helper()
	h := NewRelayHub(cfg)
	if err := h.Start(context.Background(), cfg); err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("loopback sockets unavailable: %v", err)
		}
		t.Fatal(err)
	}
	return h, NewFrameAuthenticator(uint16(cfg.HMACKeyID), cfg.HMACKey, 0, nil), h.listener.Addr().String()
}

func waitClientConnected(t *testing.T, client *RelayClient) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if state, _ := client.ClientHealth(); state == ClientConnected {
			return
		}
		time.Sleep(time.Millisecond)
	}
	state, errText := client.ClientHealth()
	t.Fatalf("client state=%s error=%s", state, errText)
}

func testTLSConfig(t *testing.T, cert, key, ca string) DeploymentConfig {
	cfg := DefaultConfig()
	cfg.Mode, cfg.Enabled, cfg.ListenAddr = ModeSelfHosted, true, "tcp://127.0.0.1:0"
	cfg.TLSEnabled, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile = true, &cert, &key, &ca
	cfg.HMACKeyID, cfg.HMACKey = 3, []byte("test-key")
	return cfg
}

func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	path := filepath.Join(t.TempDir(), "ca.pem")
	writePEM(t, path, "CERTIFICATE", der)
	return cert, key, path
}

func testCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, name string, server bool) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	usage := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	if server {
		usage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: usage, KeyUsage: x509.KeyUsageDigitalSignature}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+"-key.pem")
	writePEM(t, certPath, "CERTIFICATE", der)
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	writePEM(t, keyPath, "PRIVATE KEY", keyDER)
	return certPath, keyPath
}

func writePEM(t *testing.T, path, kind string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
}
