// Package broker implements the tunnel-broker: a small JSON API that creates
// and deletes short-lived Pomerium routes for ptun.
package broker

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// DefaultRoutePrefix is the default name prefix of every route and policy the
// broker owns (env ROUTE_PREFIX overrides it).
const DefaultRoutePrefix = "ephemeral:"

// RoutePrefix is the default prefix, used where no configured prefix is in scope.
const RoutePrefix = DefaultRoutePrefix

// ManagedMarker must appear in the description of every object the broker
// creates. Ownership requires BOTH the name prefix and this marker, so a
// hand-made "ephemeral: something" route is never adopted or deleted.
const ManagedMarker = "managed by tunnel-broker"

// ValidatePrefix checks a configured name prefix: non-empty, ends with ":",
// no whitespace.
func ValidatePrefix(p string) error {
	if p == "" || !strings.HasSuffix(p, ":") || strings.ContainsAny(p, " \t\r\n") {
		return fmt.Errorf("route prefix %q must be non-empty, end with \":\" and contain no whitespace", p)
	}
	return nil
}

// Owned reports whether an object with this name and description belongs to the
// broker: prefix AND marker.
func Owned(prefix, name, description string) bool {
	return strings.HasPrefix(name, prefix) && strings.Contains(description, ManagedMarker)
}

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// NewID returns a random 6 character lowercase alphanumeric id.
func NewID() (string, error) {
	b := make([]byte, 6)
	max := big.NewInt(int64(len(idAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("random id: %w", err)
		}
		b[i] = idAlphabet[n.Int64()]
	}
	return string(b), nil
}

// Hostname returns t-<id>.<baseDomain>.
func Hostname(id, baseDomain string) string { return "t-" + id + "." + baseDomain }

// RouteName returns the broker-owned route name for a tunnel.
func RouteName(id, email string) string { return RouteNameWith(RoutePrefix, id, email) }

// RouteNameWith is RouteName with an explicit prefix.
func RouteNameWith(prefix, id, email string) string { return prefix + id + " " + email }

// IDFromRouteName extracts the tunnel id from a route name, if it is a
// broker-owned name.
func IDFromRouteName(name string) (id, email string, ok bool) {
	return IDFromRouteNameWith(RoutePrefix, name)
}

// IDFromRouteNameWith is IDFromRouteName with an explicit prefix.
func IDFromRouteNameWith(prefix, name string) (id, email string, ok bool) {
	rest, found := strings.CutPrefix(name, prefix)
	if !found {
		return "", "", false
	}
	id, email, _ = strings.Cut(rest, " ")
	return id, email, id != ""
}

// EmailPolicy returns an inline PPL document allowing only the given email.
// The email is JSON-encoded so any quoting is safe (JSON is valid YAML).
func EmailPolicy(email string) ([]byte, error) {
	q, err := json.Marshal(email)
	if err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf("allow:\n  and:\n    - email:\n        is: %s\n", q)), nil
}

// PublicPolicy returns the visitor PPL for a public tunnel, the same shape as
// the homelab's Terraform `public` policy plus the creator: anyone may visit
// (accept), and CORS preflights are allowed. Emitted as JSON, which is valid
// YAML, so the email is safely quoted.
func PublicPolicy(email string) ([]byte, error) {
	type m = map[string]any
	doc := []m{
		{"allow": m{"or": []m{{"email": m{"is": email}}, {"accept": true}}}},
		{"allow": m{"and": []m{{"cors_preflight": true}}}},
	}
	return json.Marshal(doc)
}

// VisitorPolicy returns the PPL for who may open the tunnel's URL.
func VisitorPolicy(email string, public bool) ([]byte, error) {
	if public {
		return PublicPolicy(email)
	}
	return EmailPolicy(email)
}
