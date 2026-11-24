package broker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	configpb "github.com/pomerium/pomerium/pkg/grpc/config"
	"google.golang.org/protobuf/proto"
)

// Errors returned by Manager, mapped to HTTP statuses by the API layer.
var (
	ErrNotFound     = errors.New("tunnel not found")
	ErrForbidden    = errors.New("tunnel belongs to another user")
	ErrLimit        = errors.New("tunnel limit reached")
	ErrInvalidInput = errors.New("invalid input")
	// ErrPublicDisabled is returned when public is requested but ALLOW_PUBLIC_TUNNELS is off.
	ErrPublicDisabled = errors.New("public tunnels are disabled on this broker")
	// ErrMCPDisabled is returned when mcp is requested but ALLOW_MCP_TUNNELS is off.
	ErrMCPDisabled = errors.New("MCP tunnels are disabled on this broker")
)

// Config holds Manager settings.
type Config struct {
	AllowPublic       bool   // ALLOW_PUBLIC_TUNNELS
	AllowMCP          bool   // ALLOW_MCP_TUNNELS (default true)
	RoutePrefix       string // ROUTE_PREFIX; empty means DefaultRoutePrefix
	BaseDomain        string
	SSHHost           string
	SSHPort           int
	TTL               time.Duration
	HeartbeatInterval time.Duration
	MaxPerUser        int
}

// Tunnel is the JSON returned to the CLI.
type Tunnel struct {
	ID                       string    `json:"id"`
	URL                      string    `json:"url"`
	Hostname                 string    `json:"hostname"`
	SSHHost                  string    `json:"ssh_host"`
	SSHPort                  int       `json:"ssh_port"`
	Email                    string    `json:"email"` // the authenticated caller
	Public                   bool      `json:"public"`
	MCP                      bool      `json:"mcp"`
	MCPPath                  string    `json:"mcp_path,omitempty"`
	ExpiresAt                time.Time `json:"expires_at"`
	HeartbeatIntervalSeconds int       `json:"heartbeat_interval_seconds"`
}

type lease struct {
	routeID  string
	policyID string
	email    string
	public   bool
	mcp      bool
	mcpPath  string
	expires  time.Time
}

// Manager tracks tunnel leases and the routes behind them.
type Manager struct {
	cfg    Config
	routes RouteStore
	log    *slog.Logger
	now    func() time.Time

	mu     sync.Mutex
	leases map[string]*lease
}

func (c Config) prefix() string {
	if c.RoutePrefix == "" {
		return DefaultRoutePrefix
	}
	return c.RoutePrefix
}

// NewManager returns a Manager.
func NewManager(cfg Config, routes RouteStore, log *slog.Logger) *Manager {
	return &Manager{cfg: cfg, routes: routes, log: log, now: time.Now, leases: map[string]*lease{}}
}

// ValidateTarget checks a host:port target and returns it normalized.
func ValidateTarget(target string) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return "", fmt.Errorf("%w: target must be host:port", ErrInvalidInput)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%w: bad port", ErrInvalidInput)
	}
	// Loopback only. The route's `to` is only a placeholder (the client's -R spec
	// picks the real target), but if Pomerium ever fell back to `to` with no
	// tunnel attached, any other host would let a tunnel route reach in-cluster
	// services.
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
	default:
		return "", fmt.Errorf("%w: host must be localhost, 127.0.0.1 or ::1", ErrInvalidInput)
	}
	return net.JoinHostPort(host, port), nil
}

// BuildRoute builds the Pomerium route for a tunnel.
func BuildRoute(id, email, target string, public bool, cfg Config) (*configpb.Route, error) {
	return buildRoute(id, email, target, CreateRequest{Public: public}, cfg)
}

func buildRoute(id, email, target string, req CreateRequest, cfg Config) (*configpb.Route, error) {
	public := req.Public
	ppl, err := EmailPolicy(email)
	if err != nil {
		return nil, err
	}
	name := RouteNameWith(cfg.prefix(), id, email)
	kind := "private tunnel"
	if public {
		kind = "public tunnel"
	}
	if req.MCP {
		kind += " (MCP tunnel)"
	}
	desc := fmt.Sprintf("Ephemeral ptun %s for %s, %s. Do not edit.", kind, email, ManagedMarker)
	route := &configpb.Route{
		Name:            &name,
		Description:     &desc,
		From:            "https://" + Hostname(id, cfg.BaseDomain),
		To:              []string{"http://" + target},
		AllowWebsockets: true,
		// Lets the ptun CLI show who is visiting (--show-visitor) from X-Pomerium-Jwt-Assertion.
		PassIdentityHeaders: proto.Bool(true),
		UpstreamTunnel:      &configpb.UpstreamTunnel{SshPolicy: &configpb.PPLPolicy{Raw: ppl}},
	}
	if req.MCP {
		// MCPServer.path is the path Pomerium appends when it lists this server's URL (.mcp/routes), i.e. the
		// endpoint clients should use. No upstream_oauth2: the app on the laptop has no OAuth of its own to bridge.
		route.Mcp = &configpb.MCP{Mode: &configpb.MCP_Server{Server: &configpb.MCPServer{Path: proto.String(req.MCPPath)}}}
	}
	return route, nil
}

func (m *Manager) tunnel(id, email string, public, mcp bool, mcpPath string, expires time.Time) Tunnel {
	h := Hostname(id, m.cfg.BaseDomain)
	return Tunnel{
		ID: id, URL: "https://" + h, Hostname: h,
		SSHHost: m.cfg.SSHHost, SSHPort: m.cfg.SSHPort,
		Email:                    email,
		Public:                   public,
		MCP:                      mcp,
		MCPPath:                  mcpPath,
		ExpiresAt:                expires,
		HeartbeatIntervalSeconds: int(m.cfg.HeartbeatInterval / time.Second),
	}
}

// CreateRequest is what a caller asks for.
type CreateRequest struct {
	Target  string
	Public  bool   // anyone with the link may open it
	MCP     bool   // route fronts an MCP server
	MCPPath string // MCP endpoint path; default /mcp
}

// ValidateMCPPath checks an MCP endpoint path ("" gives the default).
func ValidateMCPPath(p string) (string, error) {
	if p == "" {
		return "/mcp", nil
	}
	if len(p) > 200 || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("%w: mcp_path must start with a single /", ErrInvalidInput)
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f || r == '?' || r == '#' || r == '\\' {
			return "", fmt.Errorf("%w: bad mcp_path", ErrInvalidInput)
		}
	}
	return p, nil
}

// Create makes a new tunnel for email.
func (m *Manager) Create(ctx context.Context, email, target string, public bool) (Tunnel, error) {
	return m.CreateWith(ctx, email, CreateRequest{Target: target, Public: public})
}

// CreateWith makes a new tunnel for email.
func (m *Manager) CreateWith(ctx context.Context, email string, req CreateRequest) (Tunnel, error) {
	public, target := req.Public, req.Target
	if public && !m.cfg.AllowPublic {
		return Tunnel{}, ErrPublicDisabled
	}
	if req.MCP {
		if !m.cfg.AllowMCP {
			return Tunnel{}, ErrMCPDisabled
		}
		p, err := ValidateMCPPath(req.MCPPath)
		if err != nil {
			return Tunnel{}, err
		}
		req.MCPPath = p
	} else {
		req.MCPPath = ""
	}
	if email == "" {
		return Tunnel{}, fmt.Errorf("%w: missing email", ErrInvalidInput)
	}
	target, err := ValidateTarget(target)
	if err != nil {
		return Tunnel{}, err
	}
	m.mu.Lock()
	if m.countLocked(email) >= m.cfg.MaxPerUser {
		m.mu.Unlock()
		return Tunnel{}, ErrLimit
	}
	id, err := NewID()
	if err != nil {
		m.mu.Unlock()
		return Tunnel{}, err
	}
	// Reserve the slot before the (slow) API call so concurrent creates cannot exceed the cap.
	expires := m.now().Add(m.cfg.TTL)
	l := &lease{email: email, public: public, mcp: req.MCP, mcpPath: req.MCPPath, expires: expires}
	m.leases[id] = l
	m.mu.Unlock()

	route, err := buildRoute(id, email, target, req, m.cfg)
	if err == nil {
		var ppl []byte
		if ppl, err = VisitorPolicy(email, public); err == nil {
			l.routeID, l.policyID, err = m.routes.Create(ctx, route, ppl)
		}
	}
	if err != nil {
		m.mu.Lock()
		delete(m.leases, id)
		m.mu.Unlock()
		return Tunnel{}, fmt.Errorf("create tunnel: %w", err)
	}
	m.log.Info("tunnel created", "id", id, "email", email, "route_id", l.routeID, "target", target, "public", public, "mcp", req.MCP)
	return m.tunnel(id, email, public, req.MCP, req.MCPPath, expires), nil
}

func (m *Manager) countLocked(email string) int {
	n := 0
	for _, l := range m.leases {
		if l.email == email {
			n++
		}
	}
	return n
}

func (m *Manager) owned(id, email string) (*lease, error) {
	l, ok := m.leases[id]
	if !ok || !m.now().Before(l.expires) {
		return nil, ErrNotFound
	}
	if l.email != email {
		return nil, ErrForbidden
	}
	return l, nil
}

// Heartbeat extends a tunnel's lease.
func (m *Manager) Heartbeat(id, email string) (Tunnel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, err := m.owned(id, email)
	if err != nil {
		return Tunnel{}, err
	}
	l.expires = m.now().Add(m.cfg.TTL)
	return m.tunnel(id, l.email, l.public, l.mcp, l.mcpPath, l.expires), nil
}

// Delete removes a tunnel and its route.
func (m *Manager) Delete(ctx context.Context, id, email string) error {
	m.mu.Lock()
	l, err := m.owned(id, email)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.remove(ctx, id, l.routeID, l.policyID)
}

func (m *Manager) remove(ctx context.Context, id, routeID, policyID string) error {
	if err := m.routes.Delete(ctx, routeID, policyID); err != nil {
		return fmt.Errorf("delete tunnel: %w", err)
	}
	m.mu.Lock()
	delete(m.leases, id)
	m.mu.Unlock()
	m.log.Info("tunnel removed", "id", id, "route_id", routeID, "policy_id", policyID)
	return nil
}

// Sweep deletes expired leases. Failures stay in the map and are retried.
func (m *Manager) Sweep(ctx context.Context) {
	type item struct{ id, routeID, policyID string }
	var expired []item
	m.mu.Lock()
	now := m.now()
	for id, l := range m.leases {
		if !now.Before(l.expires) && l.routeID != "" {
			expired = append(expired, item{id, l.routeID, l.policyID})
		}
	}
	m.mu.Unlock()
	for _, it := range expired {
		if err := m.remove(ctx, it.id, it.routeID, it.policyID); err != nil {
			m.log.Warn("sweep failed", "id", it.id, "err", err)
		}
	}
}

// Adopt loads existing ephemeral: routes (after a broker restart) with a fresh TTL,
// taking each policy id from the route's policy_ids. It then deletes ephemeral:*
// policies that no ephemeral:* route references and that belong to no lease
// (leftovers of a crash between CreatePolicy and CreateRoute).
func (m *Manager) Adopt(ctx context.Context) error {
	routes, err := m.routes.ListManaged(ctx)
	if err != nil {
		return err
	}
	referenced := map[string]bool{}
	m.mu.Lock()
	for _, rt := range routes {
		for _, pid := range rt.GetPolicyIds() {
			referenced[pid] = true
		}
		id, email, ok := IDFromRouteNameWith(m.cfg.prefix(), rt.GetName())
		if !ok || rt.GetId() == "" {
			continue
		}
		if _, exists := m.leases[id]; exists {
			continue
		}
		l := &lease{routeID: rt.GetId(), email: email, expires: m.now().Add(m.cfg.TTL)}
		l.public = strings.Contains(rt.GetDescription(), "public tunnel")
		if srv := rt.GetMcp().GetServer(); srv != nil {
			l.mcp, l.mcpPath = true, srv.GetPath()
		}
		if pids := rt.GetPolicyIds(); len(pids) > 0 {
			l.policyID = pids[0]
		}
		m.leases[id] = l
		m.log.Info("adopted tunnel", "id", id, "email", email, "route_id", rt.GetId(), "policy_id", l.policyID)
	}
	m.mu.Unlock()

	n, err := m.routes.DeleteOrphanPolicies(ctx, func(p *configpb.Policy) bool {
		if referenced[p.GetId()] {
			return true
		}
		id, _, ok := IDFromRouteNameWith(m.cfg.prefix(), p.GetName())
		m.mu.Lock()
		defer m.mu.Unlock()
		_, leased := m.leases[id] // includes in-flight creates, which reserve the id first
		return !ok || leased
	})
	if n > 0 {
		m.log.Info("deleted orphan policies", "count", n)
	}
	return err
}

// Run sweeps every interval until ctx is done.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep(ctx)
		}
	}
}
