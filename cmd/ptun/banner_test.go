package main

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
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

	color := env(map[string]string{"COLORTERM": "truecolor"})
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if shouldShowBanner(f, false, color) {
		t.Fatal("banner should not show when output is a regular file")
	}

	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if shouldShowBanner(null, false, color) {
		t.Fatal("banner should not show when output is /dev/null")
	}
}

func TestBannerOptOuts(t *testing.T) {
	t.Parallel()
	tty, err := os.Open("/dev/tty")
	if err != nil {
		t.Skip("no controlling terminal")
	}
	defer tty.Close()
	with := func(kv ...string) func(string) string {
		m := map[string]string{"COLORTERM": "truecolor"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return env(m)
	}
	if !shouldShowBanner(tty, false, with()) {
		t.Fatal("banner should show on a terminal with no opt-outs")
	}
	if shouldShowBanner(tty, true, with()) {
		t.Error("--no-banner should hide it")
	}
	for _, k := range []string{"PTUN_NO_BANNER", "NO_COLOR", "CI"} {
		if shouldShowBanner(tty, false, with(k, "1")) {
			t.Errorf("%s should hide it", k)
		}
	}
	for _, v := range []string{"0", "false", "FALSE"} {
		for _, k := range []string{"PTUN_NO_BANNER", "CI"} {
			if !shouldShowBanner(tty, false, with(k, v)) {
				t.Errorf("%s=%s should not hide it", k, v)
			}
		}
	}
	if shouldShowBanner(tty, false, with("TERM", "dumb")) {
		t.Error("TERM=dumb should hide it")
	}

	noTrue := func(term string) func(string) string { return env(map[string]string{"TERM": term}) }
	for _, term := range []string{"xterm-256color", "screen-256color", "linux", "xterm", ""} {
		if shouldShowBanner(tty, false, noTrue(term)) {
			t.Errorf("TERM=%q without 24-bit color should hide it", term)
		}
	}
}

func TestBannerArt(t *testing.T) {
	t.Parallel()
	lines := strings.Split(strings.TrimRight(pomeranian, "\n"), "\n")
	if len(lines) != 17 {
		t.Errorf("banner is %d lines, want 17", len(lines))
	}
	sgr := regexp.MustCompile(`\x1b\[[0-9;]*m`)
	for i, l := range lines {
		if w := utf8.RuneCountInString(sgr.ReplaceAllString(l, "")); w > 30 {
			t.Errorf("line %d is %d columns, want at most 30", i+1, w)
		}
		if l != "" && !strings.HasSuffix(l, "\x1b[0m") {
			t.Errorf("line %d does not reset colors", i+1)
		}
	}
}

func TestTruecolor(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"COLORTERM": "truecolor"}, true},
		{map[string]string{"COLORTERM": "24bit"}, true},
		{map[string]string{"TERM": "xterm-kitty"}, true},
		{map[string]string{"TERM": "xterm-ghostty"}, true},
		{map[string]string{"TERM": "xterm-direct"}, true},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, true},
		{map[string]string{"TERM_PROGRAM": "vscode"}, true},
		{map[string]string{"TERM": "xterm-256color"}, false},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal", "TERM": "xterm-256color"}, false},
		{nil, false},
	} {
		if got := truecolor(env(c.env)); got != c.want {
			t.Errorf("truecolor(%v) = %v, want %v", c.env, got, c.want)
		}
	}
}
