package broker

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickytonline/ptun/internal/derivecert"
)

// derivedServer serves the certificate Pomerium derives for tls_derive
// "*.pomerium-headless", which never matches the test server's 127.0.0.1.
func derivedServer(t *testing.T, psk []byte) *httptest.Server {
	t.Helper()
	ca, err := derivecert.NewCA(psk)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.NewServerCert("*.pomerium-headless")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url, secret string, opts ConfigTLS) error {
	t.Helper()
	cfg, err := configTLSConfig(secret, opts)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}).Get(url)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func TestConfigTLS(t *testing.T) {
	t.Parallel()
	psk := []byte("ptun-derivecert-test-shared-key!")
	secret := base64.StdEncoding.EncodeToString(psk)
	otherSecret := base64.StdEncoding.EncodeToString([]byte("a-different-pomerium-shared-key!"))
	derived := derivedServer(t, psk)

	if err := get(t, derived.URL, secret, ConfigTLS{}); err != nil {
		t.Errorf("derived cert, same secret: %v", err)
	}
	if err := get(t, derived.URL, otherSecret, ConfigTLS{}); err == nil || !strings.Contains(err.Error(), "SHARED_SECRET") {
		t.Errorf("derived cert, other secret: got %v, want a verification error", err)
	}
	if err := get(t, derived.URL, otherSecret, ConfigTLS{SkipVerify: true}); err != nil {
		t.Errorf("SkipVerify: %v", err)
	}

	// A server with its own CA: rejected until CONFIG_API_CA_FILE trusts it.
	own := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(own.Close)
	if err := get(t, own.URL, secret, ConfigTLS{}); err == nil {
		t.Error("private CA without CONFIG_API_CA_FILE: want an error")
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: own.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := get(t, own.URL, secret, ConfigTLS{CAFile: caFile}); err != nil {
		t.Errorf("private CA with CONFIG_API_CA_FILE: %v", err)
	}
}

func TestConfigTLSBadSecret(t *testing.T) {
	t.Parallel()
	if _, err := configTLSConfig("not base64!", ConfigTLS{}); err == nil {
		t.Error("want an error for a non-base64 secret")
	}
}
