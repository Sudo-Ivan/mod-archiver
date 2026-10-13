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
	"time"

	"mod-archiver/internal/core"
)

var modioAPI = "https://api.mod.io/v1"

// ModIO uses api.mod.io which requires a free api key from
// mod.io/me/access (env MODIO_API_KEY or -modio-key). Downloads are
// signed binary_url links on the modfile objects.
type ModIO struct{}

func (ModIO) Name() string { return "modio" }

func (ModIO) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "modio:") {
		return true
	}
	return host == "mod.io" && len(segs) >= 4 && segs[0] == "g" && segs[2] == "m"
}

// modioRef returns game and mod identifiers, either numeric ids or
// name_id slugs.
func modioRef(input string) (game, mod string, err error) {
	if strings.HasPrefix(input, "modio:") {
		parts := strings.Split(strings.TrimSuffix(input[6:], "/"), ":")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
		return "", "", fmt.Errorf("modio: want modio:<game>:<mod>")
	}
	_, segs := SplitHost(input)
	if len(segs) >= 4 && segs[0] == "g" && segs[2] == "m" {
		return segs[1], segs[3], nil
	}
	return "", "", fmt.Errorf("cannot parse mod.io mod from %q", input)
}

func (ModIO) DirName(input string) string {
	g, mod, err := modioRef(input)
	if err == nil {
		return g + "-" + mod
	}
	return "modio-mod"
}

func modioHeaders(key string) http.Header {
	return http.Header{
		"X-Modio-Apikey": []string{key},
		"Accept":         []string{"application/json"},
	}
}

type modioGame struct {
	ID       int64  `json:"id"`
	NameID   string `json:"name_id"`
	Name     string `json:"name"`
	Homepage string `json:"homepage"`
}

type modioList[T any] struct {
	Data   []T `json:"data"`
	Count  int `json:"result_count"`
	Offset int `json:"result_offset"`
	Limit  int `json:"result_limit"`
	Total  int `json:"result_total"`
}

type modioMod struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	NameID    string `json:"name_id"`
	Summary   string `json:"summary"`
	Desc      string `json:"description_plaintext"`
	DescFmt   string `json:"description"`
	DateAdd   int64  `json:"date_added"`
	DateUpd   int64  `json:"date_updated"`
	Submitter struct {
		NameID   string `json:"name_id"`
		Username string `json:"username"`
	} `json:"submitter"`
	Homepage string `json:"homepage_url"`
	Logo     struct {
		Filename string `json:"filename"`
		Original string `json:"original"`
		Thumb640 string `json:"thumb_640x360"`
	} `json:"logo"`
	Media struct {
		Images []struct {
			Filename string `json:"filename"`
			Original string `json:"original"`
			Thumb320 string `json:"thumb_320x180"`
		} `json:"images"`
	} `json:"media"`
	Stats struct {
		Downloads int64 `json:"total_downloads"`
		Ratings   int64 `json:"ratings_total_positive"`
	} `json:"stats"`
}

type modioFile struct {
	ID       int64  `json:"id"`
	ModID    int64  `json:"mod_id"`
	DateAdd  int64  `json:"date_added"`
	Version  string `json:"version"`
	FileSize int64  `json:"filesize"`
	Filename string `json:"filename"`
	FileHash struct {
		MD5 string `json:"md5"`
	} `json:"filehash"`
	Download struct {
		BinaryURL string `json:"binary_url"`
		Expires   int64  `json:"date_expires"`
	} `json:"download"`
	Changelog string `json:"changelog"`
}

// Search uses the _q full text filter on the mods endpoint. Needs an
// api key and a game hint (-game <slug or id>).
func (ModIO) Search(ctx context.Context, sc *SearchCtx) ([]SearchResult, error) {
	if sc.Opts.ModioKey == "" {
		return nil, fmt.Errorf("mod.io search needs -modio-key or MODIO_API_KEY")
	}
	if sc.Opts.GameHint == "" {
		return nil, fmt.Errorf("mod.io search needs a game, pass -game <slug or id>")
	}
	gid, gameName, err := modioGameID(ctx, sc.Client, sc.Opts.ModioKey, sc.Opts.GameHint)
	if err != nil {
		return nil, err
	}
	limit := sc.Limit
	if limit <= 0 {
		limit = 10
	}
	var list modioList[modioMod]
	u := fmt.Sprintf("/games/%d/mods?_q=%s&_limit=%d", gid, urlQueryEscape(sc.Query), limit)
	if err := modioGet(ctx, sc.Client, sc.Opts.ModioKey, u, &list); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, SearchResult{
			Source: "modio", Ref: "modio:" + gameName + ":" + m.NameID,
			Title: m.Name, Author: m.Submitter.Username, Game: gameName,
			Downloads: m.Stats.Downloads,
			URL:       fmt.Sprintf("https://mod.io/g/%s/m/%s", gameName, m.NameID),
			Summary:   m.Summary,
		})
	}
	return out, nil
}

// modioGet unwraps the list envelope or plain object response.
func modioGet[T any](ctx context.Context, c *core.Client, key, path string, out *T) error {
	return c.GetJSON(ctx, modioAPI+path, "https://mod.io/", modioHeaders(key), out)
}

// modioGameID resolves a name_id slug or returns the numeric id.
func modioGameID(ctx context.Context, c *core.Client, key, game string) (int64, string, error) {
	if id, err := strconv.ParseInt(game, 10, 64); err == nil {
		var g modioGame
		if err := modioGet(ctx, c, key, "/games/"+game, &g); err != nil {
			return 0, "", err
		}
		return id, g.Name, nil
	}
	var list modioList[modioGame]
	err := modioGet(ctx, c, key, "/games?name_id="+url2.QueryEscape(game)+"&_limit=1", &list)
	if err != nil {
		return 0, "", err
	}
	if len(list.Data) == 0 {
		return 0, "", fmt.Errorf("mod.io: unknown game %q", game)
	}
	return list.Data[0].ID, list.Data[0].Name, nil
}

func (ModIO) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	key := cc.Opts.ModioKey
	if key == "" {
		return nil, fmt.Errorf("mod.io needs an api key: set MODIO_API_KEY or -modio-key (get one at mod.io/me/access)")
	}
	game, modRef, err := modioRef(cc.Input)
	if err != nil {
		return nil, err
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest

	gid, gameName, err := modioGameID(ctx, c, key, game)
	if err != nil {
		return nil, err
	}
	term.Infof("%s fetching mod %s in game %d", term.Bold("modio"), modRef, gid)

	var mod modioMod
	if mid, perr := strconv.ParseInt(modRef, 10, 64); perr == nil {
		if err := modioGet(ctx, c, key, fmt.Sprintf("/games/%d/mods/%d", gid, mid), &mod); err != nil {
			return nil, err
		}
	} else {
		var list modioList[modioMod]
		err := modioGet(ctx, c, key, fmt.Sprintf("/games/%d/mods?name_id=%s&_limit=1", gid, url2.QueryEscape(modRef)), &list)
		if err != nil {
			return nil, err
		}
		if len(list.Data) == 0 {
			return nil, fmt.Errorf("mod.io: no mod %q in game %d", modRef, gid)
		}
		mod = list.Data[0]
	}

	desc := mod.Desc
	if desc == "" {
		desc = mod.DescFmt
	}
	pageURL := mod.Homepage
	if pageURL == "" {
		pageURL = fmt.Sprintf("https://mod.io/g/%s/m/%s", game, mod.NameID)
	}
	m.Project = &core.Project{
		Source:      "modio",
		ID:          strconv.FormatInt(mod.ID, 10),
		Slug:        mod.NameID,
		Title:       mod.Name,
		Description: mod.Summary,
		Author:      mod.Submitter.Username,
		Game:        gameName,
		URL:         pageURL,
		HeaderImage: mod.Logo.Original,
		Extra: map[string]string{
			"game_id":   strconv.FormatInt(gid, 10),
			"downloads": strconv.FormatInt(mod.Stats.Downloads, 10),
			"updated":   time.Unix(mod.DateUpd, 0).UTC().Format(time.RFC3339),
		},
	}
	if desc != "" {
		m.Project.Extra["description"] = desc
	}
	term.Okf("%s/%s: %s (%s)", gameName, mod.NameID, mod.Name, pageURL)
	saveOr(term, m)
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	// paginate modfiles
	var files []modioFile
	for off := 0; ; {
		var page modioList[modioFile]
		err := modioGet(ctx, c, key, fmt.Sprintf("/games/%d/mods/%d/files?_offset=%d&_limit=100", gid, mod.ID, off), &page)
		if err != nil {
			return nil, err
		}
		files = append(files, page.Data...)
		off += page.Count
		if page.Count == 0 || off >= page.Total {
			break
		}
	}
	term.Infof("%s: %d file(s) listed", mod.Name, len(files))

	for _, f := range files {
		if f.Download.BinaryURL == "" {
			continue
		}
		id := strconv.FormatInt(f.ID, 10)
		e := Upsert(m, "files", id)
		e.Title = firstNonEmpty(f.Version, f.Filename)
		e.Filename = f.Filename
		e.SourceURL = pageURL
		e.Category = f.Version
		e.Added = time.Unix(f.DateAdd, 0).UTC().Format(time.RFC3339)
		e.Summary = f.Changelog
		e.Size = f.FileSize
		if f.FileHash.MD5 != "" {
			e.Hashes = map[string]string{"md5": f.FileHash.MD5}
		}
	}
	if cc.Opts.Images {
		if mod.Logo.Original != "" {
			e := Upsert(m, "images", mod.Logo.Original)
			e.Filename = core.NameFromURL(mod.Logo.Original)
			e.Title = "logo"
			e.SourceURL = pageURL
		}
		for _, img := range mod.Media.Images {
			e := Upsert(m, "images", img.Original)
			e.Filename = core.NameFromURL(img.Original)
			e.SourceURL = pageURL
		}
	}
	saveOr(term, m)

	dlByID := map[int64]string{}
	for _, f := range files {
		dlByID[f.ID] = f.Download.BinaryURL
	}
	// binary urls expire, resolve lazily at download time
	gidS := strconv.FormatInt(gid, 10)
	modID := mod.ID
	modioResolve := func(rctx context.Context, fid int64) (string, error) {
		var f modioFile
		err := modioGet(rctx, c, key, fmt.Sprintf("/games/%s/mods/%d/files/%d", gidS, modID, fid), &f)
		if err != nil {
			return "", err
		}
		if f.Download.BinaryURL == "" {
			return "", fmt.Errorf("no download url for file %d", fid)
		}
		return f.Download.BinaryURL, nil
	}

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size, Hashes: e.Hashes,
		}
		if e.Section == "files" {
			fid, _ := strconv.ParseInt(e.ID, 10, 64)
			if u := dlByID[fid]; u != "" {
				t.Resolve = func(rctx context.Context) (string, error) {
					return u, nil
				}
			} else {
				t.Resolve = func(rctx context.Context) (string, error) {
					return modioResolve(rctx, fid)
				}
			}
		} else {
			t.DirectURL = e.ID
		}
		return t
	}), nil
}
