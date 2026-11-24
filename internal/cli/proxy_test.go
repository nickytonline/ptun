package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newProxyServer(t *testing.T, target string, log io.Writer, showVisitor bool) (*Proxy, *httptest.Server) {
	t.Helper()
	p := NewProxy(ProxyOptions{Target: target, Log: log, ShowVisitor: showVisitor})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, srv
}

func TestProxyForwardsAndLogs(t *testing.T) {
	t.Parallel()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") == "" {
			t.Error("X-Forwarded-For stripped")
		}
		w.Header().Set("X-App", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	t.Cleanup(app.Close)
	var log syncBuf
	jwt := "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"v@x.dev"}`)) + ".s"
	p, srv := newProxyServer(t, strings.TrimPrefix(app.URL, "http://"), &log, true)

	req, _ := http.NewRequest("GET", srv.URL+"/a/b?x=1", nil)
	req.Header.Set("X-Pomerium-Jwt-Assertion", jwt)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 418 || string(body) != "hello /a/b" || res.Header.Get("X-App") != "yes" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
	select {
	case <-p.Ready():
	default:
		t.Fatal("first request should mark ready")
	}
	line := log.String()
	if !strings.Contains(line, "GET     /a/b?x=1  418  ") || !strings.HasSuffix(strings.TrimSpace(line), "v@x.dev") {
		t.Fatalf("log line: %q", line)
	}
}

func TestFormatLogLine(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 1, 2, 13, 4, 5, 0, time.UTC)
	got := FormatLogLine(ts, "POST", "/x", 200, 12*time.Millisecond+400*time.Microsecond, "", "")
	if got != "13:04:05  POST    /x  200  12ms" {
		t.Fatalf("%q", got)
	}
	if got := FormatLogLine(ts, "GET", "/ws", 101, time.Second, "", ""); !strings.Contains(got, "101 (websocket)") {
		t.Fatalf("%q", got)
	}
}

func TestProxyBadGatewayPage(t *testing.T) {
	t.Parallel()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close() // nothing listens here any more
	var log syncBuf
	_, srv := newProxyServer(t, dead, &log, false)
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 502 || !strings.Contains(string(b), "isn't running right now") || !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	if !strings.Contains(log.String(), "502") || !strings.Contains(log.String(), "→ nothing running on "+dead) {
		t.Fatalf("log: %q", log.String())
	}
}

func TestProxyProbe(t *testing.T) {
	t.Parallel()
	hit := make(chan struct{}, 1)
	app := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit <- struct{}{} }))
	t.Cleanup(app.Close)
	var log syncBuf
	p, srv := newProxyServer(t, strings.TrimPrefix(app.URL, "http://"), &log, false)

	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set(ProbeHeader, "wrong")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	select {
	case <-hit: // wrong token is a normal request, forwarded
	case <-time.After(2 * time.Second):
		t.Fatal("wrong probe token must be forwarded")
	}
	log2 := log.String()
	if log2 == "" {
		t.Fatal("wrong token should be logged")
	}

	p2, srv2 := newProxyServer(t, strings.TrimPrefix(app.URL, "http://"), &log, false)
	_ = p
	req, _ = http.NewRequest("GET", srv2.URL+"/", nil)
	req.Header.Set(ProbeHeader, p2.ProbeToken())
	before := log.String()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 204 {
		t.Fatalf("probe status %d", res.StatusCode)
	}
	select {
	case <-p2.Ready():
	default:
		t.Fatal("probe should mark ready")
	}
	select {
	case <-hit:
		t.Fatal("probe was forwarded")
	default:
	}
	if log.String() != before {
		t.Fatal("probe was logged")
	}
}

func TestProxyWebSocketUpgrade(t *testing.T) {
	t.Parallel()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(brw, buf); err == nil {
			_, _ = conn.Write(buf)
		}
	}))
	t.Cleanup(app.Close)
	var log syncBuf
	_, srv := newProxyServer(t, strings.TrimPrefix(app.URL, "http://"), &log, false)

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: abc\r\nSec-WebSocket-Version: 13\r\n\r\n")
	br := bufio.NewReader(conn)
	status, _ := br.ReadString('\n')
	if !strings.Contains(status, "101") {
		t.Fatalf("status %q", status)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil || l == "\r\n" {
			break
		}
	}
	_, _ = io.WriteString(conn, "ping")
	echo := make([]byte, 4)
	if _, err := io.ReadFull(br, echo); err != nil || string(echo) != "ping" {
		t.Fatalf("echo %q %v", echo, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(log.String(), "101 (websocket)") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(log.String(), "101 (websocket)") {
		t.Fatalf("log %q", log.String())
	}
}

func TestCheckPort(t *testing.T) {
	t.Parallel()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	if err := CheckPort(context.Background(), ln.Addr().String(), "1"); err != nil {
		t.Fatal(err)
	}
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := dead.Addr().String()
	dead.Close()
	err := CheckPort(context.Background(), addr, "3000")
	var nr *ErrNothingRunning
	if !errors.As(err, &nr) || err.Error() != "Nothing is running on "+addr+" yet. Start your app, then run ptun 3000 again." {
		t.Fatalf("%v", err)
	}
}

func TestFriendlyError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		err  error
		want string
	}{
		{&APIError{Status: 403, Message: "public tunnels are disabled on this broker"}, "Your organization doesn't allow public links. Use ptun 3000 for a private link."},
		{&UnreachableError{Err: errors.New("dial tcp: no route")}, "Can't reach your organization's ptun service. Check your connection, or ask your admin whether ptun is set up."},
		{ErrNotConfigured, "ptun isn't set up yet. Run the setup command your admin gave you, e.g. ptun init ssh.example.com:2200"},
	}
	for _, c := range cases {
		if got := FriendlyError(c.err).Error(); got != c.want {
			t.Errorf("got %q want %q", got, c.want)
		}
	}
	if got := SSHRejected("a@x.dev").Error(); got != "The SSH sign-in was rejected. Make sure you signed in as a@x.dev (the account ptun is using)." {
		t.Errorf("%q", got)
	}
	if _, err := LoadConfig(t.TempDir()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("LoadConfig: %v", err)
	}
}

func TestSSHFilter(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	f := newSSHFilter(&out)
	_, _ = io.WriteString(f, "Pseudo-terminal will not be allocated\nSign in at https://auth.example.com/x?y=1\nwarning: noise ")
	_, _ = io.WriteString(f, "continues\nboring line\nPermission denied (publickey).\n")
	got := out.String()
	for _, want := range []string{"https://auth.example.com/x?y=1", "warning: noise continues", "Permission denied"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "boring") || strings.Contains(got, "Pseudo-terminal") {
		t.Errorf("noise leaked: %q", got)
	}
	if !f.PermissionDenied() {
		t.Error("permission denied not detected")
	}
}

func TestBrokerJSON403IsNotReauth(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = io.WriteString(w, `{"error":"public tunnels are disabled on this broker"}`)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()
	_ = saveToken(dir, host, "jwt")
	c := NewClient(host, dir, io.Discard)
	c.Login = LoginOptions{Scheme: "http", Open: func(string) { t.Error("must not re-login") }}
	_, err := c.Create(context.Background(), "localhost:1", true)
	if want := "Your organization doesn't allow public links. Use ptun 3000 for a private link."; FriendlyError(err).Error() != want {
		t.Fatalf("%v", err)
	}
}

func TestCreateWithSendsMCP(t *testing.T) {
	t.Parallel()
	var got syncBuf
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Write(b)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"abc123","url":"https://t-abc123.x.dev","mcp":true,"mcp_path":"/sse","email":"me@x.dev"}`)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()
	_ = saveToken(dir, host, "jwt")
	c := NewClient(host, dir, io.Discard)
	c.Login.Scheme = "http"
	tn, err := c.CreateWith(context.Background(), CreateParams{Target: "localhost:3000", MCP: true, MCPPath: "/sse"})
	if err != nil || !tn.MCP || tn.Email != "me@x.dev" || tn.MCPPath != "/sse" {
		t.Fatalf("%+v %v", tn, err)
	}
	for _, want := range []string{`"mcp":true`, `"mcp_path":"/sse"`, `"target":"localhost:3000"`} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("body %q missing %s", got.String(), want)
		}
	}
}

func TestAnnounceLiveMCP(t *testing.T) {
	t.Parallel()
	var out, copied syncBuf
	tn := Tunnel{URL: "https://t-abc123.x.dev", MCP: true, MCPPath: "/mcp"}
	announceLive(Options{Target: "localhost:3000", Out: &out, Clipboard: func(s string) bool { copied.Write([]byte(s)); return true }}, tn)
	if copied.String() != "https://t-abc123.x.dev/mcp" {
		t.Fatalf("clipboard %q", copied.String())
	}
	for _, want := range []string{"✔ Live! Your app on localhost:3000 is at:", "MCP server URL: https://t-abc123.x.dev/mcp", "(copied to clipboard)",
		"Add it to your MCP client (e.g. Claude) as a remote MCP server. It will sign you in through Pomerium.", "only you, after signing in", "Press Ctrl-C to stop sharing."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestAnnounceLivePlain(t *testing.T) {
	t.Parallel()
	var out, copied syncBuf
	announceLive(Options{Target: "localhost:3000", Out: &out, Clipboard: func(s string) bool { copied.Write([]byte(s)); return false }}, Tunnel{URL: "https://t-x.dev", Public: true})
	if copied.String() != "https://t-x.dev" || strings.Contains(out.String(), "copied to clipboard") || strings.Contains(out.String(), "MCP") ||
		!strings.Contains(out.String(), "anyone with the link (public)") {
		t.Fatalf("%q / %q", copied.String(), out.String())
	}
}
