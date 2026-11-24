package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	configpb "github.com/pomerium/pomerium/pkg/grpc/config"
)

// fakeAPI is an in-memory config API endpoint.
type fakeAPI struct {
	mu        sync.Mutex
	fail      error // returned by every call when set (a follower)
	routes    map[string]*configpb.Route
	pols      map[string]*configpb.Policy
	failRoute error // CreateRoute only
	next      int
	calls     int
}

func newFake() *fakeAPI {
	return &fakeAPI{routes: map[string]*configpb.Route{}, pols: map[string]*configpb.Policy{}}
}

func (f *fakeAPI) CreateRoute(_ context.Context, r *connect.Request[configpb.CreateRouteRequest]) (*connect.Response[configpb.CreateRouteResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	if f.failRoute != nil {
		return nil, f.failRoute
	}
	f.next++
	id := "r" + string(rune('0'+f.next))
	rt := r.Msg.GetRoute()
	rt.Id = &id
	f.routes[id] = rt
	return connect.NewResponse(&configpb.CreateRouteResponse{Route: rt}), nil
}

func (f *fakeAPI) DeleteRoute(_ context.Context, r *connect.Request[configpb.DeleteRouteRequest]) (*connect.Response[configpb.DeleteRouteResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	delete(f.routes, r.Msg.GetId())
	return connect.NewResponse(&configpb.DeleteRouteResponse{}), nil
}

func (f *fakeAPI) GetRoute(_ context.Context, r *connect.Request[configpb.GetRouteRequest]) (*connect.Response[configpb.GetRouteResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	rt, ok := f.routes[r.Msg.GetId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no route"))
	}
	return connect.NewResponse(&configpb.GetRouteResponse{Route: rt}), nil
}

func (f *fakeAPI) ListRoutes(_ context.Context, _ *connect.Request[configpb.ListRoutesRequest]) (*connect.Response[configpb.ListRoutesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	var out []*configpb.Route
	for _, r := range f.routes {
		out = append(out, r)
	}
	return connect.NewResponse(&configpb.ListRoutesResponse{Routes: out, TotalCount: uint64(len(out))}), nil
}

// seed adds a route the way the broker would have made it (marker in the description).
func (f *fakeAPI) seed(id, name string) {
	desc := "Ephemeral ptun tunnel, " + ManagedMarker + ". Do not edit."
	f.routes[id] = &configpb.Route{Id: &id, Name: &name, Description: &desc}
}

// seedHand adds a hand-made route and policy: right prefix, but no marker.
func (f *fakeAPI) seedHand(id, name string) {
	f.routes[id] = &configpb.Route{Id: &id, Name: &name}
	f.pols[id] = &configpb.Policy{Id: &id, Name: &name}
}

var testCfg = Config{AllowMCP: true, BaseDomain: "maisonlab.dev", SSHHost: "ssh.maisonlab.dev", SSHPort: 2200, TTL: 10 * time.Minute, HeartbeatInterval: 2 * time.Minute, MaxPerUser: 2}

func newMgr(f ...*fakeAPI) (*Manager, *time.Time) {
	var clients []routeClient
	for _, x := range f {
		clients = append(clients, x)
	}
	m := NewManager(testCfg, NewRoutes(clients), slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1_000_000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

func TestGuardNeverTouchesForeignRoutes(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seed("g1", "grafana")
	r := NewRoutes([]routeClient{f})
	if err := r.Delete(context.Background(), "g1", ""); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("want ErrNotManaged, got %v", err)
	}
	if _, ok := f.routes["g1"]; !ok {
		t.Fatal("foreign route was deleted")
	}
	name := "grafana"
	if _, _, err := r.Create(context.Background(), &configpb.Route{Name: &name}, nil); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("create: %v", err)
	}
	if err := r.Delete(context.Background(), "missing", ""); err != nil {
		t.Fatalf("missing route should be a no-op: %v", err)
	}
	managed, _ := r.ListManaged(context.Background())
	if len(managed) != 0 {
		t.Fatalf("listed foreign routes: %v", managed)
	}
}

func TestCreateBuildsRoute(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	tn, err := m.Create(context.Background(), "me@x.dev", "localhost:8081", false)
	if err != nil {
		t.Fatal(err)
	}
	if tn.SSHPort != 2200 || tn.HeartbeatIntervalSeconds != 120 || tn.URL != "https://"+tn.Hostname {
		t.Fatalf("%+v", tn)
	}
	if len(f.pols) != 1 || len(f.routes) != 1 {
		t.Fatalf("want 1 policy and 1 route, got %d/%d", len(f.pols), len(f.routes))
	}
	var rt *configpb.Route
	for _, x := range f.routes {
		rt = x
	}
	var pol *configpb.Policy
	for _, x := range f.pols {
		pol = x
	}
	if pol.GetName() != rt.GetName() || pol.GetSourcePpl() != string(rt.GetUpstreamTunnel().GetSshPolicy().GetRaw()) ||
		len(rt.GetPolicyIds()) != 1 || rt.GetPolicyIds()[0] != pol.GetId() || len(rt.GetPplPolicies()) != 0 {
		t.Fatalf("route %v does not reference policy %v", rt, pol)
	}
	if rt.GetName() != RoutePrefix+tn.ID+" me@x.dev" || rt.GetTo()[0] != "http://localhost:8081" || !rt.GetAllowWebsockets() {
		t.Fatalf("unexpected route %v", rt)
	}
	if _, err := m.Create(context.Background(), "me@x.dev", "nope", false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad target: %v", err)
	}
}

func TestOwnershipAndCap(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	ctx := context.Background()
	a, _ := m.Create(ctx, "a@x.dev", "localhost:1", false)
	if _, err := m.Heartbeat(a.ID, "b@x.dev"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := m.Delete(ctx, a.ID, "b@x.dev"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Create(ctx, "a@x.dev", "localhost:2", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Create(ctx, "a@x.dev", "localhost:3", false); !errors.Is(err, ErrLimit) {
		t.Fatalf("cap: %v", err)
	}
	if err := m.Delete(ctx, a.ID, "a@x.dev"); err != nil {
		t.Fatal(err)
	}
	if len(f.routes) != 1 || len(f.pols) != 1 {
		t.Fatalf("routes/policies: %d/%d", len(f.routes), len(f.pols))
	}
}

func TestLeaseExpiryAndHeartbeat(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, now := newMgr(f)
	ctx := context.Background()
	a, _ := m.Create(ctx, "a@x.dev", "localhost:1", false)
	b, _ := m.Create(ctx, "a@x.dev", "localhost:2", false)

	*now = now.Add(9 * time.Minute)
	if _, err := m.Heartbeat(a.ID, "a@x.dev"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Minute) // b expired (11m), a renewed (2m ago)
	m.Sweep(ctx)
	if len(f.routes) != 1 || len(f.pols) != 1 {
		t.Fatalf("want 1 route and policy left, got %d/%d", len(f.routes), len(f.pols))
	}
	if _, err := m.Heartbeat(b.ID, "a@x.dev"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired heartbeat: %v", err)
	}
	if _, err := m.Heartbeat(a.ID, "a@x.dev"); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptOnlyPtunRoutesWithGrace(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seed("g1", "grafana")
	f.seed("p1", RouteName("abc123", "a@x.dev"))
	f.routes["p1"].PolicyIds = []string{"pol1"}
	f.seedPolicy("pol1", RouteName("abc123", "a@x.dev"))
	m, now := newMgr(f)
	ctx := context.Background()
	if err := m.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	m.Sweep(ctx)
	if _, ok := f.routes["p1"]; !ok {
		t.Fatal("adopted route swept during grace")
	}
	if _, err := m.Heartbeat("abc123", "a@x.dev"); err != nil {
		t.Fatalf("adopted heartbeat: %v", err)
	}
	*now = now.Add(11 * time.Minute)
	m.Sweep(ctx)
	if _, ok := f.routes["p1"]; ok {
		t.Fatal("orphan not swept")
	}
	if _, ok := f.routes["g1"]; !ok {
		t.Fatal("foreign route swept")
	}
}

func TestLeaderFallback(t *testing.T) {
	t.Parallel()
	follower0, leader, follower2 := newFake(), newFake(), newFake()
	follower0.fail = connect.NewError(connect.CodeUnavailable, errors.New("stream reset"))
	follower2.fail = connect.NewError(connect.CodeUnavailable, errors.New("stream reset"))
	m, _ := newMgr(follower0, leader, follower2)
	ctx := context.Background()
	if _, err := m.Create(ctx, "a@x.dev", "localhost:1", false); err != nil {
		t.Fatal(err)
	}
	if len(leader.routes) != 1 {
		t.Fatal("route not on leader")
	}
	// The working endpoint is remembered: the failing follower is not tried first again.
	before := follower0.calls
	if _, err := m.Create(ctx, "b@x.dev", "localhost:1", false); err != nil {
		t.Fatal(err)
	}
	if follower0.calls != before {
		t.Fatalf("follower0 retried first: %d calls", follower0.calls-before)
	}
	// All endpoints failing surfaces an error and leaves no lease behind.
	leader.fail = connect.NewError(connect.CodeUnavailable, errors.New("down"))
	if _, err := m.Create(ctx, "c@x.dev", "localhost:1", false); err == nil {
		t.Fatal("want error")
	}
	if n := m.countLocked("c@x.dev"); n != 0 {
		t.Fatalf("leaked lease: %d", n)
	}
}

func TestValidateTargetLoopbackOnly(t *testing.T) {
	for _, ok := range []string{"localhost:8081", "127.0.0.1:3000", "[::1]:80", "LOCALHOST:1"} {
		if _, err := ValidateTarget(ok); err != nil {
			t.Errorf("ValidateTarget(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"tcp-echo.homelab.svc.cluster.local:7000", "10.0.0.5:80", "example.com:443", "localhost:0", "localhost"} {
		if _, err := ValidateTarget(bad); err == nil {
			t.Errorf("ValidateTarget(%q) = nil, want error", bad)
		}
	}
}

func (f *fakeAPI) CreatePolicy(_ context.Context, r *connect.Request[configpb.CreatePolicyRequest]) (*connect.Response[configpb.CreatePolicyResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	f.next++
	id := "p" + string(rune('0'+f.next))
	p := r.Msg.GetPolicy()
	p.Id = &id
	f.pols[id] = p
	return connect.NewResponse(&configpb.CreatePolicyResponse{Policy: p}), nil
}

func (f *fakeAPI) DeletePolicy(_ context.Context, r *connect.Request[configpb.DeletePolicyRequest]) (*connect.Response[configpb.DeletePolicyResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	delete(f.pols, r.Msg.GetId())
	return connect.NewResponse(&configpb.DeletePolicyResponse{}), nil
}

func (f *fakeAPI) GetPolicy(_ context.Context, r *connect.Request[configpb.GetPolicyRequest]) (*connect.Response[configpb.GetPolicyResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	p, ok := f.pols[r.Msg.GetId()]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no policy"))
	}
	return connect.NewResponse(&configpb.GetPolicyResponse{Policy: p}), nil
}

func (f *fakeAPI) ListPolicies(_ context.Context, _ *connect.Request[configpb.ListPoliciesRequest]) (*connect.Response[configpb.ListPoliciesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return nil, f.fail
	}
	var out []*configpb.Policy
	for _, p := range f.pols {
		out = append(out, p)
	}
	return connect.NewResponse(&configpb.ListPoliciesResponse{Policies: out, TotalCount: uint64(len(out))}), nil
}

func (f *fakeAPI) seedPolicy(id, name string) {
	desc := ManagedMarker
	f.pols[id] = &configpb.Policy{Id: &id, Name: &name, Description: &desc}
}

func TestRouteFailureCleansPolicy(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.failRoute = connect.NewError(connect.CodeInvalidArgument, errors.New("bad route"))
	m, _ := newMgr(f)
	if _, err := m.Create(context.Background(), "a@x.dev", "localhost:1", false); err == nil {
		t.Fatal("want error")
	}
	if len(f.pols) != 0 || len(f.routes) != 0 {
		t.Fatalf("leaked policy/route: %d/%d", len(f.pols), len(f.routes))
	}
}

func TestGuardCoversPolicies(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seedPolicy("me_only", "me_only")
	r := NewRoutes([]routeClient{f})
	if err := r.deletePolicy(context.Background(), "me_only"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("want ErrNotManaged, got %v", err)
	}
	if err := r.Delete(context.Background(), "", "me_only"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("Delete: %v", err)
	}
	if n, err := r.DeleteOrphanPolicies(context.Background(), func(*configpb.Policy) bool { return false }); err != nil || n != 0 {
		t.Fatalf("orphan sweep touched foreign policy: %d %v", n, err)
	}
	if _, ok := f.pols["me_only"]; !ok {
		t.Fatal("foreign policy deleted")
	}
}

func TestAdoptDeletesOrphanPolicies(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seedPolicy("keep", RouteName("aaaaaa", "a@x.dev"))
	f.seed("r-a", RouteName("aaaaaa", "a@x.dev"))
	f.routes["r-a"].PolicyIds = []string{"keep"}
	f.seedPolicy("orphan", RouteName("bbbbbb", "a@x.dev"))
	f.seedPolicy("shared", "me_only")
	m, _ := newMgr(f)
	if err := m.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.pols["orphan"]; ok {
		t.Fatal("orphan policy kept")
	}
	if _, ok := f.pols["keep"]; !ok {
		t.Fatal("referenced policy deleted")
	}
	if _, ok := f.pols["shared"]; !ok {
		t.Fatal("non-ptun policy deleted")
	}
	if l := m.leases["aaaaaa"]; l == nil || l.policyID != "keep" {
		t.Fatalf("lease %+v", l)
	}
}

func TestPublicTunnel(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	m.cfg.AllowPublic = true
	tn, err := m.Create(context.Background(), "me@x.dev", "localhost:3000", true)
	if err != nil || !tn.Public {
		t.Fatalf("%+v %v", tn, err)
	}
	var rt *configpb.Route
	for _, x := range f.routes {
		rt = x
	}
	var pol *configpb.Policy
	for _, x := range f.pols {
		pol = x
	}
	ppl := pol.GetSourcePpl()
	for _, want := range []string{`"accept":true`, `"me@x.dev"`, `"cors_preflight":true`} {
		if !strings.Contains(ppl, want) {
			t.Errorf("public PPL missing %s: %s", want, ppl)
		}
	}
	// Valid JSON, so valid YAML.
	var doc []map[string]any
	if err := json.Unmarshal([]byte(ppl), &doc); err != nil || len(doc) != 2 {
		t.Fatalf("not a 2-item JSON list: %v %s", err, ppl)
	}
	// The SSH side stays creator-only.
	ssh := string(rt.GetUpstreamTunnel().GetSshPolicy().GetRaw())
	creatorOnly, _ := EmailPolicy("me@x.dev")
	if ssh != string(creatorOnly) || strings.Contains(ssh, "accept") {
		t.Fatalf("ssh policy for public tunnel must be creator-only, got %q", ssh)
	}
	if !strings.Contains(rt.GetDescription(), "public tunnel") || rt.GetName() != RouteName(tn.ID, "me@x.dev") || pol.GetName() != rt.GetName() {
		t.Fatalf("name/description: %q %q", rt.GetName(), rt.GetDescription())
	}
	if hb, err := m.Heartbeat(tn.ID, "me@x.dev"); err != nil || !hb.Public {
		t.Fatalf("heartbeat public: %+v %v", hb, err)
	}
}

func TestPrivateTunnelPolicyUnchanged(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	if _, err := m.Create(context.Background(), "me@x.dev", "localhost:3000", false); err != nil {
		t.Fatal(err)
	}
	want, _ := EmailPolicy("me@x.dev")
	for _, p := range f.pols {
		if p.GetSourcePpl() != string(want) {
			t.Fatalf("private PPL changed: %q", p.GetSourcePpl())
		}
	}
	for _, r := range f.routes {
		if !strings.Contains(r.GetDescription(), "private tunnel") {
			t.Fatalf("description %q", r.GetDescription())
		}
	}
}

func TestPublicGate(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f) // AllowPublic false
	h := NewAPI(m, fakeAuth{}, m.log)
	req := httptest.NewRequest("POST", "/api/tunnels", strings.NewReader(`{"target":"localhost:1","public":true}`))
	req.Header.Set("X-Test-Email", "a@x.dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "public tunnels are disabled on this broker") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(f.routes)+len(f.pols) != 0 {
		t.Fatal("gate let something through")
	}
}

func TestMCPRoute(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	tn, err := m.CreateWith(context.Background(), "me@x.dev", CreateRequest{Target: "localhost:3000", MCP: true, MCPPath: "/sse"})
	if err != nil || !tn.MCP || tn.MCPPath != "/sse" || tn.Email != "me@x.dev" {
		t.Fatalf("%+v %v", tn, err)
	}
	var rt *configpb.Route
	for _, x := range f.routes {
		rt = x
	}
	srv := rt.GetMcp().GetServer()
	if srv == nil || srv.GetPath() != "/sse" || srv.GetUpstreamOauth2() != nil {
		t.Fatalf("mcp server not set as expected: %v", rt.GetMcp())
	}
	if !strings.Contains(rt.GetDescription(), "MCP tunnel") || !rt.GetPassIdentityHeaders() {
		t.Fatalf("desc %q pass_identity=%v", rt.GetDescription(), rt.GetPassIdentityHeaders())
	}
	if hb, err := m.Heartbeat(tn.ID, "me@x.dev"); err != nil || !hb.MCP || hb.Email != "me@x.dev" {
		t.Fatalf("heartbeat %+v %v", hb, err)
	}
	// default path
	tn2, _ := m.CreateWith(context.Background(), "me@x.dev", CreateRequest{Target: "localhost:3000", MCP: true})
	if tn2.MCPPath != "/mcp" {
		t.Fatalf("default path %q", tn2.MCPPath)
	}
	if _, err := m.CreateWith(context.Background(), "me@x.dev", CreateRequest{Target: "localhost:3000", MCP: true, MCPPath: "no-slash"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad path: %v", err)
	}
}

func TestNonMCPRouteHasNoMCP(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	tn, err := m.Create(context.Background(), "me@x.dev", "localhost:3000", false)
	if err != nil || tn.MCP || tn.MCPPath != "" {
		t.Fatalf("%+v %v", tn, err)
	}
	for _, rt := range f.routes {
		if rt.GetMcp() != nil || !rt.GetPassIdentityHeaders() {
			t.Fatalf("unexpected mcp or missing pass_identity_headers: %v", rt)
		}
	}
}

func TestMCPGate(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	m.cfg.AllowMCP = false
	if _, err := m.CreateWith(context.Background(), "a@x.dev", CreateRequest{Target: "localhost:1", MCP: true}); !errors.Is(err, ErrMCPDisabled) {
		t.Fatalf("%v", err)
	}
	if len(f.routes)+len(f.pols) != 0 {
		t.Fatal("gate let something through")
	}
}

func TestAdoptRestoresMCPAndPublic(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seed("r1", RouteName("abc123", "a@x.dev"))
	desc := "Ephemeral ptun public tunnel (MCP tunnel) for a@x.dev, " + ManagedMarker
	f.routes["r1"].Description = &desc
	f.routes["r1"].Mcp = &configpb.MCP{Mode: &configpb.MCP_Server{Server: &configpb.MCPServer{Path: proto.String("/mcp")}}}
	m, _ := newMgr(f)
	if err := m.Adopt(context.Background()); err != nil {
		t.Fatal(err)
	}
	hb, err := m.Heartbeat("abc123", "a@x.dev")
	if err != nil || !hb.MCP || hb.MCPPath != "/mcp" || !hb.Public {
		t.Fatalf("%+v %v", hb, err)
	}
}

func TestHandMadeObjectsWithPrefixAreNeverTouched(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seedHand("hand", "ephemeral: something")
	ctx := context.Background()
	r := NewRoutes([]routeClient{f})

	if err := r.Delete(ctx, "hand", ""); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("route delete: %v", err)
	}
	if err := r.Delete(ctx, "", "hand"); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("policy delete: %v", err)
	}
	if n, err := r.DeleteOrphanPolicies(ctx, func(*configpb.Policy) bool { return false }); err != nil || n != 0 {
		t.Fatalf("orphan sweep: %d %v", n, err)
	}
	if list, _ := r.ListManaged(ctx); len(list) != 0 {
		t.Fatalf("hand-made route listed as managed: %v", list)
	}
	m, now := newMgr(f)
	if err := m.Adopt(ctx); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	m.Sweep(ctx)
	if len(m.leases) != 0 {
		t.Fatalf("hand-made route adopted: %v", m.leases)
	}
	if _, ok := f.routes["hand"]; !ok {
		t.Fatal("hand-made route deleted")
	}
	if _, ok := f.pols["hand"]; !ok {
		t.Fatal("hand-made policy deleted")
	}
	// Creating with the prefix but without the marker is refused too.
	name := "ephemeral:abc123 a@x.dev"
	if _, _, err := r.Create(ctx, &configpb.Route{Name: &name}, nil); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("create without marker: %v", err)
	}
}

func TestMarkerWithoutPrefixIsNotOwned(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.seed("x", "grafana") // marker present, wrong prefix
	r := NewRoutes([]routeClient{f})
	if err := r.Delete(context.Background(), "x", ""); !errors.Is(err, ErrNotManaged) {
		t.Fatalf("%v", err)
	}
}

func TestCustomPrefix(t *testing.T) {
	t.Parallel()
	f := newFake()
	cfg := testCfg
	cfg.RoutePrefix = "tmp:"
	m := NewManager(cfg, NewRoutesWithPrefix([]routeClient{f}, "tmp:"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	tn, err := m.Create(context.Background(), "a@x.dev", "localhost:1", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range f.routes {
		if rt.GetName() != "tmp:"+tn.ID+" a@x.dev" || !strings.Contains(rt.GetDescription(), ManagedMarker) {
			t.Fatalf("%q %q", rt.GetName(), rt.GetDescription())
		}
	}
	if err := m.Delete(context.Background(), tn.ID, "a@x.dev"); err != nil || len(f.routes)+len(f.pols) != 0 {
		t.Fatalf("delete: %v %d", err, len(f.routes)+len(f.pols))
	}
	for _, bad := range []string{"", "nocolon", "has space:"} {
		if ValidatePrefix(bad) == nil {
			t.Errorf("prefix %q accepted", bad)
		}
	}
	if ValidatePrefix("ephemeral:") != nil {
		t.Error("default prefix rejected")
	}
}
