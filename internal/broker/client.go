package broker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/nickytonline/ptun/internal/derivecert"
	"github.com/pomerium/sdk-go"
)

func expected(aud string) *jwt.Expected { return &jwt.Expected{Audience: jwt.Audience{aud}} }

// ConfigTLS controls how the broker verifies the config API's certificate.
type ConfigTLS struct {
	// SkipVerify turns verification off (CONFIG_API_TLS_VERIFY=false).
	SkipVerify bool
	// CAFile is an extra PEM bundle to trust, besides the system roots.
	CAFile string
}

// NewConfigClients builds one Config API client per URL, authenticated with the
// base64 shared secret (sdk.NewClient with WithAPIToken).
func NewConfigClients(urls []string, sharedSecretB64 string, opts ConfigTLS) ([]routeClient, error) {
	tlsCfg, err := configTLSConfig(sharedSecretB64, opts)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	tr.TLSClientConfig = tlsCfg
	hc := &http.Client{Transport: tr}
	var out []routeClient
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		out = append(out, sdk.NewClient(sdk.WithAPIToken(sharedSecretB64), sdk.WithHTTPClient(hc), sdk.WithURL(u)))
	}
	return out, nil
}

// configTLSConfig accepts a config API certificate that either:
//   - chains to the CA Pomerium derives from the shared secret (tls_derive). The
//     host name isn't checked: the derived certificate names the tls_derive
//     domain, not necessarily the host in CONFIG_API_URLS, and only a holder of
//     the shared secret can issue one.
//   - or chains to the system roots or CAFile and matches the host name.
func configTLSConfig(sharedSecretB64 string, opts ConfigTLS) (*tls.Config, error) {
	if opts.SkipVerify {
		return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, nil //nolint:gosec // CONFIG_API_TLS_VERIFY=false
	}
	psk, err := base64.StdEncoding.DecodeString(sharedSecretB64)
	if err != nil {
		return nil, fmt.Errorf("SHARED_SECRET is not base64: %w", err)
	}
	ca, err := derivecert.NewCA(psk)
	if err != nil {
		return nil, fmt.Errorf("derive the config API CA from SHARED_SECRET: %w", err)
	}
	derived := x509.NewCertPool()
	derived.AddCert(ca.Certificate())

	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if opts.CAFile != "" {
		pemBytes, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("CONFIG_API_CA_FILE: %w", err)
		}
		if !roots.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("CONFIG_API_CA_FILE %s: no PEM certificates", opts.CAFile)
		}
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification happens in VerifyConnection, which needs to skip the host
		// name check for the derived CA only.
		InsecureSkipVerify: true, //nolint:gosec // verified in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("config API sent no certificate")
			}
			leaf := cs.PeerCertificates[0]
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				inter.AddCert(c)
			}
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: derived, Intermediates: inter}); err == nil {
				return nil
			}
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, DNSName: cs.ServerName}); err != nil {
				return fmt.Errorf("config API certificate isn't from the CA derived from SHARED_SECRET, and %w "+
					"(set CONFIG_API_CA_FILE for a private CA, or CONFIG_API_TLS_VERIFY=false to skip the check)", err)
			}
			return nil
		},
	}, nil
}
