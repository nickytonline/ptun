package broker

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewIDAndHostname(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^[a-z0-9]{6}$`)
	seen := map[string]bool{}
	for range 50 {
		id, err := NewID()
		if err != nil || !re.MatchString(id) {
			t.Fatalf("bad id %q err %v", id, err)
		}
		seen[id] = true
	}
	if len(seen) < 40 {
		t.Fatalf("ids not random enough: %d unique", len(seen))
	}
	if got := Hostname("abc123", "maisonlab.dev"); got != "t-abc123.maisonlab.dev" {
		t.Fatalf("hostname %q", got)
	}
}

func TestEmailPolicy(t *testing.T) {
	t.Parallel()
	p, err := EmailPolicy("a@b.dev")
	if err != nil {
		t.Fatal(err)
	}
	want := "allow:\n  and:\n    - email:\n        is: \"a@b.dev\"\n"
	if string(p) != want {
		t.Fatalf("got %q", p)
	}
	p, _ = EmailPolicy("x\"\ny: z")
	if strings.Contains(string(p), "\ny: z") {
		t.Fatalf("injection: %q", p)
	}
}

func TestRouteNameRoundTrip(t *testing.T) {
	t.Parallel()
	id, email, ok := IDFromRouteName(RouteName("abc123", "a@b.dev"))
	if !ok || id != "abc123" || email != "a@b.dev" {
		t.Fatalf("%q %q %v", id, email, ok)
	}
	if _, _, ok := IDFromRouteName("grafana"); ok {
		t.Fatal("non-ptun name accepted")
	}
}
