package derivecert

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"testing"
)

// Golden values from github.com/pomerium/pomerium/pkg/derivecert v0.34.1 for
// the same key. Only the public keys and serials are pinned: ECDSA signatures
// (and so the DER bytes) may differ between Go versions.
func TestCAMatchesPomerium(t *testing.T) {
	ca, err := NewCA([]byte("ptun-derivecert-test-shared-key!"))
	if err != nil {
		t.Fatal(err)
	}
	check(t, "CA", ca.Certificate(),
		"56e07f4e2ecc8adb1a6956cfeebf3f72c10fed12c31eea19aa47e9b404196d59", "161160387973574547090684874007779763262")

	srv, err := ca.NewServerCert("*.pomerium-headless")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(srv.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	check(t, "server", leaf,
		"6ccecbbd09cfb632f0a662a687ea8fcdca7cb6becb6210f65f6d77c99166044d", "133645673171298287950406861162617543505")

	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "pomerium-0.pomerium-headless"}); err != nil {
		t.Fatalf("server cert doesn't verify against the CA: %v", err)
	}
}

func check(t *testing.T, name string, c *x509.Certificate, spki, serial string) {
	t.Helper()
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	if got := hex.EncodeToString(sum[:]); got != spki {
		t.Errorf("%s public key sha256 = %s, want %s", name, got, spki)
	}
	if got := c.SerialNumber.String(); got != serial {
		t.Errorf("%s serial = %s, want %s", name, got, serial)
	}
}
