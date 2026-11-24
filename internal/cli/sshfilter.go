package cli

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
)

var interesting = regexp.MustCompile(`(?i)https?://|error|denied|failed|refused|rejected|warning|could not|cannot|unable|closed by|timed out|offending|host key|sign in|authenticat`)

// sshFilter forwards only the lines of ssh's stderr a person needs to see: the
// Pomerium sign-in link (any line with a URL) and errors. Everything else is
// dropped, but all lines are kept (bounded) so the caller can inspect them,
// e.g. for "Permission denied".
//
// A line without a trailing newline (a prompt) that already looks interesting
// is forwarded immediately and the rest of that line is passed through
// unconditionally, so a prompt is never held back waiting for a newline.
type sshFilter struct {
	out io.Writer

	mu      sync.Mutex
	partial []byte
	passing bool // current partial line was already forwarded
	all     bytes.Buffer
}

func newSSHFilter(out io.Writer) *sshFilter { return &sshFilter{out: out} }

func (f *sshFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(p)
	if f.all.Len() < 64<<10 {
		f.all.Write(p)
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			f.partial = append(f.partial, p...)
			if !f.passing && interesting.Match(f.partial) {
				_, _ = f.out.Write(f.partial)
				f.passing = true
			} else if f.passing {
				_, _ = f.out.Write(p)
			}
			break
		}
		chunk := p[:i+1]
		line := append(f.partial, chunk...)
		switch {
		case f.passing:
			_, _ = f.out.Write(chunk)
		case interesting.Match(line):
			_, _ = f.out.Write(line)
		}
		f.partial, f.passing = nil, false
		p = p[i+1:]
	}
	return n, nil
}

// Output returns everything ssh wrote to stderr so far.
func (f *sshFilter) Output() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.all.String()
}

// PermissionDenied reports whether ssh was refused.
func (f *sshFilter) PermissionDenied() bool {
	return strings.Contains(strings.ToLower(f.Output()), "permission denied")
}
