package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestBannerSkippedWhenNotATerminal(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if shouldShowBanner(&buf, false, env(nil)) {
		t.Fatal("banner should not show for a non-file writer")
	}

	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if shouldShowBanner(f, false, env(nil)) {
		t.Fatal("banner should not show when output is a regular file")
	}
}

func TestBannerOptOuts(t *testing.T) {
	t.Parallel()
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skip("no controlling terminal")
	}
	defer tty.Close()
	if !shouldShowBanner(tty, false, env(nil)) {
		t.Fatal("banner should show on a terminal with no opt-outs")
	}
	if shouldShowBanner(tty, true, env(nil)) {
		t.Error("--no-banner should hide it")
	}
	for _, k := range []string{"PTUN_NO_BANNER", "NO_COLOR", "CI"} {
		if shouldShowBanner(tty, false, env(map[string]string{k: "1"})) {
			t.Errorf("%s should hide it", k)
		}
	}
	if shouldShowBanner(tty, false, env(map[string]string{"TERM": "dumb"})) {
		t.Error("TERM=dumb should hide it")
	}
}

func TestBannerArt(t *testing.T) {
	t.Parallel()
	lines := strings.Split(strings.TrimRight(pomeranian, "\n"), "\n")
	if len(lines) != 28 {
		t.Errorf("banner is %d lines, want 28", len(lines))
	}
	for i, l := range lines {
		if l != "" && !strings.HasSuffix(l, "\x1b[0m") {
			t.Errorf("line %d does not reset colors", i+1)
		}
	}
}

func TestTo256(t *testing.T) {
	t.Parallel()
	got := to256("\x1b[38;2;255;0;0m\x1b[48;2;0;0;0m▀")
	if want := "\x1b[38;5;196m\x1b[48;5;16m▀"; got != want {
		t.Errorf("to256 = %q, want %q", got, want)
	}
	if strings.Contains(to256(pomeranian), ";2;") {
		t.Error("to256 left 24-bit codes in the banner")
	}
}
