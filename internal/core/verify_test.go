// SPDX-License-Identifier: 0BSD

package core

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyManifest(t *testing.T) {
	dir := t.TempDir()
	m := NewManifest(dir)
	m.Version = 2

	data := []byte("hello archive")
	good := filepath.Join(dir, "files", "good.bin")
	writeFileT(t, good, data)
	bad := filepath.Join(dir, "files", "bad.bin")
	writeFileT(t, bad, []byte("tampered"))
	fh, _, _ := FastHash(good)
	fhBad, _, _ := FastHash(bad)

	m.Items = []*ItemEntry{
		{ID: "g", Section: "files", Status: StDone, Path: "files/good.bin",
			Size: int64(len(data)), FastHash: fh},
		{ID: "b", Section: "files", Status: StDone, Path: "files/bad.bin",
			Size: 8, FastHash: fhBad},
		{ID: "gone", Section: "files", Status: StDone, Path: "files/gone.bin"},
		{ID: "pend", Section: "files", Status: StPending},
	}
	// corrupt bad.bin after hashing so fasthash mismatches
	f, _ := os.OpenFile(bad, os.O_WRONLY, 0o600)
	f.WriteAt([]byte{'X'}, 0)
	f.Close()
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}

	rep, err := VerifyManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != 1 || len(rep.Bad) != 1 || len(rep.Missing) != 1 {
		t.Fatalf("want 1 ok 1 bad 1 missing, got %+v", rep)
	}
}

func TestVerifyManifestRemoteHash(t *testing.T) {
	dir := t.TempDir()
	m := NewManifest(dir)
	m.Version = 2
	data := []byte("remote hashed")
	writeFileT(t, filepath.Join(dir, "files", "h.bin"), data)
	sum := fmt.Sprintf("%x", md5.Sum(data))
	fh, _, _ := FastHash(filepath.Join(dir, "files", "h.bin"))
	m.Items = []*ItemEntry{
		{ID: "h", Section: "files", Status: StDone, Path: "files/h.bin",
			Size: int64(len(data)), FastHash: fh, Hashes: map[string]string{"md5": sum}},
	}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	rep, err := VerifyManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK != 1 || len(rep.Bad) != 0 {
		t.Fatalf("remote hash verify failed: %+v", rep)
	}
}

func TestArchiveDirs(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "proj", manifestName), []byte(`{"version":2}`))
	dirs, err := ArchiveDirs(root)
	if err != nil || len(dirs) != 1 {
		t.Fatalf("want 1 archive dir, got %v err %v", dirs, err)
	}
}

func writeFileT(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
