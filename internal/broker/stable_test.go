package broker

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestStableID(t *testing.T) {
	t.Parallel()
	re := regexp.MustCompile(`^[a-z0-9]{4}$`)
	a := StableID("auth0|abc", "example.com")
	if !re.MatchString(a) {
		t.Fatalf("bad stable id %q", a)
	}
	if StableID("auth0|abc", "example.com") != a {
		t.Fatal("not deterministic")
	}
	if StableID("auth0|abd", "example.com") == a && StableID("auth0|abe", "example.com") == a {
		t.Fatal("different subjects give the same id")
	}
	seen := map[string]bool{}
	for i := range 200 {
		seen[StableID(string(rune('a'+i%26))+string(rune(i)), "example.com")] = true
	}
	if len(seen) < 190 {
		t.Fatalf("ids collide too often: %d unique of 200", len(seen))
	}
	if got := StableHostname("", "auth0|abc", "example.com"); got != a+".example.com" {
		t.Fatalf("bare %q", got)
	}
	if got := StableHostname("demo", "auth0|abc", "example.com"); got != "demo-"+a+".example.com" {
		t.Fatalf("named %q", got)
	}
}

func TestNormalizeName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"demo": "demo", "My-App2": "my-app2", " x ": "x"} {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Fatalf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "-a", "a-", "a--b", "a.b", "a_b", "ünï", "a b", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"} {
		if _, err := NormalizeName(in); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%q accepted", in)
		}
	}
}

func TestStableTunnel(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, now := newMgr(f)
	ctx := context.Background()
	host := StableHostname("demo", "sub-a", "example.com")

	// --name implies stable; no subject is refused.
	if _, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Name: "demo"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("no subject: %v", err)
	}
	if _, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Name: "bad name", Subject: "sub-a"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad name: %v", err)
	}
	first, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Name: "Demo", Subject: "sub-a"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Hostname != host || first.URL != "https://"+host || !first.Stable || first.HeartbeatIntervalSeconds != 30 ||
		!first.ExpiresAt.Equal(now.Add(StableTTL)) {
		t.Fatalf("%+v", first)
	}
	// Same link again while it is live: refused, nothing touched.
	if _, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:2", Name: "demo", Subject: "sub-a"}); !errors.Is(err, ErrConflict) ||
		!strings.Contains(err.Error(), "another ptun of yours") {
		t.Fatalf("second run: %v", err)
	}
	// Another user landing on the same hostname (same subject here, e.g. an email change): conflict.
	if _, err := m.CreateWith(ctx, "b@x.dev", CreateRequest{Target: "localhost:3", Name: "demo", Subject: "sub-a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("other user: %v", err)
	}
	// A different name, and bare --stable, run side by side.
	other, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:2", Name: "other", Subject: "sub-a"})
	if err != nil || other.Hostname != StableHostname("other", "sub-a", "example.com") {
		t.Fatalf("different name: %+v %v", other, err)
	}
	if err := m.Delete(ctx, other.ID, "a@x.dev"); err != nil {
		t.Fatal(err)
	}
	bare, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:4", Stable: true, Subject: "sub-a"})
	if err != nil || bare.Hostname != StableID("sub-a", "example.com")+".example.com" {
		t.Fatalf("bare: %+v %v", bare, err)
	}
	if err := m.Delete(ctx, bare.ID, "a@x.dev"); err != nil {
		t.Fatal(err)
	}
	// A crashed ptun stops heartbeating: after the short stable lease the link is free again,
	// even before the sweep has removed the old route.
	*now = now.Add(StableTTL)
	second, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:2", Name: "demo", Subject: "sub-a"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Hostname != host || len(f.routes) != 1 || len(f.pols) != 1 {
		t.Fatalf("after expiry: %+v routes=%d pols=%d", second, len(f.routes), len(f.pols))
	}
	// After Ctrl-C (Delete) the link is free immediately.
	if err := m.Delete(ctx, second.ID, "a@x.dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:2", Name: "demo", Subject: "sub-a"}); err != nil {
		t.Fatal(err)
	}
	m.Sweep(ctx) // drops the expired first lease (its route is already gone)
	if _, ok := m.leases[first.ID]; ok {
		t.Fatal("expired lease not swept")
	}
	// Random tunnels keep the normal lease.
	r, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:5"})
	if err != nil || r.Stable || r.HeartbeatIntervalSeconds != 120 {
		t.Fatalf("random: %+v %v", r, err)
	}
}

func TestStableHostnameNeverShadowsOtherRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	host := StableHostname("", "sub-a", "example.com")
	from := "https://" + host

	// A hand-made (or other) route already on the hostname: conflict, nothing touched.
	f := newFake()
	f.seed("g1", "grafana")
	f.routes["g1"].From = from
	m, _ := newMgr(f)
	if _, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Stable: true, Subject: "sub-a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign route: %v", err)
	}
	if len(f.routes) != 1 || len(m.leases) != 0 || len(f.pols) != 0 {
		t.Fatalf("side effects: routes=%d leases=%d pols=%d", len(f.routes), len(m.leases), len(f.pols))
	}

	// Another user's broker-made route on it: conflict too.
	f = newFake()
	f.seed("o1", RouteName("zzzzzz", "b@x.dev"))
	f.routes["o1"].From = from
	m, _ = newMgr(f)
	if _, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Stable: true, Subject: "sub-a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("other user's route: %v", err)
	}

	// The caller's own route with no live lease here (expired, not yet swept): replaced.
	f = newFake()
	f.seed("s1", RouteName("yyyyyy", "a@x.dev"))
	f.routes["s1"].From = from
	f.seedPolicy("stalepol", RouteName("yyyyyy", "a@x.dev"))
	f.routes["s1"].PolicyIds = []string{"stalepol"}
	m, _ = newMgr(f)
	tn, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Stable: true, Subject: "sub-a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.routes["s1"]; ok {
		t.Fatal("stale route kept")
	}
	if _, ok := f.pols["stalepol"]; ok {
		t.Fatal("stale policy kept")
	}
	if len(f.routes) != 1 || tn.Hostname != host {
		t.Fatalf("routes=%d %+v", len(f.routes), tn)
	}
}

func TestAdoptKeepsStableHostname(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	ctx := context.Background()
	tn, err := m.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:1", Name: "demo", Subject: "sub-a"})
	if err != nil {
		t.Fatal(err)
	}
	m2, now := newMgr(f)
	if err := m2.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := m2.Heartbeat(tn.ID, "a@x.dev")
	if err != nil || got.Hostname != tn.Hostname || !got.Stable || !got.ExpiresAt.Equal(now.Add(StableTTL)) {
		t.Fatalf("%+v %v", got, err)
	}
	// The adopted tunnel still holds the link.
	if _, err := m2.CreateWith(ctx, "a@x.dev", CreateRequest{Target: "localhost:2", Name: "demo", Subject: "sub-a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("adopted link: %v", err)
	}
}

func TestStableConcurrentStartIsConflict(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, now := newMgr(f)
	host := StableHostname("demo", "sub-a", "example.com")
	// Another create for the same link has reserved it but not yet created its route (routeID "").
	m.leases["inflight"] = &lease{email: "a@x.dev", hostname: host, stable: true, expires: now.Add(m.cfg.TTL)}
	if _, err := m.CreateWith(context.Background(), "a@x.dev", CreateRequest{Target: "localhost:1", Name: "demo", Subject: "sub-a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("concurrent start: %v", err)
	}
	if len(m.leases) != 1 || len(f.routes) != 0 || len(f.pols) != 0 {
		t.Fatalf("side effects: leases=%d routes=%d pols=%d", len(m.leases), len(f.routes), len(f.pols))
	}
}
