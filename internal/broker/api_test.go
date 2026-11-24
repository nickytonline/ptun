package broker

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeAuth struct{}

func (fakeAuth) Email(r *http.Request) (string, error) {
	if e := r.Header.Get("X-Test-Email"); e != "" {
		return e, nil
	}
	return "", errors.New("no auth")
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
}
