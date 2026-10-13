// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"mod-archiver/internal/core"
)

const tsAPI = "https://thunderstore.io/api/experimental"

// Thunderstore uses the public experimental API. Community and search
// endpoints are behind Cloudflare, so we stick to the ungated
// /api/experimental/package path and archive the latest version plus
// one pinned version per input.
type Thunderstore struct{}

func (Thunderstore) Name() string { return "thunderstore" }

func (Thunderstore) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "ts:") || strings.HasPrefix(input, "thunderstore:") {
		return true
	}
	return strings.HasSuffix(host, "thunderstore.io") && len(segs) >= 2
}

// tsRef parses input into namespace, name and optional version.
func tsRef(input string) (ns, name, version string, err error) {
	for _, p := range []string{"thunderstore:", "ts:"} {
		if strings.HasPrefix(input, p) {
			parts := strings.Split(strings.TrimSuffix(input[len(p):], "/"), ":")
			switch len(parts) {
			case 2:
				return parts[0], parts[1], "", nil
			case 3:
				return parts[0], parts[1], parts[2], nil
			}
			return "", "", "", fmt.Errorf("thunderstore: want ts:<namespace>:<name>[:<version>]")
		}
	}
	_, segs := SplitHost(input)
	// thunderstore.io/c/{community}/p/{namespace}/{name}/
	for i, s := range segs {
		if s == "p" && i+2 < len(segs) {
			return segs[i+1], segs[i+2], "", nil
		}
	}
	// thunderstore.io/package/{namespace}/{name}/
	for i, s := range segs {
		if s == "package" && i+2 < len(segs) {
			return segs[i+1], segs[i+2], "", nil
		}
	}
	return "", "", "", fmt.Errorf("cannot parse thunderstore package from %q", input)
}

func (Thunderstore) DirName(input string) string {
	ns, name, _, err := tsRef(input)
	if err == nil {
		return ns + "-" + name
	}
	return "thunderstore-pkg"
}

type tsVersion struct {
	VersionNumber string   `json:"version_number"`
	FullName      string   `json:"full_name"`
	Description   string   `json:"description"`
	Icon          string   `json:"icon"`
	Dependencies  []string `json:"dependencies"`
	DownloadURL   string   `json:"download_url"`
	Downloads     int64    `json:"downloads"`
	DateCreated   string   `json:"date_created"`
	WebsiteURL    string   `json:"website_url"`
	IsActive      bool     `json:"is_active"`
}

type tsPackage struct {
	Namespace      string    `json:"namespace"`
	Name           string    `json:"name"`
	FullName       string    `json:"full_name"`
	Owner          string    `json:"owner"`
	PackageURL     string    `json:"package_url"`
	DateCreated    string    `json:"date_created"`
	DateUpdated    string    `json:"date_updated"`
	RatingScore    float64   `json:"rating_score"`
	IsDeprecated   bool      `json:"is_deprecated"`
	TotalDownloads int64     `json:"total_downloads"`
	Latest         tsVersion `json:"latest"`
	Listings       []struct {
		HasNSFW    bool     `json:"has_nsfw_content"`
		Categories []string `json:"categories"`
		Community  string   `json:"community"`
		Reviewed   string   `json:"review_status"`
	} `json:"community_listings"`
}

func (Thunderstore) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	ns, name, version, err := tsRef(cc.Input)
	if err != nil {
		return nil, err
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest

	term.Infof("%s fetching package %s/%s", term.Bold("thunderstore"), ns, name)
	var p tsPackage
	u := fmt.Sprintf("%s/package/%s/%s/", tsAPI, ns, name)
	if err := c.GetJSON(ctx, u, "https://thunderstore.io/", nil, &p); err != nil {
		return nil, err
	}

	community := ""
	cats := []string{}
	if len(p.Listings) > 0 {
		community = p.Listings[0].Community
		cats = p.Listings[0].Categories
	}
	pageURL := p.PackageURL
	if pageURL == "" {
		pageURL = fmt.Sprintf("https://thunderstore.io/c/%s/p/%s/%s/", community, ns, name)
	}
	m.Project = &core.Project{
		Source:      "thunderstore",
		ID:          p.FullName,
		Slug:        ns + "/" + name,
		Title:       name,
		Description: p.Latest.Description,
		Author:      p.Owner,
		Game:        community,
		URL:         pageURL,
		HeaderImage: p.Latest.Icon,
		Extra: map[string]string{
			"updated":    p.DateUpdated,
			"downloads":  fmt.Sprint(p.TotalDownloads),
			"rating":     fmt.Sprint(p.RatingScore),
			"deprecated": fmt.Sprint(p.IsDeprecated),
			"categories": strings.Join(cats, ","),
		},
	}
	term.Okf("%s/%s: %s (%s)", ns, name, p.FullName, community)
	if p.IsDeprecated {
		term.Warnf("%s: deprecated", p.FullName)
	}
	m.Save()
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	versions := []tsVersion{p.Latest}
	if version != "" && version != p.Latest.VersionNumber {
		var v tsVersion
		vu := fmt.Sprintf("%s/package/%s/%s/%s/", tsAPI, ns, name, version)
		if err := c.GetJSON(ctx, vu, pageURL, nil, &v); err != nil {
			return nil, fmt.Errorf("version %s: %w", version, err)
		}
		versions = append(versions, v)
	}

	for _, v := range versions {
		if !v.IsActive || v.DownloadURL == "" {
			continue
		}
		e := Upsert(m, "files", v.FullName)
		e.Title = v.FullName
		e.Filename = v.FullName + ".zip"
		e.SourceURL = pageURL
		e.Category = v.VersionNumber
		e.Added = v.DateCreated
		e.Summary = strings.Join(v.Dependencies, ", ")
	}
	if cc.Opts.Images && p.Latest.Icon != "" {
		e := Upsert(m, "images", p.Latest.Icon)
		e.Filename = core.NameFromURL(p.Latest.Icon)
		e.Title = "icon"
		e.SourceURL = pageURL
	}
	m.Save()

	dlByID := map[string]string{}
	for _, v := range versions {
		dlByID[v.FullName] = v.DownloadURL
	}
	icon := p.Latest.Icon

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size,
		}
		if e.Section == "files" {
			t.DirectURL = dlByID[e.ID]
		} else {
			t.DirectURL = icon
		}
		return t
	}), nil
}
