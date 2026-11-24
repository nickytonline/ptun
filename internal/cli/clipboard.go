package cli

import (
	"os/exec"
	"runtime"
	"strings"
)

// CopyToClipboard copies s with pbcopy (macOS), wl-copy or xclip when present.
// It reports whether a tool accepted it; callers omit the note on false.
func CopyToClipboard(s string) bool {
	var cmds [][]string
	if runtime.GOOS == "darwin" {
		cmds = [][]string{{"pbcopy"}}
	} else {
		cmds = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}}
	}
	for _, c := range cmds {
		path, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, c[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if cmd.Run() == nil {
			return true
		}
	}
	return false
}
