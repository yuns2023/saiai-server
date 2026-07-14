package extracerts

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAddsCertificatesFromDirectoryAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	certPEM := testCertificatePEM(t)
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "duplicate.crt"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := "-----BEGIN " + "PRIVATE KEY-----\nabc\n-----END " + "PRIVATE KEY-----\n"
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), []byte(privateKeyPEM), 0o644); err != nil {
		t.Fatal(err)
	}

	result := Load(Options{
		BasePool: x509.NewCertPool(),
		Dirs:     []string{dir},
	})

	if result.CertCount != 1 {
		t.Fatalf("expected one unique extra cert, got %d, warnings=%v", result.CertCount, result.Warnings)
	}
	if result.Pool == nil {
		t.Fatal("expected certificate pool")
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", result.Warnings)
	}
}

func TestLoadReportsInvalidCertificateBlocks(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.pem")
	if err := os.WriteFile(file, []byte("-----BEGIN CERTIFICATE-----\naW52YWxpZA==\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := Load(Options{
		BasePool: x509.NewCertPool(),
		Files:    []string{file},
	})

	if result.CertCount != 0 {
		t.Fatalf("expected zero certs, got %d", result.CertCount)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected warning for invalid cert")
	}
}

func testCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "SAIAI Test Root"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
