// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"fmt"
	"net/http"
	url2 "net/url"
	"path/filepath"
	"strconv"
	"strings"

	"mod-archiver/internal/core"
)

var cfAPI = "https://api.curseforge.com/v1"
var cfCDN = "https://edge.forgecdn.net"

// Circulated public key from the official client. Overridden by
// -cf-key or CURSEFORGE_API_KEY; a personal key is strongly
// recommended since shared keys get rotated.
const cfDefaultKey = "$2a$10$bL4bIL5pUWqfcO7KQtnMReakwtfHbNKh6v1uTpKlzhwoueEJQnPnm"

// CurseForge uses api.curseforge.com which requires an x-api-key
// header. CDN downloads also require the key since July 2026.
type CurseForge struct{}

func (CurseForge) Name() string { return "curseforge" }

func (CurseForge) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "cf:") || strings.HasPrefix(input, "curseforge:") {
		return true
	}
	return strings.HasSuffix(host, "curseforge.com") && len(segs) >= 1
}

func (CurseForge) DirName(input string) string {
	_, segs := SplitHost(input)
	if len(segs) > 0 {
		return segs[len(segs)-1]
	}
	if i := strings.LastIndex(input, ":"); i >= 0 && i+1 < len(input) {
		return input[i+1:]
	}
	return "curseforge-mod"
}

type cfGame struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type cfCategory struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	IsClass bool   `json:"isClass"`
}

type cfMod struct {
	ID        int64  `json:"id"`
	GameID    int64  `json:"gameId"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Summary   string `json:"summary"`
	Status    int    `json:"status"`
	Downloads int64  `json:"downloadCount"`
	Links     struct {
		WebsiteURL string `json:"websiteUrl"`
	} `json:"links"`
	Authors []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"authors"`
	Logo struct {
		URL          string `json:"url"`
		ThumbnailURL string `json:"thumbnailUrl"`
	} `json:"logo"`
	Screenshots []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"screenshots"`
	AllowDistribution *bool  `json:"allowModDistribution"`
	DateModified      string `json:"dateModified"`
}

type cfFile struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
	FileName    string `json:"fileName"`
	ReleaseType int    `json:"releaseType"` // 1 rel 2 beta 3 alpha
	FileDate    string `json:"fileDate"`
	FileLength  int64  `json:"fileLength"`
	DownloadURL string `json:"downloadUrl"`
	Downloads   int64  `json:"downloadCount"`
	IsAvailable bool   `json:"isAvailable"`
	Deps        []struct {
		ModID    int64 `json:"modId"`
		Relation int   `json:"relationType"`
	} `json:"dependencies"`
	Hashes []struct {
		Value string `json:"value"`
		Algo  int    `json:"algo"` // 1 sha1, 2 md5
	} `json:"hashes"`
	GameVersions []string `json:"gameVersions"`
}

type cfResp[T any] struct {
	Data       T `json:"data"`
	Pagination struct {
		Index       int `json:"index"`
		PageSize    int `json:"pageSize"`
		ResultCount int `json:"resultCount"`
		TotalCount  int `json:"totalCount"`
	} `json:"pagination"`
}

func cfHeaders(key string) http.Header {
	return http.Header{
		"x-api-key":  []string{key},
		"User-Agent": []string{"mod-archiver/" + toolVersion()},
		"Accept":     []string{"application/json"},
	}
}

// cfGet decodes the {data: ...} envelope.
func cfGet[T any](ctx context.Context, c *core.Client, key, path string, out *T) error {
	var env cfResp[T]
	if err := c.GetJSON(ctx, cfAPI+path, "https://www.curseforge.com/", cfHeaders(key), &env); err != nil {
		return err
	}
	*out = env.Data
	return nil
}

// cfResolveInput turns a cf target into a numeric mod id plus the
// game segment for building dep refs.
func cfResolveInput(ctx context.Context, c *core.Client, key, input, gameHint string) (int64, string, error) {
	var gameSeg, classSeg, modSeg string
	if strings.HasPrefix(input, "cf:") || strings.HasPrefix(input, "curseforge:") {
		parts := strings.Split(strings.TrimPrefix(strings.TrimPrefix(input, "curseforge:"), "cf:"), ":")
		if len(parts) == 2 {
			gameSeg, modSeg = parts[0], parts[1]
		} else {
			return 0, "", fmt.Errorf("curseforge: want cf:<game-slug-or-id>:<mod-slug-or-id>")
		}
	} else {
		_, segs := SplitHost(input)
		if len(segs) >= 3 {
			gameSeg, classSeg, modSeg = segs[0], segs[1], segs[2]
		} else if len(segs) == 2 {
			gameSeg, modSeg = segs[0], segs[1]
		} else {
			return 0, "", fmt.Errorf("cannot parse curseforge url %q", input)
		}
	}
	if gameHint != "" && gameSeg == "" {
		gameSeg = gameHint
	}

	if n, err := strconv.ParseInt(modSeg, 10, 64); err == nil {
		return n, gameSeg, nil
	}
	// resolve slug -> gameId -> classId -> search -> mod id
	var gameID int64
	if g, err := strconv.ParseInt(gameSeg, 10, 64); err == nil {
		gameID = g
	} else {
		var err error
		gameID, err = cfGameID(ctx, c, key, gameSeg)
		if err != nil {
			return 0, "", err
		}
	}
	var classID int64
	if classSeg != "" {
		if cid, err := cfClassID(ctx, c, key, gameID, classSeg); err == nil {
			classID = cid
		}
	}
	q := fmt.Sprintf("/mods/search?gameId=%d&slug=%s", gameID, url2.QueryEscape(modSeg))
	if classID > 0 {
		q += fmt.Sprintf("&classId=%d", classID)
	}
	var hits []cfMod
	if err := cfGet(ctx, c, key, q, &hits); err != nil {
		return 0, "", err
	}
	if len(hits) == 0 {
		return 0, "", fmt.Errorf("curseforge: no mod %q under game %d", modSeg, gameID)
	}
	return hits[0].ID, gameSeg, nil
}

// forgecdnURL builds the deterministic CDN path for a file.
func forgecdnURL(fileID int64, fileName string) string {
	return fmt.Sprintf("%s/files/%d/%d/%s", cfCDN, fileID/1000, fileID%1000, url2.PathEscape(fileName))
}

// Search hits the v1 mods search endpoint. Needs an api key and a
// game hint (-game minecraft or a numeric game id).
func (CurseForge) Search(ctx context.Context, sc *SearchCtx) ([]SearchResult, error) {
	key := sc.Opts.CFKey
	if key == "" {
		key = cfDefaultKey
	}
	if sc.Opts.GameHint == "" {
		return nil, fmt.Errorf("curseforge search needs a game, pass -game <slug or id>")
	}
	gid, err := cfGameID(ctx, sc.Client, key, sc.Opts.GameHint)
	if err != nil {
		return nil, err
	}
	limit := sc.Limit
	if limit <= 0 {
		limit = 10
	}
	var r cfResp[[]cfMod]
	u := fmt.Sprintf("%s/mods/search?gameId=%d&searchFilter=%s&sortField=2&sortOrder=desc&pageSize=%d",
		cfAPI, gid, urlQueryEscape(sc.Query), limit)
	if err := sc.Client.GetJSON(ctx, u, "https://www.curseforge.com/", cfHeaders(key), &r); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(r.Data))
	for _, d := range r.Data {
		author := ""
		if len(d.Authors) > 0 {
			author = d.Authors[0].Name
		}
		out = append(out, SearchResult{
			Source: "curseforge", Ref: "cf:" + sc.Opts.GameHint + ":" + d.Slug,
			Title: d.Name, Author: author, Game: sc.Opts.GameHint,
			Downloads: d.Downloads,
			URL:       fmt.Sprintf("https://www.curseforge.com/mods/%d", d.ID),
			Summary:   d.Summary,
		})
	}
	return out, nil
}

// Dependencies collects required file-level deps into
// cf:<game>:<modId> refs. relationType 3 means required.
func (CurseForge) Dependencies(ctx context.Context, cc *CollectCtx) ([]string, error) {
	key := cc.Opts.CFKey
	if key == "" {
		key = cfDefaultKey
	}
	modID, gameSeg, err := cfResolveInput(ctx, cc.Client, key, cc.Input, cc.Opts.GameHint)
	if err != nil {
		return nil, err
	}
	var files []cfFile
	if err := cfGet(ctx, cc.Client, key, fmt.Sprintf("/mods/%d/files?pageSize=50", modID), &files); err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var out []string
	for _, f := range files {
		for _, d := range f.Deps {
			if d.Relation != 3 || d.ModID == 0 || seen[d.ModID] {
				continue
			}
			seen[d.ModID] = true
			out = append(out, fmt.Sprintf("cf:%s:%d", gameSeg, d.ModID))
		}
	}
	return out, nil
}

func (CurseForge) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	key := cc.Opts.CFKey
	if key == "" {
		key = cfDefaultKey
		cc.Term.Debugf("curseforge: using embedded public key, set CURSEFORGE_API_KEY to override")
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest

	modID, gameSeg, err := cfResolveInput(ctx, c, key, cc.Input, cc.Opts.GameHint)
	if err != nil {
		return nil, err
	}

	var mod cfMod
	term.Infof("%s fetching mod %d", term.Bold("curseforge"), modID)
	if err := cfGet(ctx, c, key, fmt.Sprintf("/mods/%d", modID), &mod); err != nil {
		return nil, err
	}
	author := ""
	if len(mod.Authors) > 0 {
		author = mod.Authors[0].Name
	}
	gameName := gameSeg
	if mod.GameID > 0 {
		var g cfGame
		if err := cfGet(ctx, c, key, fmt.Sprintf("/games/%d", mod.GameID), &g); err == nil && g.Name != "" {
			gameName = g.Name
		}
	}
	m.Project = &core.Project{
		Source:      "curseforge",
		ID:          strconv.FormatInt(mod.ID, 10),
		Slug:        mod.Slug,
		Title:       mod.Name,
		Description: mod.Summary,
		Author:      author,
		Game:        gameName,
		URL:         mod.Links.WebsiteURL,
		HeaderImage: mod.Logo.URL,
		Extra: map[string]string{
			"downloads": strconv.FormatInt(mod.Downloads, 10),
			"modified":  mod.DateModified,
		},
	}
	term.Okf("%s: %s", mod.Slug, mod.Name)
	if mod.AllowDistribution != nil && !*mod.AllowDistribution {
		term.Warnf("%s: author opted out of third party distribution, CDN urls will be constructed directly", mod.Name)
	}
	saveOr(term, m)
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	// paginate files
	var files []cfFile
	for idx := 0; ; {
		var page []cfFile
		var env cfResp[[]cfFile]
		if err := c.GetJSON(ctx, fmt.Sprintf("%s/mods/%d/files?index=%d&pageSize=50", cfAPI, modID, idx), m.Project.URL, cfHeaders(key), &env); err != nil {
			return nil, err
		}
		page = env.Data
		files = append(files, page...)
		idx += env.Pagination.ResultCount
		if env.Pagination.ResultCount == 0 || idx >= env.Pagination.TotalCount {
			break
		}
	}
	term.Infof("%s: %d file(s) listed", mod.Slug, len(files))

	for _, f := range files {
		if !f.IsAvailable {
			continue
		}
		id := strconv.FormatInt(f.ID, 10)
		e := Upsert(m, "files", id)
		e.Title = firstNonEmpty(f.DisplayName, f.FileName)
		e.Filename = f.FileName
		e.SourceURL = m.Project.URL + "/files/" + id
		e.Added = f.FileDate
		switch f.ReleaseType {
		case 1:
			e.Category = "release"
		case 2:
			e.Category = "beta"
		case 3:
			e.Category = "alpha"
		}
		e.Summary = strings.Join(f.GameVersions, ", ")
		e.Size = f.FileLength
		e.Hashes = map[string]string{}
		for _, h := range f.Hashes {
			switch h.Algo {
			case 1:
				e.Hashes["sha1"] = h.Value
			case 2:
				e.Hashes["md5"] = h.Value
			}
		}
	}
	if cc.Opts.Images {
		if mod.Logo.URL != "" {
			e := Upsert(m, "images", mod.Logo.URL)
			e.Filename = core.NameFromURL(mod.Logo.URL)
			e.Title = "logo"
			e.SourceURL = m.Project.URL
		}
		for _, s := range mod.Screenshots {
			e := Upsert(m, "images", s.URL)
			e.Filename = core.NameFromURL(s.URL)
			e.Title = s.Title
			e.SourceURL = m.Project.URL
		}
	}
	saveOr(term, m)

	// per entry download urls
	dlByID := map[int64]string{}
	for _, f := range files {
		if f.DownloadURL != "" {
			dlByID[f.ID] = f.DownloadURL
		} else {
			dlByID[f.ID] = forgecdnURL(f.ID, f.FileName)
		}
	}
	cdnHdr := cfHeaders(key)

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size, Hashes: e.Hashes,
		}
		if e.Section == "files" {
			fid, _ := strconv.ParseInt(e.ID, 10, 64)
			t.DirectURL = dlByID[fid]
			t.Header = cdnHdr
		} else {
			t.DirectURL = e.ID
		}
		return t
	}), nil
}

// cfGameID resolves a game slug to its numeric id by paging the games
// list once.
func cfGameID(ctx context.Context, c *core.Client, key, slug string) (int64, error) {
	for idx := 0; ; {
		var env cfResp[[]cfGame]
		if err := c.GetJSON(ctx, fmt.Sprintf("%s/games?index=%d&pageSize=50", cfAPI, idx), "", cfHeaders(key), &env); err != nil {
			return 0, err
		}
		for _, g := range env.Data {
			if g.Slug == slug {
				return g.ID, nil
			}
		}
		idx += env.Pagination.ResultCount
		if env.Pagination.ResultCount == 0 || idx >= env.Pagination.TotalCount {
			return 0, fmt.Errorf("curseforge: unknown game slug %q", slug)
		}
	}
}

// cfClassID resolves a class slug like mc-mods to its classId.
func cfClassID(ctx context.Context, c *core.Client, key string, gameID int64, slug string) (int64, error) {
	var env cfResp[[]cfCategory]
	err := c.GetJSON(ctx, fmt.Sprintf("%s/categories?gameId=%d&classesOnly=true", cfAPI, gameID), "", cfHeaders(key), &env)
	if err != nil {
		return 0, err
	}
	for _, cat := range env.Data {
		if cat.IsClass && cat.Slug == slug {
			return cat.ID, nil
		}
	}
	return 0, fmt.Errorf("curseforge: unknown class slug %q for game %d", slug, gameID)
}
