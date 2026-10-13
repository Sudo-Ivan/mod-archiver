// SPDX-License-Identifier: 0BSD

package core

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		`a/b\c:d*e?f"g<h>i|j`: "a_b_c_d_e_f_g_h_i_j",
		"  spaced out  ":      "spaced out",
		"...dots...":          "dots",
		"CON":                 "_CON",
		"con.zip":             "_con.zip",
		"normal file.rar":     "normal file.rar",
		"":                    "unnamed",
		"file.txt":            "file.txt",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSanitizeNameLength(t *testing.T) {
	long := strings.Repeat("x", 500) + ".rar"
	got := SanitizeName(long)
	if len(got) > 180 {
		t.Fatalf("name too long: %d", len(got))
	}
	if !strings.HasSuffix(got, ".rar") {
		t.Fatalf("extension lost: %q", got)
	}
}

func TestUniqueName(t *testing.T) {
	seen := map[string]int{}
	a := UniqueName("f.rar", seen)
	b := UniqueName("f.rar", seen)
	c := UniqueName("f.rar", seen)
	if a != "f.rar" || b != "f-2.rar" || c != "f-3.rar" {
		t.Fatalf("got %q %q %q", a, b, c)
	}
}
