package main

import (
	_ "embed"
	"fmt"
	"io"
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

func truecolor(getenv func(string) string) bool {
	ct := strings.ToLower(getenv("COLORTERM"))
	return ct == "truecolor" || ct == "24bit"
}

var rgbSGR = regexp.MustCompile(`\x1b\[([34])8;2;(\d+);(\d+);(\d+)m`)

// to256 rewrites 24-bit color codes to the nearest xterm 256-color entry,
// for terminals that do not advertise truecolor support.
func to256(s string) string {
	return rgbSGR.ReplaceAllStringFunc(s, func(m string) string {
		p := rgbSGR.FindStringSubmatch(m)
		r, _ := strconv.Atoi(p[2])
		g, _ := strconv.Atoi(p[3])
		b, _ := strconv.Atoi(p[4])
		return fmt.Sprintf("\x1b[%s8;5;%dm", p[1], xterm256(r, g, b))
	})
}

// cubeLevels are the channel values of the xterm 6x6x6 color cube (16-231).
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// xterm256 returns the closest xterm 256-color index to r, g, b, choosing
// between the color cube and the 24-step gray ramp (232-255, 8 to 238).
func xterm256(r, g, b int) int {
	cr, cg, cb := cube(r), cube(g), cube(b)
	cubeIdx := 16 + 36*cr + 6*cg + cb
	cubeDist := dist(r, g, b, cubeLevels[cr], cubeLevels[cg], cubeLevels[cb])

	gi := ((r+g+b)/3 - 8 + 5) / 10
	gi = max(0, min(23, gi))
	gv := 8 + 10*gi
	if dist(r, g, b, gv, gv, gv) < cubeDist {
		return 232 + gi
	}
	return cubeIdx
}

func dist(r1, g1, b1, r2, g2, b2 int) int {
	dr, dg, db := r1-r2, g1-g2, b1-b2
	return dr*dr + dg*dg + db*db
}

// cube maps 0-255 to the nearest of the 6 xterm cube levels (0,95,135,175,215,255).
func cube(v int) int {
	if v < 48 {
		return 0
	}
	if v < 115 {
		return 1
	}
	return (v - 35) / 40
}
