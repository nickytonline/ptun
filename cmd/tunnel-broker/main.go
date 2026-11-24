// Command tunnel-broker is the headless API behind ptun. It creates and deletes
// short-lived Pomerium routes (names starting with "ephemeral:") through Pomerium
// Core's config API. See tools/ptun/README.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nickytonline/homelab-k8s/tools/ptun/internal/broker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// staticAuth is the DEV_AUTH_EMAIL authenticator (loopback-only local testing).
type staticAuth string

// Identify implements broker.Authenticator. The subject is derived from the email,
// so --stable URLs differ from the deployed broker's (no real IdP sub here).
func (s staticAuth) Identify(*http.Request) (broker.Caller, error) {
	return broker.Caller{Email: string(s), Subject: "dev:" + string(s)}, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envDur(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func run(log *slog.Logger) error {
	secret := os.Getenv("SHARED_SECRET")
	if secret == "" {
		return errors.New("SHARED_SECRET is required")
	}
	var err error
	cfg := broker.Config{
		AllowPublic: env("ALLOW_PUBLIC_TUNNELS", "false") == "true",
		AllowMCP:    env("ALLOW_MCP_TUNNELS", "true") == "true",
		RoutePrefix: env("ROUTE_PREFIX", broker.DefaultRoutePrefix),
		BaseDomain:  env("BASE_DOMAIN", "maisonlab.dev"),
		SSHHost:     env("SSH_HOST", "ssh.maisonlab.dev"),
	}
	if cfg.SSHPort, err = envInt("SSH_PORT", 2200); err != nil {
		return err
	}
	if cfg.MaxPerUser, err = envInt("MAX_TUNNELS_PER_USER", 5); err != nil {
		return err
	}
	if cfg.TTL, err = envDur("TUNNEL_TTL", 10*time.Minute); err != nil {
		return err
	}
	if cfg.HeartbeatInterval, err = envDur("HEARTBEAT_INTERVAL", 2*time.Minute); err != nil {
		return err
	}
	urls := strings.Split(env("CONFIG_API_URLS",
		"https://pomerium-0.pomerium-headless:5443,https://pomerium-1.pomerium-headless:5443,https://pomerium-2.pomerium-headless:5443"), ",")
	insecure := env("CONFIG_API_TLS_VERIFY", "false") != "true"

	brokerHost := env("BROKER_HOST", "tunnels.maisonlab.dev")
	var auth broker.Authenticator
	if devEmail := os.Getenv("DEV_AUTH_EMAIL"); devEmail != "" {
		// Local testing only: no Pomerium in front, so no JWT. Refuse unless the
		// listener is loopback, so this can never be reachable from a network.
		host, _, err := net.SplitHostPort(env("LISTEN_ADDR", ":8080"))
		if ip := net.ParseIP(host); err != nil || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("DEV_AUTH_EMAIL requires LISTEN_ADDR on loopback, e.g. 127.0.0.1:8080")
		}
		log.Warn("DEV_AUTH_EMAIL set: JWT verification is OFF, every request is " + devEmail)
		auth = staticAuth(devEmail)
	} else {
		jwtAuth, err := broker.NewJWTAuthenticator(
			env("JWKS_URL", "https://"+brokerHost+"/.well-known/pomerium/jwks.json"),
			env("JWT_AUDIENCE", brokerHost))
		if err != nil {
			return err
		}
		auth = jwtAuth
	}

	if err := broker.ValidatePrefix(cfg.RoutePrefix); err != nil {
		return fmt.Errorf("ROUTE_PREFIX: %w", err)
	}
	routes := broker.NewRoutesWithPrefix(broker.NewConfigClients(urls, secret, insecure), cfg.RoutePrefix)
	m := broker.NewManager(cfg, routes, log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Adopt existing ephemeral: routes. Retry in the background: the leader may not be elected yet.
	go func() {
		for {
			actx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := m.Adopt(actx)
			cancel()
			if err == nil {
				return
			}
			log.Warn("adopt failed, retrying", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(15 * time.Second):
			}
		}
	}()
	go m.Run(ctx, time.Minute)

	srv := &http.Server{
		Addr:              env("LISTEN_ADDR", ":8080"),
		Handler:           broker.NewAPI(m, auth, log),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("tunnel-broker listening", "addr", srv.Addr, "base_domain", cfg.BaseDomain, "ttl", cfg.TTL.String())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}
