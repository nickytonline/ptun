package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// pomeranian is the Air Pom head (github.com/nickytonline/air-pom), mirrored
// to face left, over "PTUN" in the Air Pom logo font on a dark plate so the
// title looks the same in light and dark themes. It is 24-bit color
// half-block art, 30 columns by 17 lines.
//
//go:embed pomeranian.ans
var pomeranian string

// printBanner writes the Pomeranian to w, but only when a person is likely
// watching: w is a terminal with 24-bit color, color is allowed, and the user
// has not opted out.
func printBanner(w io.Writer, noBanner bool) {
	if !shouldShowBanner(w, noBanner, os.Getenv) {
		return
	}
	// A blank line above and below keeps the art clear of the prompt and the status lines.
	fmt.Fprint(w, "\n", pomeranian, "\n")
}

func shouldShowBanner(w io.Writer, noBanner bool, getenv func(string) string) bool {
	if noBanner || envSet(getenv("PTUN_NO_BANNER")) || getenv("NO_COLOR") != "" || envSet(getenv("CI")) {
		return false
	}
	term := getenv("TERM")
	if term == "dumb" {
		return false
	}
	// The art is drawn only in 24-bit color. 256-color codes are not a fixed
	// palette: some terminals rebuild colors 16-255 from the theme, which
	// inverts the art in light themes, so without 24-bit color it is skipped.
	if !truecolor(getenv) {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// envSet reports whether an opt-out variable is turned on. Empty, "0" and
// "false" count as off, so a shell that exports CI=false still gets the banner.
func envSet(v string) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	return v != "" && v != "0" && v != "false"
}

// truecolor reports whether the terminal supports 24-bit color. COLORTERM is
// the standard signal, but some terminals that support it don't set it (for
// example over SSH, where COLORTERM is usually not passed through), so
// terminals known to support it are recognized by TERM and TERM_PROGRAM too.
func truecolor(getenv func(string) string) bool {
	switch strings.ToLower(getenv("COLORTERM")) {
	case "truecolor", "24bit":
		return true
	}
	switch term := getenv("TERM"); {
	case term == "xterm-kitty", term == "xterm-ghostty", term == "wezterm", strings.HasSuffix(term, "-direct"):
		return true
	}
	switch getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "ghostty", "vscode", "WarpTerminal":
		return true
	}
	return false
}
