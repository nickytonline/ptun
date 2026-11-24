package cli

import (
	"strings"
	"testing"
)

func TestSSHArgs(t *testing.T) {
	t.Parallel()
	got := strings.Join(SSHArgs(Tunnel{Hostname: "t-abc123.maisonlab.dev", SSHHost: "ssh.maisonlab.dev", SSHPort: 2200}, "localhost:8081"), " ")
	want := "-p 2200 -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=0 -R t-abc123.maisonlab.dev:443:localhost:8081 ssh.maisonlab.dev"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeBroker(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"tunnels.maisonlab.dev": "tunnels.maisonlab.dev", "https://t.dev/": "t.dev"} {
		if got, err := NormalizeBroker(in); err != nil || got != want {
			t.Errorf("%q -> %q %v", in, got, err)
		}
	}
	if _, err := NormalizeBroker("a/b"); err == nil {
		t.Error("want error")
	}
}

func TestConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("want error when unset")
	}
	if err := SaveConfig(dir, Config{Broker: "x.dev"}); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(dir)
	if err != nil || c.Broker != "x.dev" {
		t.Fatal(c, err)
	}
}

func TestBrokerFromInit(t *testing.T) {
	cases := []struct {
		arg, override, want string
		wantErr             bool
	}{
		{"ssh.maisonlab.dev:2200", "", "tunnels.maisonlab.dev", false},
		{"ssh.maisonlab.dev", "", "tunnels.maisonlab.dev", false},
		{"ssh://ssh.maisonlab.dev:2200", "", "tunnels.maisonlab.dev", false},
		{"tunnels.maisonlab.dev", "", "tunnels.maisonlab.dev", false},
		{"https://tunnels.maisonlab.dev/", "", "tunnels.maisonlab.dev", false},
		{"ssh.maisonlab.dev:2200", "broker.example.com", "broker.example.com", false},
		{"pomerium.example.com:2200", "", "", true},
	}
	for _, c := range cases {
		got, err := BrokerFromInit(c.arg, c.override)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("BrokerFromInit(%q,%q) = %q,%v want %q,err=%v", c.arg, c.override, got, err, c.want, c.wantErr)
		}
	}
}
