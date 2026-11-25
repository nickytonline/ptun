package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SSHArgs builds the ssh argument list for a tunnel.
func SSHArgs(t Tunnel, target string) []string {
	return []string{
		"-p", strconv.Itoa(t.SSHPort),
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		// No ServerAliveInterval: Pomerium's SSH proxy drops the session when OpenSSH sends its keepalive request
		// ("generic proxy: ... unknown stream id", 30s after the last activity). The broker's heartbeat/TTL already
		// cleans up dead tunnels.
		"-o", "ServerAliveInterval=0",
		"-R", t.Hostname + ":443:" + target,
		t.SSHHost,
	}
}

// Options configures Run.
type Options struct {
	Target      string // already parsed host:port
	Arg         string // what the user typed (for messages)
	Public      bool   // anyone with the link may open it
	MCP         bool   // share as a remote MCP server
	MCPPath     string // MCP endpoint path for the printed URL (default /mcp)
	Stable      bool   // same URL every run (<id>.<domain>)
	Name        string // same URL every run, with a name (<name>-<id>.<domain>); implies Stable
	Quiet       bool   // no per-request log lines
	ShowVisitor bool   // append the visitor email to log lines
	NoCheck     bool   // skip the "is anything listening" pre-flight
	Dir         string
	Out         io.Writer
	Clipboard   func(string) bool // copies text; nil uses the system clipboard
}

// readyAfterPrivate is how long ssh must stay up without error before a
// private tunnel counts as live. A private link answers the self-probe with a
// Pomerium sign-in redirect, so the probe never reaches the proxy; "ssh still
// running with ExitOnForwardFailure and no error" is the best signal we have.
const readyAfterPrivate = 5 * time.Second

// Run creates a tunnel, runs ssh -R through a local logging proxy, heartbeats,
// and cleans up. It returns the process exit code.
func Run(ctx context.Context, o Options) (int, error) {
	cfg, err := LoadConfig(o.Dir)
	if err != nil {
		return 1, err
	}
	if o.Arg == "" {
		o.Arg = o.Target
	}
	if !o.NoCheck {
		if err := CheckPort(ctx, o.Target, o.Arg); err != nil {
			return 1, err
		}
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	c := NewClient(cfg.Broker, o.Dir, o.Out)
	if u := os.Getenv("PTUN_BROKER_URL"); u != "" {
		// Local testing against a broker started with DEV_AUTH_EMAIL: plain HTTP
		// on loopback, no Pomerium login.
		scheme, host, ok := strings.Cut(u, "://")
		if !ok || scheme != "http" || !(strings.HasPrefix(host, "127.0.0.1:") || strings.HasPrefix(host, "localhost:")) {
			return 1, fmt.Errorf("PTUN_BROKER_URL must be http://127.0.0.1:<port> or http://localhost:<port>")
		}
		c.Broker, c.Login.Scheme = strings.TrimSuffix(host, "/"), scheme
		c.mu.Lock()
		c.jwt = "dev"
		c.mu.Unlock()
		fmt.Fprintf(o.Out, "Using local broker %s (PTUN_BROKER_URL, no sign-in)\n", u)
	}

	// The proxy comes up first so nothing is created on the server if we cannot listen.
	var logw io.Writer
	if !o.Quiet {
		logw = o.Out
	}
	proxy := NewProxy(ProxyOptions{Target: o.Target, Log: logw, ShowVisitor: o.ShowVisitor})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1, fmt.Errorf("start local proxy: %w", err)
	}
	srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 15 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	proxyAddr := ln.Addr().String()

	t, err := c.CreateWith(ctx, CreateParams{Target: o.Target, Public: o.Public, MCP: o.MCP, MCPPath: o.MCPPath, Stable: o.Stable || o.Name != "", Name: o.Name})
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil
		}
		return 1, FriendlyError(err)
	}
	if t.Email != "" {
		fmt.Fprintf(o.Out, "✔ Signed in as %s\n", t.Email)
	}
	fmt.Fprintln(o.Out, "✔ Created your link")
	fmt.Fprintln(o.Out, "⏳ Connecting… (if a sign-in link appears below, open it)")

	// ssh forwards to the proxy, not to the app. Only sign-in links and errors reach the terminal.
	sshErr := newSSHFilter(os.Stderr)
	cmd := exec.Command("ssh", SSHArgs(t, proxyAddr)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, sshErr
	if err := cmd.Start(); err != nil {
		cleanup(c, t.ID, o.Out, false)
		return 1, fmt.Errorf("start ssh: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Readiness: first request through the proxy, or the self-probe (public), or ssh surviving (private).
	liveCh := make(chan struct{})
	var liveOnce sync.Once
	markLive := func() { liveOnce.Do(func() { close(liveCh) }) }
	probeCtx, cancelProbe := context.WithCancel(ctx)
	defer cancelProbe()
	go func() {
		select {
		case <-proxy.Ready():
			markLive()
		case <-probeCtx.Done():
		}
	}()
	// Public links can be probed end to end. Private links answer the probe with a Pomerium sign-in redirect and MCP
	// links with 401 + WWW-Authenticate, so neither reaches the proxy: they use the ssh-survived heuristic.
	if t.Public && !t.MCP {
		go probeLoop(probeCtx, t.URL, proxy.ProbeToken())
	} else {
		go func() {
			select {
			case <-time.After(readyAfterPrivate):
				markLive()
			case <-probeCtx.Done():
			}
		}()
	}

	iv := time.Duration(t.HeartbeatIntervalSeconds) * time.Second
	if iv <= 0 {
		iv = 2 * time.Minute
	}
	tick := time.NewTicker(iv)
	defer tick.Stop()

	liveC := (<-chan struct{})(liveCh) // set to nil once fired so select ignores it
	announced := false
	var waitErr error
loop:
	for {
		select {
		case <-liveC:
			if !announced {
				announced = true
				announceLive(o, t)
			}
			liveC = nil
		case <-tick.C:
			hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Heartbeat(hctx, t.ID)
			cancel()
			var api *APIError
			if t.Stable && errors.As(err, &api) && api.Status == http.StatusNotFound {
				// The lease ran out (e.g. the laptop slept through heartbeats) and the link was freed.
				_ = cmd.Process.Signal(syscall.SIGTERM)
				<-done
				fmt.Fprintf(o.Out, "✖ %s expired (no heartbeat reached the server in time, e.g. after sleep). Stopped; run ptun again to get it back.\n", t.URL)
				return 0, nil
			}
			if err != nil {
				fmt.Fprintf(o.Out, "warning: heartbeat failed: %v\n", err)
			}
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
			waitErr = <-done
			break loop
		case waitErr = <-done:
			break loop
		}
	}
	userStopped := ctx.Err() != nil
	cleanup(c, t.ID, o.Out, userStopped)

	var ee *exec.ExitError
	switch {
	case userStopped || waitErr == nil:
		return 0, nil
	case sshErr.PermissionDenied():
		return 255, SSHRejected(t.Email)
	case errors.As(waitErr, &ee):
		return ee.ExitCode(), fmt.Errorf("The connection to the ptun server ended unexpectedly (ssh exit code %d).", ee.ExitCode())
	default:
		return 1, waitErr
	}
}

func announceLive(o Options, t Tunnel) {
	// For an MCP tunnel the useful thing to paste is the MCP endpoint, not the bare URL.
	mcpURL := ""
	toCopy := t.URL
	if t.MCP {
		mcpURL = MCPURL(t)
		toCopy = mcpURL
	}
	copied := false
	if o.Clipboard != nil {
		copied = o.Clipboard(toCopy)
	} else {
		copied = CopyToClipboard(toCopy)
	}
	note := ""
	if copied {
		note = "      (copied to clipboard)"
	}
	who := "only you, after signing in"
	if t.Public {
		who = "anyone with the link (public)"
	}
	fmt.Fprintf(o.Out, "✔ Live! Your app on %s is at:\n", o.Target)
	if t.MCP {
		fmt.Fprintf(o.Out, "    %s\n  MCP server URL: %s%s\n", t.URL, mcpURL, note)
	} else {
		fmt.Fprintf(o.Out, "    %s%s\n", t.URL, note)
	}
	fmt.Fprintf(o.Out, "  Who can open it: %s\n", who)
	if t.Stable {
		fmt.Fprintln(o.Out, "  This link is the same every time you run this command. It only works while ptun is running.")
		if t.Public {
			fmt.Fprintln(o.Out, "  Heads-up: a public link that never changes can be found again by anyone who has seen it.")
		}
	}
	if t.MCP {
		fmt.Fprintln(o.Out, "  Add it to your MCP client (e.g. Claude) as a remote MCP server. It will sign you in through Pomerium.")
	}
	fmt.Fprintln(o.Out, "  Press Ctrl-C to stop sharing.")
}

// MCPURL is the URL to give an MCP client: the tunnel URL plus the MCP path.
func MCPURL(t Tunnel) string {
	p := t.MCPPath
	if p == "" {
		p = "/mcp"
	}
	return strings.TrimSuffix(t.URL, "/") + p
}

// probeLoop GETs the public URL once a second for ~30s with the probe header.
// The proxy answers it with 204 and signals readiness; this loop only has to
// keep the requests coming. It stops when ctx is cancelled (ready or exiting).
func probeLoop(ctx context.Context, url, token string) {
	hc := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
		if err != nil {
			return
		}
		req.Header.Set(ProbeHeader, token)
		if res, err := hc.Do(req); err == nil {
			res.Body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func cleanup(c *Client, id string, out io.Writer, userStopped bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Delete(ctx, id); err != nil {
		fmt.Fprintf(out, "warning: could not remove the link right away (it expires on its own): %v\n", err)
		return
	}
	if userStopped {
		fmt.Fprintln(out, "✔ Stopped. That link no longer works.")
	}
}
