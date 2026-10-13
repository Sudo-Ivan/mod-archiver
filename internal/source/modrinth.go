// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"mod-archiver/internal/core"
)

var modrinthAPI = "https://api.modrinth.com/v2"

// Modrinth uses the public v2 REST API. No auth needed. A descriptive
// User-Agent is required by their API policy.
type Modrinth struct{}

func (Modrinth) Name() string { return "modrinth" }

func (Modrinth) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "modrinth:") {
		return true
	}
	return strings.HasSuffix(host, "modrinth.com") && len(segs) >= 2
}

func (Modrinth) DirName(input string) string {
	if id, err := modrinthID(input); err == nil {
		return id
	}
	return "modrinth-project"
}

func modrinthID(input string) (string, error) {
	s := input
	if strings.HasPrefix(s, "modrinth:") {
		s = s[9:]
		return s, nil
	}
	host, segs := SplitHost(s)
	if !strings.HasSuffix(host, "modrinth.com") || len(segs) < 2 {
		return "", fmt.Errorf("cannot parse modrinth id from %q", input)
	}
	// modrinth.com/<project_type>/<slug>
	return segs[1], nil
}

type mrProject struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	ProjectType string `json:"project_type"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Downloads   int64  `json:"downloads"`
	Followers   int64  `json:"followers"`
	Status      string `json:"status"`
	IconURL     string `json:"icon_url"`
	RawIconURL  string `json:"raw_icon_url"`
	License     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"license"`
	GameVersions []string `json:"game_versions"`
	Loaders      []string `json:"loaders"`
	Categories   []string `json:"categories"`
	Gallery      []struct {
		URL     string `json:"url"`
		RawURL  string `json:"raw_url"`
		Title   string `json:"title"`
		Created string `json:"created"`
	} `json:"gallery"`
}

type mrVersion struct {
	ID            string   `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	VersionNumber string   `json:"version_number"`
	VersionType   string   `json:"version_type"`
	GameVersions  []string `json:"game_versions"`
	Loaders       []string `json:"loaders"`
	DatePublished string   `json:"date_published"`
	Downloads     int64    `json:"downloads"`
	Status        string   `json:"status"`
	Files         []struct {
		Hashes   map[string]string `json:"hashes"`
		URL      string            `json:"url"`
		Filename string            `json:"filename"`
		Primary  bool              `json:"primary"`
		Size     int64             `json:"size"`
	} `json:"files"`
}

func mrHeaders() http.Header {
	return http.Header{"User-Agent": []string{"mod-archiver/" + toolVersion() + " (mod-archiver CLI)"}}
}

func toolVersion() string { return "1.0.0" }

func (Modrinth) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	id, err := modrinthID(cc.Input)
	if err != nil {
		return nil, err
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest

	term.Infof("%s fetching project", term.Bold(id))
	var p mrProject
	if err := c.GetJSON(ctx, modrinthAPI+"/project/"+id, "https://modrinth.com/", mrHeaders(), &p); err != nil {
		return nil, err
	}
	m.Project = &core.Project{
		Source:      "modrinth",
		ID:          p.ID,
		Slug:        p.Slug,
		Title:       p.Title,
		Description: p.Description,
		Author:      "",
		Game:        "Minecraft",
		URL:         "https://modrinth.com/" + p.ProjectType + "/" + p.Slug,
		HeaderImage: firstNonEmpty(p.RawIconURL, p.IconURL),
		Extra: map[string]string{
			"project_type":  p.ProjectType,
			"status":        p.Status,
			"license":       p.License.ID,
			"downloads":     fmt.Sprint(p.Downloads),
			"followers":     fmt.Sprint(p.Followers),
			"game_versions": strings.Join(p.GameVersions, ","),
			"loaders":       strings.Join(p.Loaders, ","),
			"categories":    strings.Join(p.Categories, ","),
		},
	}
	term.Okf("%s: %s (%s)", p.Slug, p.Title, p.ProjectType)
	if p.Body != "" {
		if err := os.WriteFile(filepath.Join(cc.Dir, "body.md"), []byte(p.Body), 0o600); err != nil {
			term.Warnf("body snapshot: %v", err)
		}
	}
	saveOr(term, m)
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	var versions []mrVersion
	if err := c.GetJSON(ctx, modrinthAPI+"/project/"+p.ID+"/version?include_changelog=false", m.Project.URL, mrHeaders(), &versions); err != nil {
		return nil, err
	}

	nfiles := 0
	for _, v := range versions {
		if cc.Opts.VersionFilter != "" &&
			!strings.Contains(v.VersionNumber, cc.Opts.VersionFilter) &&
			!strings.Contains(v.Name, cc.Opts.VersionFilter) {
			continue
		}
		for _, f := range v.Files {
			fid := v.ID + "/" + f.Filename
			e := Upsert(m, "files", fid)
			e.Title = v.Name + " / " + f.Filename
			e.Filename = f.Filename
			e.SourceURL = m.Project.URL + "/version/" + v.ID
			e.Category = v.VersionType
			e.Added = v.DatePublished
			e.Summary = strings.Join(v.GameVersions, ", ") + " " + strings.Join(v.Loaders, ", ")
			e.Size = f.Size
			e.Hashes = map[string]string{}
			maps.Copy(e.Hashes, f.Hashes)
			nfiles++
		}
	}
	term.Infof("%s: %d file(s) across %d version(s)", p.Slug, nfiles, len(versions))

	if cc.Opts.Images {
		icon := firstNonEmpty(p.RawIconURL, p.IconURL)
		if icon != "" {
			e := Upsert(m, "images", icon)
			e.Filename = core.NameFromURL(icon)
			e.Title = "icon"
			e.SourceURL = m.Project.URL
		}
		for _, g := range p.Gallery {
			u := firstNonEmpty(g.RawURL, g.URL)
			e := Upsert(m, "images", u)
			e.Filename = core.NameFromURL(u)
			e.Title = g.Title
			e.Added = g.Created
			e.SourceURL = m.Project.URL
		}
	}
	saveOr(term, m)

	// map file id -> direct cdn url
	urlByID := map[string]string{}
	for _, v := range versions {
		for _, f := range v.Files {
			urlByID[v.ID+"/"+f.Filename] = f.URL
		}
	}

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size, Hashes: e.Hashes,
		}
		if e.Section == "files" {
			t.DirectURL = urlByID[e.ID]
		} else {
			t.DirectURL = e.ID // media entries key by url
		}
		return t
	}), nil
}
