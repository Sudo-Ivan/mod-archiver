// SPDX-License-Identifier: 0BSD

package app

import (
	"path/filepath"
	"strings"
	"unicode"
)

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SanitizeName makes an arbitrary string safe as a file name on
// Windows, macOS and Linux: strips separators and control characters,
// avoids trailing dots or spaces and Windows device names, and caps
// the length while keeping the extension.
func SanitizeName(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteByte('_')
		case unicode.IsControl(r):
			// drop
		default:
			b.WriteRune(r)
		}
	}
	s = strings.Trim(b.String(), " .")
	if s == "" {
		s = "unnamed"
	}
	stem := s
	if i := strings.IndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	if windowsReserved[strings.ToUpper(stem)] {
		s = "_" + s
	}
	const maxLen = 180
	if len(s) > maxLen {
		ext := filepath.Ext(s)
		keep := maxLen - len(ext)
		if keep < 1 {
			keep = maxLen
			ext = ""
		}
		s = s[:keep] + ext
	}
	return s
}

// uniqueName returns name, or name-2.ext, name-3.ext, ... when the
// name is already taken in the seen set.
func uniqueName(name string, seen map[string]int) string {
	if seen[name] == 0 {
		seen[name] = 1
		return name
	}
	seen[name]++
	ext := filepath.Ext(name)
	stem := name[:len(name)-len(ext)]
	for {
		cand := stem + "-" + itoa(seen[name]) + ext
		if seen[cand] == 0 {
			seen[cand] = 1
			return cand
		}
		seen[name]++
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
