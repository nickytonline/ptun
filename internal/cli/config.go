package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Config is the saved CLI configuration.
type Config struct {
	Broker string `json:"broker"` // broker host, e.g. tunnels.example.com
}

type tokenCache struct {
	Broker string `json:"broker"`
	JWT    string `json:"jwt"`
}

// ConfigDir returns <os.UserConfigDir>/ptun (~/Library/Application Support/ptun
// on macOS, ~/.config/ptun on Linux).
func ConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "ptun"), nil
}

func writeFile0600(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// NormalizeBroker accepts "host" or "https://host[/]" and returns the host.
func NormalizeBroker(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimSuffix(s, "/")
	if s == "" || strings.ContainsAny(s, "/ ?#@") || strings.Contains(s, "://") {
		return "", fmt.Errorf("invalid broker host %q (want e.g. tunnels.example.com)", s)
	}
	return s, nil
}

// BrokerFromInit turns what the user gives `ptun init` into the broker host.
// People naturally give the Pomerium SSH endpoint (ssh.example.com[:2200]), so
// that is accepted and mapped to tunnels.example.com. Anything else is taken
// as the broker host itself. override (--broker) wins.
func BrokerFromInit(arg, override string) (string, error) {
	if override != "" {
		return NormalizeBroker(override)
	}
	s := strings.TrimSpace(arg)
	s = strings.TrimPrefix(s, "ssh://")
	s = strings.TrimSuffix(strings.TrimPrefix(s, "https://"), "/")
	host := s
	if h, port, err := net.SplitHostPort(s); err == nil {
		host = h
		if port != "443" && !strings.HasPrefix(h, "ssh.") {
			return "", fmt.Errorf("%q looks like an SSH endpoint but its host does not start with ssh.; pass the broker with --broker <host>", arg)
		}
	}
	if rest, ok := strings.CutPrefix(host, "ssh."); ok {
		return NormalizeBroker("tunnels." + rest)
	}
	return NormalizeBroker(host)
}

// SaveConfig writes config.json in dir.
func SaveConfig(dir string, c Config) error {
	return writeFile0600(filepath.Join(dir, "config.json"), c)
}

// LoadConfig reads config.json from dir.
func LoadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return c, ErrNotConfigured
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	if c.Broker == "" {
		return c, ErrNotConfigured
	}
	return c, nil
}

func loadToken(dir, broker string) string {
	b, err := os.ReadFile(filepath.Join(dir, "token.json"))
	if err != nil {
		return ""
	}
	var t tokenCache
	if json.Unmarshal(b, &t) != nil || t.Broker != broker {
		return ""
	}
	return t.JWT
}

func saveToken(dir, broker, jwt string) error {
	return writeFile0600(filepath.Join(dir, "token.json"), tokenCache{Broker: broker, JWT: jwt})
}

func dropToken(dir string) { _ = os.Remove(filepath.Join(dir, "token.json")) }
