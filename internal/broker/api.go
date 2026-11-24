package broker

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	sdk "github.com/pomerium/sdk-go"
)

// Authenticator returns the caller's email from a request.
type Authenticator interface {
	Email(r *http.Request) (string, error)
}

// JWTAuthenticator verifies X-Pomerium-Jwt-Assertion against Pomerium's JWKS.
type JWTAuthenticator struct{ v *sdk.Verifier }

// NewJWTAuthenticator builds an authenticator. jwksURL is required (no
// trust-on-first-use from the token's issuer claim). audience, if set, must
// match the token's aud claim (the broker route host).
func NewJWTAuthenticator(jwksURL, audience string) (*JWTAuthenticator, error) {
	opts := &sdk.Options{JWKSEndpoint: jwksURL}
	if audience != "" {
		opts.Expected = expected(audience)
	}
	v, err := sdk.New(opts)
	if err != nil {
		return nil, err
	}
	return &JWTAuthenticator{v: v}, nil
}

// Email implements Authenticator.
func (a *JWTAuthenticator) Email(r *http.Request) (string, error) {
	raw := r.Header.Get("X-Pomerium-Jwt-Assertion")
	if raw == "" {
		return "", errors.New("missing assertion")
	}
	id, err := a.v.GetIdentity(r.Context(), raw)
	if err != nil {
		return "", err
	}
	if id.Email == "" {
		return "", errors.New("assertion has no email claim")
	}
	return id.Email, nil
}

// API is the HTTP handler.
type API struct {
	m    *Manager
	auth Authenticator
	log  *slog.Logger
}

// NewAPI returns the HTTP API.
func NewAPI(m *Manager, auth Authenticator, log *slog.Logger) http.Handler {
	a := &API{m: m, auth: auth, log: log}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /api/tunnels", a.authed(a.create))
	mux.HandleFunc("POST /api/tunnels/{id}/heartbeat", a.authed(a.heartbeat))
	mux.HandleFunc("DELETE /api/tunnels/{id}", a.authed(a.delete))
	return mux
}

type handler func(w http.ResponseWriter, r *http.Request, email string)

func (a *API) authed(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email, err := a.auth.Email(r)
		if err != nil {
			a.log.Warn("auth rejected", "path", r.URL.Path, "err", err)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		h(w, r, email)
	}
}

func (a *API) create(w http.ResponseWriter, r *http.Request, email string) {
	var req struct {
		Target  string `json:"target"`
		Public  bool   `json:"public"`
		MCP     bool   `json:"mcp"`
		MCPPath string `json:"mcp_path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	t, err := a.m.CreateWith(r.Context(), email, CreateRequest{Target: req.Target, Public: req.Public, MCP: req.MCP, MCPPath: req.MCPPath})
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (a *API) heartbeat(w http.ResponseWriter, r *http.Request, email string) {
	t, err := a.m.Heartbeat(r.PathValue("id"), email)
	if err != nil {
		a.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request, email string) {
	if err := a.m.Delete(r.Context(), r.PathValue("id"), email); err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) fail(w http.ResponseWriter, err error) {
	code, msg := http.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, ErrNotFound):
		code, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrForbidden):
		code, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, ErrPublicDisabled), errors.Is(err, ErrMCPDisabled):
		code, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, ErrLimit):
		code, msg = http.StatusTooManyRequests, err.Error()
	case errors.Is(err, ErrInvalidInput):
		code, msg = http.StatusBadRequest, err.Error()
	default:
		a.log.Error("request failed", "err", err)
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
