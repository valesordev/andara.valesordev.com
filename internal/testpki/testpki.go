// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Valesor Development

// Package testpki issues a throwaway CA and server certificate for tests,
// so that nothing under test depends on .local/tls or on openssl. ECDSA
// P-256 keeps a thousand handshakes cheap.
package testpki

import (
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
	"testing"
	"time"
)

// PKI is one CA and one leaf for localhost / 127.0.0.1 / ::1.
type PKI struct {
	Dir      string
	CAFile   string
	CertFile string
	KeyFile  string
	Pool     *x509.CertPool
	// Cache is shared by every client config from ClientTLS, so handshakes
	// after the first resume rather than start over.
	Cache tls.ClientSessionCache
}

// New writes ca.pem, server.pem, and server-key.pem into a temp dir.
func New(t *testing.T) *PKI {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Andara's World"}, CommonName: "Andara Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{Organization: []string{"Andara's World"}, CommonName: "andara-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatal(err)
	}

	p := &PKI{
		Dir:      dir,
		CAFile:   filepath.Join(dir, "ca.pem"),
		CertFile: filepath.Join(dir, "server.pem"),
		KeyFile:  filepath.Join(dir, "server-key.pem"),
		Pool:     x509.NewCertPool(),
		Cache:    tls.NewLRUClientSessionCache(4),
	}
	p.Pool.AddCert(caCert)
	write := func(path string, b *pem.Block) {
		if err := os.WriteFile(path, pem.EncodeToMemory(b), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(p.CAFile, &pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	write(p.CertFile, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	write(p.KeyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER})
	return p
}

// ClientTLS trusts the CA and nothing else.
func (p *PKI) ClientTLS() *tls.Config {
	return &tls.Config{RootCAs: p.Pool, MinVersion: tls.VersionTLS12, ClientSessionCache: p.Cache}
}
