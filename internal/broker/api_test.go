package broker

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAuth struct{}

func (fakeAuth) Identify(r *http.Request) (Caller, error) {
	if e := r.Header.Get("X-Test-Email"); e != "" {
		return Caller{Email: e, Subject: "sub|" + e}, nil
	}
	return Caller{}, errors.New("no auth")
}

func TestAPI(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	h := NewAPI(m, fakeAuth{}, m.log)

	do := func(method, path, email, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if email != "" {
			req.Header.Set("X-Test-Email", email)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if c := do("GET", "/healthz", "", "").Code; c != 200 {
		t.Fatalf("healthz %d", c)
	}
	if c := do("POST", "/api/tunnels", "", `{"target":"localhost:1"}`).Code; c != 401 {
		t.Fatalf("unauth %d", c)
	}
	rec := do("POST", "/api/tunnels", "a@x.dev", `{"target":"localhost:1"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"email":"a@x.dev"`) {
		t.Fatalf("create %d %s", rec.Code, rec.Body)
	}
	if c := do("POST", "/api/tunnels", "a@x.dev", `{"target":"bad"}`).Code; c != 400 {
		t.Fatalf("bad target %d", c)
	}
	var id string
	for k := range m.leases {
		id = k
	}
	if c := do("POST", "/api/tunnels/"+id+"/heartbeat", "b@x.dev", "").Code; c != 403 {
		t.Fatalf("foreign heartbeat %d", c)
	}
	if rec := do("POST", "/api/tunnels/"+id+"/heartbeat", "a@x.dev", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"email":"a@x.dev"`) {
		t.Fatalf("heartbeat %d %s", rec.Code, rec.Body)
	}
	if c := do("DELETE", "/api/tunnels/"+id, "a@x.dev", "").Code; c != 204 {
		t.Fatalf("delete %d", c)
	}
	if c := do("DELETE", "/api/tunnels/"+id, "a@x.dev", "").Code; c != 404 {
		t.Fatalf("second delete %d", c)
	}
	// Stable: the hostname comes from the JWT subject, never from the request body.
	want := `"hostname":"demo-` + StableID("sub|a@x.dev", "example.com") + `.example.com"`
	rec = do("POST", "/api/tunnels", "a@x.dev", `{"target":"localhost:1","name":"demo","subject":"forged"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), `"stable":true`) {
		t.Fatalf("stable create %d %s", rec.Code, rec.Body)
	}
	if c := do("POST", "/api/tunnels", "a@x.dev", `{"target":"localhost:1","name":"no good"}`).Code; c != 400 {
		t.Fatalf("bad name %d", c)
	}
}

func TestAPIConflictIs409(t *testing.T) {
	t.Parallel()
	f := newFake()
	m, _ := newMgr(f)
	f.seed("g1", "grafana")
	f.routes["g1"].From = "https://" + StableHostname("", "sub|a@x.dev", "example.com")
	h := NewAPI(m, fakeAuth{}, m.log)
	req := httptest.NewRequest("POST", "/api/tunnels", strings.NewReader(`{"target":"localhost:1","stable":true}`))
	req.Header.Set("X-Test-Email", "a@x.dev")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "already used by another route") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
