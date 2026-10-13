// SPDX-License-Identifier: 0BSD

package core

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FastHash is deterministic and one flipped byte anywhere changes the
// fingerprint for files larger than the sample window.
func TestFastHashMetamorphic(t *testing.T) {
	dir := t.TempDir()
	rng := rand.New(rand.NewPCG(42, 99))
	orig := make([]byte, 12<<20+7777)
	for i := range orig {
		orig[i] = byte(rng.IntN(256))
	}
	p := filepath.Join(dir, "f.bin")
	os.WriteFile(p, orig, 0o600)

	h1, _, _ := FastHash(p)
	h2, _, _ := FastHash(p)
	if h1 != h2 {
		t.Fatal("same file produced different hashes")
	}

	// flipping a byte in the head, middle and tail must each change it
	for _, pos := range []int{0, len(orig) / 2, len(orig) - 1} {
		mut := append([]byte(nil), orig...)
		mut[pos] ^= 0xff
		mp := filepath.Join(dir, "mut.bin")
		os.WriteFile(mp, mut, 0o600)
		hm, _, _ := FastHash(mp)
		if hm == h1 {
			t.Fatalf("flip at %d did not change hash", pos)
		}
	}

	// same head/mid/tail but different length must differ (size is in
	// the digest)
	short := orig[:len(orig)-1]
	sp := filepath.Join(dir, "s.bin")
	os.WriteFile(sp, short, 0o600)
	hs, _, _ := FastHash(sp)
	if hs == h1 {
		t.Fatal("size-only change did not change hash")
	}
}

// SanitizeName must never emit path separators, must be idempotent,
// and must never produce an empty or traversal path.
func TestSanitizeMetamorphic(t *testing.T) {
	nasties := []string{
		"../../etc/passwd", "..\\..\\win.ini", "a/b/c", "a\\b\\c",
		"file:name*?<>|", "   ", ".", "..", "...", "CON", "nul.txt",
		"LPT1", "com5", "\x00nul-byte", "trailing dot.", "many  spaces",
		strings.Repeat("x", 500), "file\r\nname", "\ttab\t",
	}
	seen := map[string]int{}
	for _, in := range nasties {
		out := SanitizeName(in)
		if out == "" {
			t.Errorf("empty result for %q", in)
		}
		if strings.ContainsAny(out, "/\\") {
			t.Errorf("separator survived in %q -> %q", in, out)
		}
		if out == "." || out == ".." {
			t.Errorf("traversal survived %q -> %q", in, out)
		}
		if SanitizeName(out) != out {
			t.Errorf("not idempotent: %q -> %q -> %q", in, out, SanitizeName(out))
		}
		// uniqueness under collisions
		u1 := UniqueName(out, seen)
		u2 := UniqueName(out, seen)
		if u1 == u2 {
			t.Errorf("UniqueName collided for %q", out)
		}
	}
}

// Manifest round trip keeps every field and NeedsDownload obeys
// file existence.
func TestManifestMetamorphic(t *testing.T) {
	dir := t.TempDir()
	m := NewManifest(dir)
	m.Version = 2
	m.Project = &Project{Source: "x", ID: "1", Title: "t"}
	e := &ItemEntry{ID: "1", Section: "files", Filename: "a.bin", Status: StPending}
	m.Items = append(m.Items, e)
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	m2, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Project.Title != "t" || len(m2.Items) != 1 || m2.Items[0].ID != "1" {
		t.Fatal("round trip lost fields")
	}

	// item without file on disk must need download
	if !m2.NeedsDownload(m2.Items[0], dir) {
		t.Fatal("pending item without file should need download")
	}
	// write the file, mark done, must not need download
	os.MkdirAll(filepath.Join(dir, "files"), 0o750)
	os.WriteFile(filepath.Join(dir, "files", "a.bin"), []byte("x"), 0o600)
	m2.Items[0].Path = "files/a.bin"
	m2.Items[0].Status = StDone
	if m2.NeedsDownload(m2.Items[0], dir) {
		t.Fatal("done item with existing file should not need download")
	}
	// delete -> needs again
	os.Remove(filepath.Join(dir, "files", "a.bin"))
	if !m2.NeedsDownload(m2.Items[0], dir) {
		t.Fatal("done item with missing file should need download")
	}
}
