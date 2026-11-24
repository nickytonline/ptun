package broker

import (
	"crypto/tls"
	"net/http"
	"strings"

	"github.com/go-jose/go-jose/v3/jwt"
	"github.com/pomerium/sdk-go"
)

func expected(aud string) *jwt.Expected { return &jwt.Expected{Audience: jwt.Audience{aud}} }

// NewConfigClients builds one Config API client per URL, authenticated with the
// base64 shared secret exactly like the Terraform provider (sdk.NewClient with
// WithAPIToken). insecureSkipVerify mirrors tls_insecure_skip_verify in
// terraform/pomerium-core/providers.tf.
//
// TODO: verify the config API certificate (derived from the shared secret via
// tls_derive "*.pomerium-headless") instead of skipping verification.
func NewConfigClients(urls []string, sharedSecretB64 string, insecureSkipVerify bool) []routeClient {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecureSkipVerify} //nolint:gosec // see TODO
	hc := &http.Client{Transport: tr}
	var out []routeClient
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		out = append(out, sdk.NewClient(sdk.WithAPIToken(sharedSecretB64), sdk.WithHTTPClient(hc), sdk.WithURL(u)))
	}
	return out
}
