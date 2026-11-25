package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPublicFlagRegistered(t *testing.T) {
	t.Parallel()
	root, _ := newRootCmd()
	f := root.Flags().Lookup("public")
	if f == nil || f.DefValue != "false" {
		t.Fatal("--public flag missing or not default false")
	}
	if err := root.ParseFlags([]string{"--public"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := root.Flags().GetBool("public"); !v {
		t.Fatal("--public not parsed")
	}
}

func TestStartAliasAndFlags(t *testing.T) {
	t.Parallel()
	root, _ := newRootCmd()
	start, _, err := root.Find([]string{"start", "3000"})
	if err != nil || start.Name() != "start" {
		t.Fatalf("start subcommand missing: %v", err)
	}
	for _, name := range []string{"public", "quiet", "show-visitor", "no-check", "mcp", "mcp-path", "stable", "name"} {
		if root.Flags().Lookup(name) == nil || start.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s missing on root or start", name)
		}
	}
	long := strings.ToLower(root.Long)
	for _, bad := range []string{"broker", "ssh ", "pomerium"} {
		if strings.Contains(long, bad) && bad != "ssh " {
			t.Errorf("root help mentions %q", bad)
		}
	}
	if !strings.HasPrefix(root.Long, "ptun shares an app running on your computer at a web address.") {
		t.Error("root help should lead with the one-line explanation")
	}
}

func TestVersionFlag(t *testing.T) {
	t.Parallel()
	root, exitCode := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "ptun version "+buildVersion()+"\n"; got != want {
		t.Fatalf("--version printed %q, want %q", got, want)
	}
	if *exitCode != 0 {
		t.Fatalf("--version exit code %d", *exitCode)
	}
}

// Not parallel: it sets the package-level version.
func TestBuildVersionDropsV(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	for _, v := range []string{"1.0.0", "v1.0.0"} {
		version = v
		if got := buildVersion(); got != "1.0.0" {
			t.Errorf("version %q: buildVersion() = %q, want 1.0.0", v, got)
		}
	}
}
