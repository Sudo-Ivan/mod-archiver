// SPDX-License-Identifier: 0BSD

package core

import (
	"os"
	"path/filepath"
	"testing"
)

// Vectors verified against the reference xxh64sum tool.
func TestXXH64Vectors(t *testing.T) {
	cases := []struct {
		in   string
		want uint64
	}{
		{"", 0xef46db3751d8e999},
		{"a", 0xd24ec4f1a98c6e5b},
		{"abc", 0x44bc2cf5ad770999},
		{"hello", 0x26c7827d889f6da3},
		{"1234567", 0xd3a46e9108289359},
		{"12345678", 0xd2d02f08cf7cfd4a},
		{"123456789", 0x8cb841db40e6ae83},
		{"abcdefghijklmnopqrstuvwxyz0123456789", 0x64f23ecf1609b766},
		{"abcdefghijklmnopqrstuvwxyz0123456789abcd", 0x2774d99411dd5d1c},
	}
	for _, c := range cases {
		if got := xxh64([]byte(c.in), 0); got != c.want {
			t.Errorf("xxh64(%q) = %016x, want %016x", c.in, got, c.want)
		}
	}
}

func TestFastHashSmall(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.bin")
	data := []byte("some small file contents")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	h, size, err := FastHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(data)) {
		t.Fatalf("size %d, want %d", size, len(data))
	}
	if h == "" || len(h) < 8 {
		t.Fatalf("bad hash %q", h)
	}

	// same content, different name: same fast hash
	p2 := filepath.Join(dir, "b.bin")
	os.WriteFile(p2, data, 0o644)
	h2, _, _ := FastHash(p2)
	if h != h2 {
		t.Fatalf("same content hashed differently: %s vs %s", h, h2)
	}

	// changed content changes the hash
	os.WriteFile(p2, append(data, 'x'), 0o644)
	h3, _, _ := FastHash(p2)
	if h == h3 {
		t.Fatal("hash did not change after modification")
	}
}

func TestFastHashSampled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.bin")
	// larger than 3 * sampleSpan so sampling engages
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	for i := 0; i < 13; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()

	h1, s1, err := FastHash(p)
	if err != nil {
		t.Fatal(err)
	}
	h2, s2, err := FastHash(p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 || s1 != s2 {
		t.Fatal("fast hash not deterministic")
	}
}
