package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Login performs Pomerium's programmatic login against the broker host and
// returns the session JWT.
//
// Flow (pomerium proxy/handlers.go ProgrammaticLogin): GET
// /.pomerium/api/v1/login?pomerium_redirect_uri=<loopback url> returns a
// sign-in URL as text/plain. After sign-in Pomerium redirects the browser to
// the loopback URL with ?pomerium_jwt=<jwt>. Only loopback redirects are
// allowed by default (programmatic_redirect_domain_whitelist: localhost).
func Login(ctx context.Context, broker string, out io.Writer, opts LoginOptions) (string, error) {
	scheme, open := opts.Scheme, opts.Open
	if scheme == "" {
		scheme = "https"
	}
	if open == nil {
		open = OpenBrowser
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://%s/", ln.Addr().String())

	hc := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	u := scheme + "://" + broker + "/.pomerium/api/v1/login?" + url.Values{"pomerium_redirect_uri": {redirect}}.Encode()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	res, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("request login url: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	res.Body.Close()
	signIn := strings.TrimSpace(string(body))
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(signIn, "http") {
		return "", fmt.Errorf("login endpoint returned %s", res.Status)
	}

	jwtc := make(chan string, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j := r.URL.Query().Get("pomerium_jwt")
		if j == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ptun: signed in. You can close this tab.\n")
		select {
		case jwtc <- j:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	fmt.Fprintf(out, "Opening your browser to sign in. If it does not open, visit:\n  %s\n", signIn)
	open(signIn)

	select {
	case j := <-jwtc:
		return j, nil
	case <-time.After(5 * time.Minute):
		return "", errors.New("timed out waiting for sign-in")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// LoginOptions customizes Login; the zero value is the production behaviour.
type LoginOptions struct {
	Scheme string       // "https" (default); tests use "http"
	Open   func(string) // opens the sign-in URL; defaults to OpenBrowser
}

// OpenBrowser opens u in the default browser. Set PTUN_NO_BROWSER=1 to only
// print the URL.
func OpenBrowser(u string) {
	if os.Getenv("PTUN_NO_BROWSER") != "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	default:
		return
	}
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}
