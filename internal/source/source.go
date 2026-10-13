// SPDX-License-Identifier: 0BSD

// Package source implements the per-site collectors that turn a user
// supplied URL or id into a manifest plus a list of download tasks.
package source

import (
	"context"
	"fmt"
	"maps"
	url2 "net/url"
	"strings"

	"mod-archiver/internal/core"
)

// Options carries credentials and per source knobs set from flags
// or environment.
type Options struct {
	HashMode      string
	Fresh         bool
	Images        bool
	Videos        bool
	MetadataOnly  bool
	NexusKey      string
	NexusNXMKey   string // free accounts: key param from an nxm:// link
	NexusNXMExp   string // free accounts: expires param from an nxm:// link
	CFKey         string
	ModioKey      string
	SteamUser     string
	SteamPass     string
	SteamcmdPath  string
	GameHint      string // -game: nexus domain / steam appid / cf game slug
	VersionFilter string // optional substring filter for versioned files
}

// CollectCtx is handed to Source.Collect.
type CollectCtx struct {
	Input    string
	Dir      string
	Client   *core.Client
	Term     *core.Term
	Opts     *Options
	Manifest *core.Manifest
}

// Source turns an input into manifest items and download tasks.
type Source interface {
	Name() string
	// Match reports whether the input looks like it belongs to this
	// source.
	Match(input string) bool
	// DirName picks the archive directory name for the input.
	DirName(input string) string
	// Collect scrapes/API fetches metadata, fills cc.Manifest.Project
	// and upserts pending items, and returns the download tasks for
	// everything still needed.
	Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error)
}

// DepsSource returns ready-to-archive target refs for a package's
// declared dependencies. Implemented where the api exposes them.
type DepsSource interface {
	Dependencies(ctx context.Context, cc *CollectCtx) ([]string, error)
}

var sources = []Source{
	ModDB{},
	Nexus{},
	Steam{},
	CurseForge{},
	Modrinth{},
	Thunderstore{},
	GameBanana{},
	ModIO{},
}

// Lookup returns the source matching input, preferring an explicit
// -src hint when given.
func Lookup(input, hint string) (Source, error) {
	if hint != "" {
		for _, s := range sources {
			if s.Name() == hint {
				return s, nil
			}
		}
		return nil, fmt.Errorf("unknown source %q (want moddb, nexus, steam, curseforge, modrinth, thunderstore, gamebanana, modio)", hint)
	}
	for _, s := range sources {
		if s.Match(input) {
			return s, nil
		}
	}
	return nil, fmt.Errorf("cannot detect source for %q (use -src or a full URL)", input)
}

// Upsert returns the existing item for (section, id) or appends a new
// pending one.
func Upsert(m *core.Manifest, section, id string) *core.ItemEntry {
	if e := m.Find(section, id); e != nil {
		return e
	}
	e := &core.ItemEntry{ID: id, Section: section, Status: core.StPending}
	m.Items = append(m.Items, e)
	return e
}

// PendingTasks builds tasks for every manifest item that still needs
// bytes, calling mk for each entry to produce the source specific
// task fields.
func PendingTasks(cc *CollectCtx, mk func(e *core.ItemEntry, destName string) *core.Task) []*core.Task {
	seen := map[string]map[string]int{}
	var tasks []*core.Task
	for _, e := range cc.Manifest.Items {
		// remote republished with a different digest, refetch
		if e.Status == core.StDone && len(e.Hashes) > 0 && len(e.VerifiedHashes) > 0 &&
			!maps.Equal(e.Hashes, e.VerifiedHashes) {
			e.Status = core.StPending
		}
		if !cc.Manifest.NeedsDownload(e, cc.Dir) {
			continue
		}
		e.Status = core.StPending
		if seen[e.Section] == nil {
			seen[e.Section] = map[string]int{}
		}
		name := e.Filename
		if name == "" {
			name = e.ID
		}
		name = core.UniqueName(core.SanitizeName(name), seen[e.Section])
		t := mk(e, name)
		if t.Title == "" {
			t.Title = name
		}
		t.Entry = e
		tasks = append(tasks, t)
	}
	return tasks
}

// saveOr persists the manifest, warning when the write fails.
func saveOr(term *core.Term, m *core.Manifest) {
	if err := m.Save(); err != nil {
		term.Warnf("manifest save failed: %v", err)
	}
}

func urlQueryEscape(s string) string {
	return url2.QueryEscape(s)
}

// SplitHost extracts host and path segments from a URL-ish input.
func SplitHost(input string) (host string, segs []string) {
	s := strings.TrimSpace(input)
	for _, p := range []string{"https://", "http://", "//"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "/")
	parts := strings.SplitN(s, "/", 2)
	host = parts[0]
	if len(parts) > 1 {
		for seg := range strings.SplitSeq(parts[1], "/") {
			if seg != "" {
				segs = append(segs, seg)
			}
		}
	}
	return host, segs
}
