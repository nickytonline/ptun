package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// Tunnel mirrors the broker's JSON response.
type Tunnel struct {
	ID                       string    `json:"id"`
	URL                      string    `json:"url"`
	Hostname                 string    `json:"hostname"`
	SSHHost                  string    `json:"ssh_host"`
	SSHPort                  int       `json:"ssh_port"`
	Email                    string    `json:"email"`
	Public                   bool      `json:"public"`
	MCP                      bool      `json:"mcp"`
	MCPPath                  string    `json:"mcp_path"`
	Stable                   bool      `json:"stable"`
	ExpiresAt                time.Time `json:"expires_at"`
	HeartbeatIntervalSeconds int       `json:"heartbeat_interval_seconds"`
}

var errUnauthenticated = errors.New("not authenticated")

// Client talks to the broker with a Pomerium session token.
type Client struct {
	Broker string
	Dir    string // config dir for the token cache
	Out    io.Writer
	HTTP   *http.Client
	Login  LoginOptions

	mu  sync.Mutex
	jwt string
}

// NewClient returns a client with the cached token, if any.
func NewClient(broker, dir string, out io.Writer) *Client {
	return &Client{
		Broker: broker, Dir: dir, Out: out,
		HTTP: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		jwt: loadToken(dir, broker),
	}
}

func (c *Client) scheme() string {
	if c.Login.Scheme != "" {
		return c.Login.Scheme
	}
	return "https"
}

func (c *Client) token() string { c.mu.Lock(); defer c.mu.Unlock(); return c.jwt }

func (c *Client) relogin(ctx context.Context) error {
	dropToken(c.Dir)
	j, err := Login(ctx, c.Broker, c.Out, c.Login)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.jwt = j
	c.mu.Unlock()
	if err := saveToken(c.Dir, c.Broker, j); err != nil {
		fmt.Fprintf(c.Out, "warning: could not cache token: %v\n", err)
	}
	return nil
}

func (c *Client) once(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.scheme()+"://"+c.Broker+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if t := c.token(); t != "" {
		req.Header.Set("Authorization", "Pomerium "+t)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("can't reach the tunnel broker at https://%s: %v\n"+
			"  Is the broker deployed? Re-run `ptun init <pomerium-ssh-host>` if the host is wrong.", c.Broker, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	// The broker answers errors as JSON {"error": ...}; Pomerium's own 401/403/redirect pages are not JSON.
	// Telling them apart matters: only Pomerium's mean "sign in again".
	var e struct {
		Error string `json:"error"`
	}
	brokerJSON := json.Unmarshal(b, &e) == nil && e.Error != ""
	switch {
	case c.token() == "", res.StatusCode == http.StatusUnauthorized,
		res.StatusCode == http.StatusForbidden && !brokerJSON,
		res.StatusCode >= 300 && res.StatusCode < 400:
		return fmt.Errorf("%w (%s)", errUnauthenticated, res.Status)
	case res.StatusCode >= 400:
		msg := e.Error
		if !brokerJSON {
			msg = res.Status
		}
		return &APIError{Status: res.StatusCode, Message: msg}
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}

// do calls the broker. With reauth, a 401/403/redirect drops the cached token
// and signs in again once.
func (c *Client) do(ctx context.Context, reauth bool, method, path string, in, out any) error {
	err := c.once(ctx, method, path, in, out)
	if reauth && errors.Is(err, errUnauthenticated) {
		if err := c.relogin(ctx); err != nil {
			return err
		}
		err = c.once(ctx, method, path, in, out)
	}
	if errors.Is(err, errUnauthenticated) {
		return fmt.Errorf("%w: sign in again with a fresh run of ptun", err)
	}
	return err
}

// CreateParams is what the CLI asks the broker for.
type CreateParams struct {
	Target  string `json:"target"`
	Public  bool   `json:"public"`
	MCP     bool   `json:"mcp,omitempty"`
	MCPPath string `json:"mcp_path,omitempty"`
	Stable  bool   `json:"stable,omitempty"`
	Name    string `json:"name,omitempty"`
}

// Create asks the broker for a tunnel to target.
func (c *Client) Create(ctx context.Context, target string, public bool) (Tunnel, error) {
	return c.CreateWith(ctx, CreateParams{Target: target, Public: public})
}

// CreateWith asks the broker for a tunnel.
func (c *Client) CreateWith(ctx context.Context, p CreateParams) (Tunnel, error) {
	var t Tunnel
	err := c.do(ctx, true, http.MethodPost, "/api/tunnels", p, &t)
	return t, err
}

// Heartbeat extends the tunnel lease.
func (c *Client) Heartbeat(ctx context.Context, id string) error {
	return c.do(ctx, false, http.MethodPost, "/api/tunnels/"+id+"/heartbeat", nil, nil)
}

// Delete removes the tunnel.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, false, http.MethodDelete, "/api/tunnels/"+id, nil, nil)
}
