package main

import (
	_ "embed"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
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
	if noBanner || getenv("PTUN_NO_BANNER") != "" || getenv("NO_COLOR") != "" || getenv("CI") != "" {
		return false
	}
	if getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func truecolor(getenv func(string) string) bool {
	ct := strings.ToLower(getenv("COLORTERM"))
	return ct == "truecolor" || ct == "24bit"
}

var rgbSGR = regexp.MustCompile(`\x1b\[([34])8;2;(\d+);(\d+);(\d+)m`)

// to256 rewrites 24-bit color codes to the nearest xterm 256-color cube entry,
// for terminals that do not advertise truecolor support.
func to256(s string) string {
	return rgbSGR.ReplaceAllStringFunc(s, func(m string) string {
		p := rgbSGR.FindStringSubmatch(m)
		r, _ := strconv.Atoi(p[2])
		g, _ := strconv.Atoi(p[3])
		b, _ := strconv.Atoi(p[4])
		return fmt.Sprintf("\x1b[%s8;5;%dm", p[1], 16+36*cube(r)+6*cube(g)+cube(b))
	})
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
