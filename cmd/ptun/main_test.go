package main

import (
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
