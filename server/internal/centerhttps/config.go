// Package centerhttps configures the optional native HTTPS center listener.
// It reuses the application's router; TLS does not grant sync authorization.
package centerhttps

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const DefaultAddr = ":18082"

type Config struct {
	Addr          string
	TLS           *tls.Config
	NetworkOrigin string
}

// StartupMessage is emitted only after the listener is successfully bound.
// It is distinct from Next's HTTP-only Web banner, not a reachability claim.
func (c *Config) StartupMessage() string {
	message := fmt.Sprintf("Center HTTPS (Desktop/API and sync only)\n  Listening: %s\n", c.Addr)
	if c.NetworkOrigin != "" {
		message += fmt.Sprintf("  Network:   %s\n", c.NetworkOrigin)
	}
	return message
}

// FromEnvironment fails closed for partial or unusable TLS configuration.
// Certificates are operator-provided, loaded once, and renewed by restarting.
// No self-signed certificates are generated and no client trust is modified.
func FromEnvironment() (*Config, error) {
	addr := os.Getenv("MULTICA_CENTER_HTTPS_ADDR")
	certFile := os.Getenv("MULTICA_CENTER_HTTPS_CERT_FILE")
	keyFile := os.Getenv("MULTICA_CENTER_HTTPS_KEY_FILE")
	if addr == "" && certFile == "" && keyFile == "" {
		return nil, nil
	}
	if certFile == "" || keyFile == "" || !filepath.IsAbs(certFile) || !filepath.IsAbs(keyFile) {
		return nil, errors.New("HTTPS requires absolute MULTICA_CENTER_HTTPS_CERT_FILE and MULTICA_CENTER_HTTPS_KEY_FILE paths")
	}
	if addr == "" {
		addr = DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || (host != "" && host != "localhost" && net.ParseIP(host) == nil) {
		return nil, errors.New("MULTICA_CENTER_HTTPS_ADDR must be an IP:port listener address, for example :18082")
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("cannot load center HTTPS certificate/key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, errors.New("invalid center HTTPS leaf certificate")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("center HTTPS certificate is expired or not yet valid")
	}
	origin := os.Getenv("MULTICA_CENTER_SYNC_ORIGIN")
	if origin != "" {
		publicURL, err := url.Parse(origin)
		if err != nil || publicURL.Scheme != "https" || publicURL.Hostname() == "" || publicURL.User != nil ||
			publicURL.Path != "" || publicURL.RawQuery != "" || publicURL.ForceQuery || publicURL.Fragment != "" ||
			publicURL.Port() != port || origin != "https://"+publicURL.Host {
			return nil, errors.New("MULTICA_CENTER_SYNC_ORIGIN must be an HTTPS origin matching the listener port")
		}
		ip := net.ParseIP(publicURL.Hostname())
		if (ip != nil && ip.IsUnspecified()) || leaf.VerifyHostname(publicURL.Hostname()) != nil {
			return nil, errors.New("center HTTPS certificate SAN must cover the advertised sync hostname/IP")
		}
	}
	return &Config{Addr: addr, NetworkOrigin: origin, TLS: &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
	}}, nil
}
