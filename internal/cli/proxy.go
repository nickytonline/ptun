package cli

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"
)

// ProbeHeader marks the self-probe request ptun sends to its own public URL.
const ProbeHeader = "X-Ptun-Probe"

const badGatewayPage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Not running</title>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#222">
<h1 style="font-size:1.4rem">The app behind this link isn't running right now</h1>
<p>The person who shared it needs to start their app. Try again in a moment.</p>
</body>`

// ProxyOptions configures the local proxy.
type ProxyOptions struct {
	Target      string    // host:port of the user's app
	Log         io.Writer // request log; nil disables it
	ShowVisitor bool      // append the (unverified) visitor email from X-Pomerium-Jwt-Assertion
	Now         func() time.Time
}

// Proxy is the local reverse proxy that ssh -R points at. It logs one line per
// request, answers a friendly 502 page when the app is down, and recognises
// ptun's own readiness probe.
type Proxy struct {
	opts  ProxyOptions
	rp    *httputil.ReverseProxy
	probe string

	logMu sync.Mutex
	once  sync.Once
	ready chan struct{}
}

// NewProxy returns a Proxy forwarding to opts.Target over plain HTTP.
func NewProxy(opts ProxyOptions) *Proxy {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	p := &Proxy{opts: opts, probe: hex.EncodeToString(b), ready: make(chan struct{})}
	p.rp = &httputil.ReverseProxy{
		// Director (not Rewrite) so X-Forwarded-* and the Host header Pomerium sent are left alone.
		Director: func(r *http.Request) { //nolint:staticcheck // SA1019: see the comment above
			r.URL.Scheme = "http"
			r.URL.Host = opts.Target
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			if rec, ok := w.(*recorder); ok {
				rec.note = "→ nothing running on " + opts.Target
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, badGatewayPage)
		},
	}
	return p
}

// ProbeToken is the value the self-probe must send in ProbeHeader.
func (p *Proxy) ProbeToken() string { return p.probe }

// Ready is closed when the proxy has seen the probe or its first real request.
func (p *Proxy) Ready() <-chan struct{} { return p.ready }

func (p *Proxy) markReady() { p.once.Do(func() { close(p.ready) }) }

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get(ProbeHeader); v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(p.probe)) == 1 {
		p.markReady()
		w.WriteHeader(http.StatusNoContent) // not forwarded, not logged
		return
	}
	p.markReady()
	rec := &recorder{ResponseWriter: w, p: p, start: p.opts.Now(), method: r.Method, path: r.URL.RequestURI()}
	if p.opts.ShowVisitor {
		rec.visitor = VisitorEmail(r.Header.Get("X-Pomerium-Jwt-Assertion"))
	}
	p.rp.ServeHTTP(rec, r)
	rec.logOnce(rec.status)
}

// recorder captures the status for the log line. It unwraps so WebSocket
// upgrades (Hijack) and streaming (Flush) keep working.
type recorder struct {
	http.ResponseWriter
	p       *Proxy
	start   time.Time
	method  string
	path    string
	visitor string
	note    string
	status  int
	logged  bool
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) WriteHeader(code int) {
	if code == http.StatusSwitchingProtocols {
		// Log at upgrade time, not when the WebSocket eventually closes.
		r.logOnce(code)
	}
	if r.status == 0 && code >= 200 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Hijack is what ReverseProxy calls for a WebSocket upgrade: log it as 101 now.
func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil {
		r.status = http.StatusSwitchingProtocols
		r.logOnce(http.StatusSwitchingProtocols)
	}
	return conn, brw, err
}

func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *recorder) logOnce(status int) {
	if r.logged {
		return
	}
	r.logged = true
	if status == 0 {
		status = http.StatusOK
	}
	r.p.logLine(FormatLogLine(r.start, r.method, r.path, status, r.p.opts.Now().Sub(r.start), r.visitor, r.note))
}

func (p *Proxy) logLine(s string) {
	if p.opts.Log == nil {
		return
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	_, _ = io.WriteString(p.opts.Log, s+"\n")
}

// FormatLogLine renders `HH:MM:SS  METHOD  path  status  duration`, with
// "(websocket)" for 101 and optional visitor and note suffixes.
func FormatLogLine(t time.Time, method, path string, status int, d time.Duration, visitor, note string) string {
	st := fmt.Sprint(status)
	if status == http.StatusSwitchingProtocols {
		st += " (websocket)"
	}
	line := fmt.Sprintf("%s  %-6s  %s  %s  %s", t.Format("15:04:05"), method, path, st, d.Round(time.Millisecond))
	if visitor != "" {
		line += "  " + visitor
	}
	if note != "" {
		line += "  " + note
	}
	return line
}

// VisitorEmail returns the email claim of a Pomerium JWT assertion WITHOUT
// verifying it. It is for display only and must never be used for decisions.
func VisitorEmail(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.Email
}
