package main

import (
	_ "embed"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
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
// watching: w is a terminal, color is allowed, and the user has not opted out.
func printBanner(w io.Writer, noBanner bool) {
	if !shouldShowBanner(w, noBanner, os.Getenv) {
		return
	}
	art := pomeranian
	if !truecolor(os.Getenv) {
		art = to256(art)
	}
	// A blank line above and below keeps the art clear of the prompt and the status lines.
	fmt.Fprint(w, "\n", art, "\n")
}

func shouldShowBanner(w io.Writer, noBanner bool, getenv func(string) string) bool {
	if noBanner || envSet(getenv("PTUN_NO_BANNER")) || getenv("NO_COLOR") != "" || envSet(getenv("CI")) {
		return false
	}
	term := getenv("TERM")
	if term == "dumb" {
		return false
	}
	// The art needs at least 256 colors. Terminals with only 8 or 16 (the Linux
	// console, vt100 and the like) would show it in the wrong colors.
	if !truecolor(getenv) && !strings.Contains(term, "256color") {
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

var rgbSGR = regexp.MustCompile(`\x1b\[([34])8;2;(\d+);(\d+);(\d+)m`)

// to256 rewrites 24-bit color codes to the nearest xterm 256-color entry,
// for terminals without truecolor support.
func to256(s string) string {
	return rgbSGR.ReplaceAllStringFunc(s, func(m string) string {
		p := rgbSGR.FindStringSubmatch(m)
		r, _ := strconv.Atoi(p[2])
		g, _ := strconv.Atoi(p[3])
		b, _ := strconv.Atoi(p[4])
		return fmt.Sprintf("\x1b[%s8;5;%dm", p[1], xterm256(r, g, b))
	})
}

// xterm256Lab holds the CIELAB values of xterm colors 16-255: the 6x6x6 color
// cube and the 24-step gray ramp. Colors 0-15 are left out because terminal
// themes change them.
var xterm256Lab = func() (t [240][3]float64) {
	levels := [6]int{0, 95, 135, 175, 215, 255}
	for i := range 216 {
		t[i] = toLab(levels[i/36], levels[i/6%6], levels[i%6])
	}
	for i := range 24 {
		v := 8 + 10*i
		t[216+i] = toLab(v, v, v)
	}
	return t
}()

// xterm256 returns the xterm 256-color index that looks closest to r, g, b.
// It compares in CIELAB rather than RGB, because matching each RGB channel
// on its own shifts hues (a dark red-brown turns olive, for example).
func xterm256(r, g, b int) int {
	want := toLab(r, g, b)
	best, bestDist := 0, math.Inf(1)
	for i, c := range xterm256Lab {
		dl, da, db := want[0]-c[0], want[1]-c[1], want[2]-c[2]
		if d := dl*dl + da*da + db*db; d < bestDist {
			best, bestDist = i, d
		}
	}
	return 16 + best
}

// toLab converts an sRGB color to CIELAB (D65 white point).
func toLab(r, g, b int) [3]float64 {
	lin := func(v int) float64 {
		c := float64(v) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	rl, gl, bl := lin(r), lin(g), lin(b)
	x := (0.4124*rl + 0.3576*gl + 0.1805*bl) / 0.95047
	y := 0.2126*rl + 0.7152*gl + 0.0722*bl
	z := (0.0193*rl + 0.1192*gl + 0.9505*bl) / 1.08883
	f := func(t float64) float64 {
		if t > 0.008856 {
			return math.Cbrt(t)
		}
		return 7.787*t + 16.0/116
	}
	fx, fy, fz := f(x), f(y), f(z)
	return [3]float64{116*fy - 16, 500 * (fx - fy), 200 * (fy - fz)}
}
