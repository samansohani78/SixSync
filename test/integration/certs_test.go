//go:build integration

package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// selfSignedPair generates a throwaway certificate and private key for testing
// how APISIX handles TLS material on read.
//
// Generated per run and never written to the repository: a committed private
// key is a liability even when it protects nothing.
func selfSignedPair(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "compat.example.test"},
		DNSNames:     []string{"compat.example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}

	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	// PKCS#1 bytes must carry the "RSA PRIVATE KEY" label. Labelling them
	// "PRIVATE KEY" claims PKCS#8 and OpenSSL refuses to parse them, which
	// APISIX reports as `failed to parse key: PEM_read_bio_PrivateKey()`.
	keyPEM = string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	return certPEM, keyPEM
}
