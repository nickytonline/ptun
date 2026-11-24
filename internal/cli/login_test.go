package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeBroker emulates Pomerium's programmatic login plus the broker API. No
// real browser or network host is involved.
func fakeBroker(t *testing.T, validJWT string, logins *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.pomerium/api/v1/login", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		redirect := r.URL.Query().Get("pomerium_redirect_uri")
		// The "sign-in URL" points back at this server; opening it redirects to the loopback listener.
		_, _ = io.WriteString(w, "http://"+r.Host+"/signin?r="+url.QueryEscape(redirect))
	})
	mux.HandleFunc("/signin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Query().Get("r")+"?pomerium_jwt="+validJWT, http.StatusFound)
	})
	mux.HandleFunc("/api/tunnels", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Pomerium "+validJWT {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"abc123","url":"https://t-abc123.x.dev","hostname":"t-abc123.x.dev","ssh_host":"ssh.x.dev","ssh_port":2200,"heartbeat_interval_seconds":120}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestLoginAndReauth(t *testing.T) {
	t.Parallel()
	var logins atomic.Int32
	srv := fakeBroker(t, "good-jwt", &logins)
	host := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()
	if err := saveToken(dir, host, "stale-jwt"); err != nil {
		t.Fatal(err)
	}

	c := NewClient(host, dir, io.Discard)
	c.Login = LoginOptions{Scheme: "http", Open: func(u string) {
		// Stub opener: follow the sign-in URL ourselves instead of launching a browser.
		go func() {
			res, err := http.Get(u)
			if err == nil {
				res.Body.Close()
			}
		}()
	}}
	tn, err := c.Create(context.Background(), "localhost:8081", true)
	if err != nil {
		t.Fatal(err)
	}
	if tn.ID != "abc123" || tn.SSHPort != 2200 {
		t.Fatalf("%+v", tn)
	}
	if logins.Load() != 1 {
		t.Fatalf("logins = %d", logins.Load())
	}
	if got := loadToken(dir, host); got != "good-jwt" {
		t.Fatalf("cached token %q", got)
	}
}

func TestCreateSendsPublicFlag(t *testing.T) {
	t.Parallel()
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.Store(string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"abc123","public":true}`)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	dir := t.TempDir()
	if err := saveToken(dir, host, "jwt"); err != nil {
		t.Fatal(err)
	}
	c := NewClient(host, dir, io.Discard)
	c.Login.Scheme = "http"
	tn, err := c.Create(context.Background(), "localhost:3000", true)
	if err != nil || !tn.Public {
		t.Fatalf("%+v %v", tn, err)
	}
	if s, _ := got.Load().(string); !strings.Contains(s, `"public":true`) || !strings.Contains(s, `"target":"localhost:3000"`) {
		t.Fatalf("body %q", s)
	}
}
