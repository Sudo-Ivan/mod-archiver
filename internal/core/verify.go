// SPDX-License-Identifier: 0BSD

package core

import (
	"fmt"
	"os"
	"path/filepath"
)

// VerifyReport is the outcome of re-checking a manifest on disk.
type VerifyReport struct {
	Checked int
	OK      int
	Bad     []string // paths that failed verification
	Missing []string // expected files absent from disk
}

// VerifyManifest recomputes digests for every manifest item that
// points at a file on disk. Site-published hashes take precedence,
// the sampled fast hash covers the rest.
func VerifyManifest(dir string) (*VerifyReport, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	rep := &VerifyReport{}
	for _, e := range m.Items {
		if e.Path == "" || e.Status != StDone {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(e.Path))
		st, err := os.Stat(full)
		if err != nil {
			rep.Checked++
			rep.Missing = append(rep.Missing, e.Path)
			continue
		}
		if e.Size > 0 && st.Size() != e.Size {
			rep.Checked++
			rep.Bad = append(rep.Bad, e.Path+": size")
			continue
		}
		rep.Checked++
		if len(e.Hashes) > 0 {
			if ok, err := verifyHashes(full, e.Hashes); err != nil || !ok {
				rep.Bad = append(rep.Bad, e.Path+": hash")
				continue
			}
			rep.OK++
			continue
		}
		if e.FastHash != "" {
			fh, _, err := FastHash(full)
			if err != nil || fh != e.FastHash {
				rep.Bad = append(rep.Bad, e.Path+": fasthash")
				continue
			}
		}
		rep.OK++
	}
	return rep, nil
}

// ArchiveDirs lists manifest-bearing directories under root.
func ArchiveDirs(root string) ([]string, error) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		d := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(d, manifestName)); err == nil {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no archives under %s", root)
	}
	return out, nil
}
