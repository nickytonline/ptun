// Package broker implements the tunnel-broker: a small JSON API that creates
// and deletes short-lived Pomerium routes for ptun.
package broker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
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

// PublicPolicy returns the visitor PPL for a public tunnel: anyone may visit
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

// StableIDLen is the length of the per-user stable id (e.g. "k3x9m2pq").
const StableIDLen = 8

// StableID returns the caller's stable id: StableIDLen base36 characters of
// sha256(baseDomain + "|" + sub), where sub is the IdP subject from Pomerium's
// JWT. It is deterministic (no storage), one-way (the URL reveals neither the sub
// nor the email), and changes only if the IdP subject changes.
func StableID(subject, baseDomain string) string {
	sum := sha256.Sum256([]byte(baseDomain + "|" + subject))
	space := uint64(1)
	for range StableIDLen {
		space *= 36
	}
	s := strconv.FormatUint(binary.BigEndian.Uint64(sum[:8])%space, 36)
	return strings.Repeat("0", StableIDLen-len(s)) + s
}

// MaxNameLen bounds --name so <name>-<stable id> stays well inside a DNS label (63).
const MaxNameLen = 32

// NormalizeName lowercases and validates a tunnel name: a-z, 0-9 and single
// hyphens, starting and ending with a letter or digit.
func NormalizeName(name string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	bad := n == "" || len(n) > MaxNameLen || n[0] == '-' || n[len(n)-1] == '-' || strings.Contains(n, "--")
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			bad = true
		}
	}
	if bad {
		return "", fmt.Errorf("%w: name %q must be 1-%d letters, digits or single hyphens, starting and ending with a letter or digit", ErrInvalidInput, name, MaxNameLen)
	}
	return n, nil
}

// StableHostname returns <stable id>.<baseDomain>, or <name>-<stable id>.<baseDomain>
// when name is set. name must already be normalized.
func StableHostname(name, subject, baseDomain string) string {
	label := StableID(subject, baseDomain)
	if name != "" {
		label = name + "-" + label
	}
	return label + "." + baseDomain
}
