// SPDX-License-Identifier: 0BSD

package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const manifestName = "manifest.json"

const (
	stPending = "pending"
	stDone    = "done"
	stFailed  = "failed"
)

type FileEntry struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	PageURL  string `json:"page_url"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Category string `json:"category"`
	Uploader string `json:"uploader"`
	Added    string `json:"added"`
	Summary  string `json:"summary,omitempty"`
	Size     int64  `json:"size"`
	MD5      string `json:"md5"`
	MD5OK    bool   `json:"md5_ok"`
	FastHash string `json:"fast_hash,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

type MediaEntry struct {
	URL      string `json:"url"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	FastHash string `json:"fast_hash,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
}

// Manifest is the archive record for one mod. It is also the resume
// state: entries whose Status is done and whose file still exists are
// skipped on the next run.
type Manifest struct {
	Version   int           `json:"version"`
	Tool      string        `json:"tool"`
	ScrapedAt time.Time     `json:"scraped_at"`
	Mod       *Mod          `json:"mod"`
	Files     []*FileEntry  `json:"files"`
	Images    []*MediaEntry `json:"images"`
	Videos    []*MediaEntry `json:"videos"`

	mu   sync.Mutex `json:"-"`
	path string     `json:"-"`
}

func manifestPath(dir string) string { return filepath.Join(dir, manifestName) }

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
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *Manifest) findFile(id string) *FileEntry {
	for _, f := range m.Files {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func (m *Manifest) findFileBySlug(slug string) *FileEntry {
	for _, f := range m.Files {
		if f.Slug == slug {
			return f
		}
	}
	return nil
}

func (m *Manifest) findMedia(list []*MediaEntry, url string) *MediaEntry {
	for _, e := range list {
		if e.URL == url {
			return e
		}
	}
	return nil
}

func (m *Manifest) counts() (done, failed, pending int) {
	for _, f := range m.Files {
		switch f.Status {
		case stDone:
			done++
		case stFailed:
			failed++
		default:
			pending++
		}
	}
	for _, e := range m.Images {
		switch e.Status {
		case stDone:
			done++
		case stFailed:
			failed++
		default:
			pending++
		}
	}
	for _, e := range m.Videos {
		switch e.Status {
		case stDone:
			done++
		case stFailed:
			failed++
		default:
			pending++
		}
	}
	return
}
