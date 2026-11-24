// Package cli implements the ptun command line client.
package cli

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ParseTarget turns a CLI argument into a host:port target.
//
//	8081                  -> localhost:8081
//	localhost:8081        -> localhost:8081
//	http://localhost:8081 -> localhost:8081
//	https://myapp.test    -> myapp.test:443
func ParseTarget(arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return "", fmt.Errorf("empty target")
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("port %d out of range", n)
		}
		return net.JoinHostPort("localhost", arg), nil
	}
	if !strings.Contains(arg, "://") {
		arg = "http://" + arg
	}
	u, err := url.Parse(arg)
	if err != nil {
		return "", fmt.Errorf("invalid target %q: %w", arg, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q: only http and https targets", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("target %q has no host", arg)
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("invalid port %q", port)
	}
	return net.JoinHostPort(host, port), nil
}
