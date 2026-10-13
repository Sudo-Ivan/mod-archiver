// SPDX-License-Identifier: 0BSD

package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const manifestName = "manifest.json"

const (
	StPending = "pending"
	StDone    = "done"
	StFailed  = "failed"
)

// Project is the archived project metadata.
type Project struct {
	Source      string            `json:"source"`
	ID          string            `json:"id"`
	Slug        string            `json:"slug"`
	Input       string            `json:"input,omitempty"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Author      string            `json:"author"`
	Game        string            `json:"game"`
	GameURL     string            `json:"game_url,omitempty"`
	URL         string            `json:"url"`
	HeaderImage string            `json:"header_image,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
}

// ItemEntry is one downloadable artifact in the manifest.
type ItemEntry struct {
	ID        string            `json:"id"`
	Section   string            `json:"section"` // files, images, videos
	Title     string            `json:"title"`
	Filename  string            `json:"filename"`
	Path      string            `json:"path"`
	SourceURL string            `json:"source_url"` // page it came from
	Category  string            `json:"category,omitempty"`
	Uploader  string            `json:"uploader,omitempty"`
	Added     string            `json:"added,omitempty"`
	Summary   string            `json:"summary,omitempty"`
	Size      int64             `json:"size"`
	Hashes    map[string]string `json:"hashes,omitempty"` // remote digests: md5, sha1, sha512
	// VerifiedHashes records the remote digests the on disk file was
	// last checked against, so a republished file re-downloads.
	VerifiedHashes map[string]string `json:"verified_hashes,omitempty"`
	HashOK         bool              `json:"hash_ok"`
	FastHash       string            `json:"fast_hash,omitempty"`
	SHA256         string            `json:"sha256,omitempty"`
	Status         string            `json:"status"`
	Error          string            `json:"error,omitempty"`
}

// Manifest is the archive record for one project. It doubles as the
// resume state: done entries whose file still exists and passes the
// fast hash check are skipped.
type Manifest struct {
	Version   int          `json:"version"`
	Tool      string       `json:"tool"`
	ScrapedAt time.Time    `json:"scraped_at"`
	Project   *Project     `json:"project"`
	Items     []*ItemEntry `json:"items"`

	mu   sync.Mutex `json:"-"`
	path string     `json:"-"`
}

func manifestPath(dir string) string { return filepath.Join(dir, manifestName) }

// NewManifest returns an empty manifest bound to dir.
func NewManifest(dir string) *Manifest {
	return &Manifest{path: manifestPath(dir)}
}

// Lock and Unlock guard the item list while the worker pool updates
// entries concurrently.
func (m *Manifest) Lock()   { m.mu.Lock() }
func (m *Manifest) Unlock() { m.mu.Unlock() }

func LoadManifest(dir string) (*Manifest, error) {
	m := &Manifest{path: manifestPath(dir)}
	b, err := os.ReadFile(m.path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	m.path = manifestPath(dir)
	if m.Version != 2 {
		return &Manifest{path: m.path}, nil
	}
	return m, nil
}

func (m *Manifest) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tmp := m.path + ".tmp"
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// Find locates an entry by id within a section.
func (m *Manifest) Find(section, id string) *ItemEntry {
	for _, e := range m.Items {
		if e.Section == section && e.ID == id {
			return e
		}
	}
	return nil
}

func (m *Manifest) FindByName(section, filename string) *ItemEntry {
	for _, e := range m.Items {
		if e.Section == section && e.Filename == filename {
			return e
		}
	}
	return nil
}

// NeedsDownload reports whether a done entry actually exists on disk
// and still matches its recorded fast hash.
func (m *Manifest) NeedsDownload(e *ItemEntry, dir string) bool {
	if e.Status != StDone {
		return true
	}
	if e.Path == "" {
		return true
	}
	p := filepath.Join(dir, e.Path)
	st, err := os.Stat(p)
	if err != nil {
		return true
	}
	if st.IsDir() {
		// a directory entry counts as done only when it has contents
		kids, derr := os.ReadDir(p)
		return derr != nil || len(kids) == 0
	}
	if e.Size > 0 && st.Size() != e.Size {
		return true
	}
	if e.FastHash == "" {
		return false
	}
	fh, _, ferr := FastHash(p)
	return ferr != nil || fh != e.FastHash
}

func (m *Manifest) Counts() (done, failed, pending int) {
	for _, e := range m.Items {
		switch e.Status {
		case StDone:
			done++
		case StFailed:
			failed++
		default:
			pending++
		}
	}
	return
}
