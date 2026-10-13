// SPDX-License-Identifier: 0BSD

package core

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

const (
	ansiReset   = "\x1b[0m"
	ansiBold    = "\x1b[1m"
	ansiDim     = "\x1b[2m"
	ansiRed     = "\x1b[31m"
	ansiGreen   = "\x1b[32m"
	ansiYellow  = "\x1b[33m"
	ansiBlue    = "\x1b[34m"
	ansiMagenta = "\x1b[35m"
	ansiCyan    = "\x1b[36m"
)

// Term writes log lines and a live status block to the terminal.
// Status lines sit at the bottom of the screen and are redrawn in
// place on TTYs. On plain pipes only log lines are emitted.
type Term struct {
	mu      sync.Mutex
	out     io.Writer
	file    *os.File
	color   bool
	tty     bool
	quiet   bool
	verbose bool
	status  []string
	lastRep time.Time
}

func NewTerm(out *os.File, mode string, quiet, verbose bool) *Term {
	t := &Term{out: out, file: out, quiet: quiet, verbose: verbose}
	if st, err := out.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
		t.tty = true
	}
	switch mode {
	case "always":
		t.color = true
	case "never":
		t.color = false
	default:
		t.color = t.tty && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && enableANSI(out)
	}
	return t
}

func (t *Term) paint(c, s string) string {
	if !t.color {
		return s
	}
	return c + s + ansiReset
}

// Reserve allocates n status lines at the bottom of the screen.
func (t *Term) Reserve(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.tty || n <= 0 {
		return
	}
	t.status = make([]string, n)
	for range n {
		fmt.Fprintln(t.out)
	}
}

// Set updates the text of status line i.
func (t *Term) Set(i int, s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i < 0 || i >= len(t.status) {
		return
	}
	t.status[i] = s
	if !t.tty {
		if time.Since(t.lastRep) > 2*time.Second && s != "" {
			t.lastRep = time.Now()
			fmt.Fprintln(t.out, s)
		}
		return
	}
	up := len(t.status) - i
	fmt.Fprintf(t.out, "\x1b[%dA\r\x1b[2K%s\x1b[%dB\r", up, s, up)
}

// Release clears the status block.
func (t *Term) Release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.tty || len(t.status) == 0 {
		t.status = nil
		return
	}
	n := len(t.status)
	fmt.Fprintf(t.out, "\x1b[%dA", n)
	for range n {
		fmt.Fprint(t.out, "\x1b[2K")
		fmt.Fprintln(t.out)
	}
	fmt.Fprintf(t.out, "\x1b[%dA", n)
	t.status = nil
}

// logf prints a message above the status block so it scrolls
// into history while progress lines stay pinned at the bottom.
func (t *Term) logf(prefix, format string, a ...any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	msg := fmt.Sprintf(format, a...)
	if t.tty && len(t.status) > 0 {
		n := len(t.status)
		fmt.Fprintf(t.out, "\x1b[%dA", n)
		fmt.Fprintf(t.out, "\r\x1b[2K%s %s\n", prefix, msg)
		for _, s := range t.status {
			fmt.Fprintf(t.out, "\x1b[2K%s\n", s)
		}
		fmt.Fprint(t.out, "\r")
		return
	}
	fmt.Fprintf(t.out, "%s %s\n", prefix, msg)
}

func (t *Term) Infof(format string, a ...any) {
	if t.quiet {
		return
	}
	t.logf(t.paint(ansiCyan, "*"), format, a...)
}

func (t *Term) Okf(format string, a ...any) {
	if t.quiet {
		return
	}
	t.logf(t.paint(ansiGreen, "+"), format, a...)
}

func (t *Term) Warnf(format string, a ...any) {
	t.logf(t.paint(ansiYellow, "!"), format, a...)
}

func (t *Term) Errf(format string, a ...any) {
	t.logf(t.paint(ansiRed, "x"), format, a...)
}

func (t *Term) Debugf(format string, a ...any) {
	if !t.verbose {
		return
	}
	t.logf(t.paint(ansiDim, "."), format, a...)
}

// Bold returns s emphasized when colors are enabled.
func (t *Term) Bold(s string) string  { return t.paint(ansiBold, s) }
func (t *Term) Dim(s string) string   { return t.paint(ansiDim, s) }
func (t *Term) Green(s string) string { return t.paint(ansiGreen, s) }

// TTY reports whether the output is an interactive terminal.
func (t *Term) TTY() bool { return t.tty }

// HumanBytes renders n bytes as a compact string like 26.4MB.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}

func HumanRate(bytesPerSec float64) string {
	return HumanBytes(int64(bytesPerSec)) + "/s"
}

// HumanCount renders a download count like 243.6M.
func HumanCount(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	for _, u := range []struct {
		div int64
		suf string
	}{{1_000_000_000, "B"}, {1_000_000, "M"}, {1_000, "K"}} {
		if n >= u.div {
			v := float64(n) / float64(u.div)
			if v < 10 {
				return fmt.Sprintf("%.1f%s", v, u.suf)
			}
			return fmt.Sprintf("%.0f%s", v, u.suf)
		}
	}
	return fmt.Sprintf("%d", n)
}

func HumanDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// ProgressLine composes a single status line for a download in flight.
func (t *Term) ProgressLine(title string, frac float64, done int64, rate float64, total int64) string {
	line := fmt.Sprintf("%s %-22.22s %s %5.1f%% %s %s",
		t.paint(ansiBlue, ">>"),
		title,
		RenderBar(14, frac),
		frac*100,
		HumanBytes(done),
		HumanRate(rate))
	if total > 0 {
		line += " of " + HumanBytes(total)
	}
	return line
}

// RenderBar draws an ASCII progress bar like [====>     ] 42%
func RenderBar(width int, frac float64) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fill := int(frac * float64(width))
	bar := make([]byte, 0, width)
	for i := range width {
		switch {
		case i < fill:
			bar = append(bar, '=')
		case i == fill && frac < 1:
			bar = append(bar, '>')
		default:
			bar = append(bar, ' ')
		}
	}
	return "[" + string(bar) + "]"
}
