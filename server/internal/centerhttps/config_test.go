package centerhttps

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func cleanEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"MULTICA_CENTER_HTTPS_ADDR", "MULTICA_CENTER_HTTPS_CERT_FILE", "MULTICA_CENTER_HTTPS_KEY_FILE", "MULTICA_CENTER_SYNC_ORIGIN"} {
		t.Setenv(key, "")
	}
}

func TestAdvertisedHTTPSOrigin(t *testing.T) {
	cleanEnvironment(t)
	cert, key, _ := certificateFiles(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	t.Setenv("MULTICA_CENTER_HTTPS_CERT_FILE", cert)
	t.Setenv("MULTICA_CENTER_HTTPS_KEY_FILE", key)
	for _, origin := range []string{"https://center.example.test:18082", "https://127.0.0.1:18082"} {
		t.Setenv("MULTICA_CENTER_SYNC_ORIGIN", origin)
		cfg, err := FromEnvironment()
		if err != nil {
			t.Fatal(err)
		}
		want := "Center HTTPS (Desktop/API and sync only)\n  Listening: :18082\n  Network:   " + origin + "\n"
		if cfg.StartupMessage() != want {
			t.Fatalf("unexpected banner: %s", cfg.StartupMessage())
		}
	}
	for _, origin := range []string{"http://127.0.0.1:18082", "https://127.0.0.1:18080", "https://127.0.0.1:18082/", "https://user@127.0.0.1:18082", "https://127.0.0.1:18082?", "https://127.0.0.1:18082#", "https://0.0.0.0:18082", "https://wrong.example:18082"} {
		t.Setenv("MULTICA_CENTER_SYNC_ORIGIN", origin)
		if _, err := FromEnvironment(); err == nil {
			t.Fatalf("invalid origin accepted: %s", origin)
		}
	}
}

func certificateFiles(t *testing.T, before, after time.Time) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test center only"},
		NotBefore: before, NotAfter: after,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		DNSNames: []string{"center.example.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("fixture certificate invalid")
	}
	return certFile, keyFile, roots
}

func TestDisabledUnlessConfigured(t *testing.T) {
	cleanEnvironment(t)
	if cfg, err := FromEnvironment(); cfg != nil || err != nil {
		t.Fatalf("disabled listener: %v, %v", cfg, err)
	}
	t.Setenv("MULTICA_CENTER_HTTPS_ADDR", ":18082")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("address-only TLS was accepted")
	}
}

func TestConfigDefaultsAndRejectsInvalidInputs(t *testing.T) {
	cleanEnvironment(t)
	cert, key, _ := certificateFiles(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	t.Setenv("MULTICA_CENTER_HTTPS_CERT_FILE", cert)
	t.Setenv("MULTICA_CENTER_HTTPS_KEY_FILE", key)
	cfg, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":18082" || cfg.TLS.MinVersion != tls.VersionTLS12 || len(cfg.TLS.Certificates) != 1 {
		t.Fatal("HTTPS defaults were not preserved")
	}
	for _, addr := range []string{":0", ":65536", "18082", "https://center:18082", ":https", "host.invalid:18082"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("MULTICA_CENTER_HTTPS_ADDR", addr)
			if _, err := FromEnvironment(); err == nil {
				t.Fatal("invalid bind address accepted")
			}
		})
	}
	for _, addr := range []string{":18082", "127.0.0.1:18082", "[::1]:18082"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("MULTICA_CENTER_HTTPS_ADDR", addr)
			if _, err := FromEnvironment(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, file := range []string{"", "relative.pem", filepath.Join(t.TempDir(), "missing.pem")} {
		t.Run("key="+file, func(t *testing.T) {
			t.Setenv("MULTICA_CENTER_HTTPS_KEY_FILE", file)
			if _, err := FromEnvironment(); err == nil {
				t.Fatal("missing/relative key accepted")
			}
		})
	}
	_, otherKey, _ := certificateFiles(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	t.Setenv("MULTICA_CENTER_HTTPS_KEY_FILE", otherKey)
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("mismatched key accepted")
	}
}

func TestRejectsExpiredAndFutureCertificates(t *testing.T) {
	cleanEnvironment(t)
	for _, offset := range []time.Duration{-2 * time.Hour, time.Hour} {
		cert, key, _ := certificateFiles(t, time.Now().Add(offset), time.Now().Add(offset+time.Hour))
		t.Setenv("MULTICA_CENTER_HTTPS_CERT_FILE", cert)
		t.Setenv("MULTICA_CENTER_HTTPS_KEY_FILE", key)
		if _, err := FromEnvironment(); err == nil {
			t.Fatal("certificate outside validity window accepted")
		}
	}
}

func TestVerifiedTLSAndAuthenticationBoundary(t *testing.T) {
	cleanEnvironment(t)
	cert, key, roots := certificateFiles(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	t.Setenv("MULTICA_CENTER_HTTPS_CERT_FILE", cert)
	t.Setenv("MULTICA_CENTER_HTTPS_KEY_FILE", key)
	cfg, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{
		TLSConfig: cfg.TLS, ReadHeaderTimeout: time.Second,
		ErrorLog: log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.TLS == nil {
				t.Error("router received plaintext")
			}
			if r.Header.Get("Authorization") != "Bearer fixture-session" {
				http.Error(w, "sign in required", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(); _ = listener.Close() })
	done := make(chan error, 1)
	go func() { done <- server.ServeTLS(listener, "", "") }()
	address := listener.Addr().String()
	for _, tc := range []struct {
		name, hostname string
		roots          *x509.CertPool
		trusted        bool
	}{
		{"trusted IP SAN", "", roots, true},
		{"trusted DNS SAN", "center.example.test", roots, true},
		{"unknown CA", "", x509.NewCertPool(), false},
		{"wrong hostname", "other.example.test", roots, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: tc.roots, ServerName: tc.hostname, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			response, err := client.Get("https://" + address + "/api/me")
			if !tc.trusted {
				if err == nil {
					response.Body.Close()
					t.Fatal("untrusted TLS accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusUnauthorized {
				t.Fatal("TLS bypassed authentication")
			}
			request, _ := http.NewRequest("POST", "https://"+address+"/api/center-sync/info", nil)
			request.Header.Set("Authorization", "Bearer fixture-session")
			response, err = client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				t.Fatal("authenticated fixture request failed")
			}
		})
	}
	// No plaintext fallback, even on the TLS port.
	plain := &http.Client{Timeout: 2 * time.Second}
	response, err := plain.Get("http://" + address + "/api/me")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || calls.Load() != 4 {
		t.Fatal("plaintext or untrusted request reached the router")
	}
	oldTLS, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", address,
		&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11})
	if err == nil {
		oldTLS.Close()
		t.Fatal("obsolete TLS version accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("shutdown: %v", err)
	}
}
