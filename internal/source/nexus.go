// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"mod-archiver/internal/core"
)

var nexusAPI = "https://api.nexusmods.com/v1"

// Nexus uses the Nexus Mods v1 REST API. It needs a personal api key
// (nexusmods.com account settings, API access tab). Free accounts must
// additionally pass the key/expires pair from an nxm:// download link
// to mint CDN urls.
type Nexus struct{}

func (Nexus) Name() string { return "nexus" }

func (Nexus) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "nexus:") {
		return true
	}
	return strings.HasSuffix(host, "nexusmods.com") && len(segs) >= 3 && segs[1] == "mods"
}

func (Nexus) DirName(input string) string {
	if game, id, err := nexusRef(input, ""); err == nil {
		return game + "-" + id
	}
	return "nexus-mod"
}

// nexusRef extracts game domain and mod id.
func nexusRef(input, gameHint string) (game, modID string, err error) {
	if strings.HasPrefix(input, "nexus:") {
		parts := strings.Split(input[6:], ":")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
		return "", "", fmt.Errorf("nexus: want nexus:<game-domain>:<mod-id>")
	}
	host, segs := SplitHost(input)
	if strings.HasSuffix(host, "nexusmods.com") {
		if len(segs) >= 3 && segs[1] == "mods" {
			return segs[0], segs[2], nil
		}
		return "", "", fmt.Errorf("cannot parse nexus url %q", input)
	}
	// bare numeric id with -game hint
	if gameHint != "" {
		return gameHint, strings.TrimSpace(input), nil
	}
	return "", "", fmt.Errorf("cannot parse nexus id from %q (use nexus:<game>:<id> or -game)", input)
}

type nxMod struct {
	Name         string `json:"name"`
	Summary      string `json:"summary"`
	Description  string `json:"description"`
	PictureURL   string `json:"picture_url"`
	ModID        int64  `json:"mod_id"`
	GameID       int64  `json:"game_id"`
	DomainName   string `json:"domain_name"`
	Version      string `json:"version"`
	Author       string `json:"author"`
	UploadedBy   string `json:"uploaded_by"`
	Status       string `json:"status"`
	Available    bool   `json:"available"`
	Adult        bool   `json:"contains_adult_content"`
	ModDownloads int64  `json:"mod_downloads"`
	Endorsements int64  `json:"endorsement_count"`
	CreatedTime  string `json:"created_time"`
	UpdatedTime  string `json:"updated_time"`
}

type nxFile struct {
	FileID       int64  `json:"file_id"`
	UID          int64  `json:"uid"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	FileName     string `json:"file_name"`
	CategoryName string `json:"category_name"`
	IsPrimary    bool   `json:"is_primary"`
	SizeKB       int64  `json:"size_kb"`
	SizeInBytes  int64  `json:"size_in_bytes"`
	Size         int64  `json:"size"`
	ModVersion   string `json:"mod_version"`
	UploadedTime string `json:"uploaded_time"`
	Description  string `json:"description"`
}

type nxFileList struct {
	Files []nxFile `json:"files"`
}

type nxLink struct {
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	URI       string `json:"URI"`
}

func nxHeaders(key string) http.Header {
	return http.Header{
		"apikey":              []string{key},
		"Application-Name":    []string{"mod-archiver"},
		"Application-Version": []string{toolVersion()},
	}
}

func (Nexus) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	game, modID, err := nexusRef(cc.Input, cc.Opts.GameHint)
	if err != nil {
		return nil, err
	}
	key := cc.Opts.NexusKey
	if key == "" {
		return nil, fmt.Errorf("nexus needs an api key: set NEXUS_API_KEY or -nexus-key (yours is at nexusmods.com, my account, api access)")
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest
	hdr := nxHeaders(key)

	base := fmt.Sprintf("%s/games/%s/mods/%s", nexusAPI, game, modID)
	term.Infof("%s fetching mod %s/%s", term.Bold("nexus"), game, modID)

	var mod nxMod
	if err := c.GetJSON(ctx, base+".json", "https://www.nexusmods.com/", hdr, &mod); err != nil {
		return nil, err
	}
	pageURL := fmt.Sprintf("https://www.nexusmods.com/%s/mods/%d", game, mod.ModID)
	m.Project = &core.Project{
		Source:      "nexus",
		ID:          strconv.FormatInt(mod.ModID, 10),
		Slug:        game + "/" + strconv.FormatInt(mod.ModID, 10),
		Title:       mod.Name,
		Description: mod.Summary,
		Author:      firstNonEmpty(mod.UploadedBy, mod.Author),
		Game:        mod.DomainName,
		URL:         pageURL,
		HeaderImage: mod.PictureURL,
		Extra: map[string]string{
			"version":   mod.Version,
			"status":    mod.Status,
			"downloads": strconv.FormatInt(mod.ModDownloads, 10),
			"updated":   mod.UpdatedTime,
		},
	}
	term.Okf("%s/%d: %s", game, mod.ModID, mod.Name)
	if mod.Adult {
		term.Warnf("%s: flagged adult content", mod.Name)
	}
	m.Save()
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	var fl nxFileList
	if err := c.GetJSON(ctx, base+"/files.json", pageURL, hdr, &fl); err != nil {
		return nil, err
	}
	term.Infof("%s: %d file(s) listed", mod.Name, len(fl.Files))

	for _, f := range fl.Files {
		id := strconv.FormatInt(f.FileID, 10)
		e := Upsert(m, "files", id)
		e.Title = f.Name
		e.Filename = f.FileName
		e.SourceURL = pageURL + "?tab=files"
		e.Category = firstNonEmpty(f.CategoryName, "")
		e.Added = f.UploadedTime
		e.Summary = f.Description
		e.Uploader = mod.Author
		size := f.SizeInBytes
		if size == 0 && f.SizeKB > 0 {
			size = f.SizeKB * 1024
		}
		if size == 0 && f.Size > 0 {
			size = f.Size * 1024 // legacy field is KiB
		}
		e.Size = size
	}
	if cc.Opts.Images && mod.PictureURL != "" {
		e := Upsert(m, "images", mod.PictureURL)
		e.Filename = core.NameFromURL(mod.PictureURL)
		e.Title = "cover"
		e.SourceURL = pageURL
	}
	m.Save()

	dlURL := func(rctx context.Context, fileID string) (string, error) {
		u := base + "/files/" + fileID + "/download_link.json"
		if cc.Opts.NexusNXMKey != "" {
			u += "?key=" + cc.Opts.NexusNXMKey
			if cc.Opts.NexusNXMExp != "" {
				u += "&expires=" + cc.Opts.NexusNXMExp
			}
		}
		var links []nxLink
		if err := c.GetJSON(rctx, u, pageURL, hdr, &links); err != nil {
			return "", err
		}
		if len(links) == 0 {
			return "", fmt.Errorf("no download links returned for file %s", fileID)
		}
		return links[0].URI, nil
	}

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size,
		}
		if e.Section == "files" {
			fid := e.ID
			t.Resolve = func(rctx context.Context) (string, error) {
				return dlURL(rctx, fid)
			}
		} else {
			t.DirectURL = e.ID
		}
		return t
	}), nil
}
