// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mod-archiver/internal/core"
)

const gbAPI = "https://gamebanana.com/apiv11"

// gbProps is the CSV property list we ask the API for. Requesting an
// unknown property makes the whole call 400, so keep this minimal.
const gbProps = "_sName,_sDescription,_aSubmitter,_tsDateAdded,_aFiles,_aPreviewMedia,_sVersion,_aGame,_aCategory,_sProfileUrl"

// GameBanana uses the public apiv11 JSON API. File rows carry size,
// md5 and a download url so this is fully keyless.
type GameBanana struct{}

func (GameBanana) Name() string { return "gamebanana" }

func (GameBanana) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "gb:") || strings.HasPrefix(input, "gamebanana:") {
		return true
	}
	return strings.HasSuffix(host, "gamebanana.com") && len(segs) >= 2
}

func gbRef(input string) (itemtype, id string, err error) {
	for _, p := range []string{"gamebanana:", "gb:"} {
		if strings.HasPrefix(input, p) {
			id = strings.TrimSuffix(input[len(p):], "/")
			if _, nerr := strconv.ParseUint(id, 10, 64); nerr != nil {
				return "", "", fmt.Errorf("gamebanana: want gb:<numeric-mod-id>")
			}
			return "Mod", id, nil
		}
	}
	_, segs := SplitHost(input)
	if len(segs) < 2 {
		return "", "", fmt.Errorf("cannot parse gamebanana url %q", input)
	}
	// gamebanana.com/<type-plural>/<id>
	itemtype, err = gbItemType(segs[0])
	if err != nil {
		return "", "", err
	}
	if _, nerr := strconv.ParseUint(segs[1], 10, 64); nerr != nil {
		return "", "", fmt.Errorf("cannot parse gamebanana id from %q", input)
	}
	return itemtype, segs[1], nil
}

// gbItemType maps url path plurals to API item type names.
func gbItemType(seg string) (string, error) {
	m := map[string]string{
		"mods": "Mod", "maps": "Map", "wips": "Wip", "sounds": "Sound",
		"tools": "Tool", "scripts": "Script", "sprays": "Spray",
		"guis": "Gui", "tutorials": "Tutorial", "questions": "Question",
		"saves": "Save", "skins": "Skin", "texts": "Text",
		"concepts": "Concept", "requests": "Request", "projects": "Project",
		"generators": "Generator", "studios": "Studio", "club": "Club",
		"clubs": "Club", "jams": "Jam", "news": "News", "bugs": "Bug",
		"ideas": "Idea", "reviews": "Review", "threads": "Thread",
		"polls": "Poll", "contests": "Contest",
	}
	t, ok := m[seg]
	if !ok {
		return "", fmt.Errorf("unsupported gamebanana item type %q", seg)
	}
	return t, nil
}

func (GameBanana) DirName(input string) string {
	_, id, err := gbRef(input)
	if err == nil {
		return id
	}
	return "gamebanana-item"
}

type gbItem struct {
	Name        string `json:"_sName"`
	Description string `json:"_sDescription"`
	Version     string `json:"_sVersion"`
	ProfileURL  string `json:"_sProfileUrl"`
	DateAdded   int64  `json:"_tsDateAdded"`
	Game        struct {
		Name string `json:"_sName"`
		URL  string `json:"_sProfileUrl"`
	} `json:"_aGame"`
	Category struct {
		Name string `json:"_sName"`
	} `json:"_aCategory"`
	Submitter struct {
		Name       string `json:"_sName"`
		ProfileURL string `json:"_sProfileUrl"`
	} `json:"_aSubmitter"`
	Files []struct {
		ID        int64  `json:"_idRow"`
		Filename  string `json:"_sFile"`
		Size      int64  `json:"_nFilesize"`
		DLURL     string `json:"_sDownloadUrl"`
		MD5       string `json:"_sMd5Checksum"`
		Analysis  string `json:"_sAnalysisResult"`
		DateAdded int64  `json:"_tsDateAdded"`
	} `json:"_aFiles"`
	PreviewMedia struct {
		Images []struct {
			Type    string `json:"_sType"`
			BaseURL string `json:"_sBaseUrl"`
			File    string `json:"_sFile"`
			File220 string `json:"_sFile220"`
		} `json:"_aImages"`
	} `json:"_aPreviewMedia"`
}

func (GameBanana) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	itemtype, id, err := gbRef(cc.Input)
	if err != nil {
		return nil, err
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest

	term.Infof("%s fetching %s %s", term.Bold("gamebanana"), itemtype, id)
	var it gbItem
	u := fmt.Sprintf("%s/%s/%s?_csvProperties=%s", gbAPI, itemtype, id, gbProps)
	if err := c.GetJSON(ctx, u, "https://gamebanana.com/", nil, &it); err != nil {
		return nil, err
	}
	if it.Name == "" {
		return nil, fmt.Errorf("gamebanana: %s %s not found", itemtype, id)
	}

	pageURL := it.ProfileURL
	if pageURL == "" {
		pageURL = fmt.Sprintf("https://gamebanana.com/%ss/%s", strings.ToLower(itemtype), id)
	}
	m.Project = &core.Project{
		Source:      "gamebanana",
		ID:          id,
		Slug:        id,
		Title:       it.Name,
		Description: it.Description,
		Author:      it.Submitter.Name,
		Game:        it.Game.Name,
		URL:         pageURL,
		Extra: map[string]string{
			"item_type": itemtype,
			"version":   it.Version,
			"added":     time.Unix(it.DateAdded, 0).UTC().Format(time.RFC3339),
			"category":  it.Category.Name,
		},
	}
	term.Okf("%s %s: %s (%s)", itemtype, id, it.Name, it.Game.Name)
	m.Save()
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	for _, f := range it.Files {
		if f.DLURL == "" {
			continue
		}
		e := Upsert(m, "files", strconv.FormatInt(f.ID, 10))
		e.Title = f.Filename
		e.Filename = f.Filename
		e.SourceURL = pageURL
		e.Size = f.Size
		if f.DateAdded > 0 {
			e.Added = time.Unix(f.DateAdded, 0).UTC().Format(time.RFC3339)
		}
		if f.MD5 != "" {
			e.Hashes = map[string]string{"md5": f.MD5}
		}
	}
	if cc.Opts.Images {
		for i, img := range it.PreviewMedia.Images {
			u := img.BaseURL
			if img.File != "" {
				u = strings.TrimSuffix(u, "/") + "/" + img.File
			}
			if u == "" {
				continue
			}
			e := Upsert(m, "images", u)
			e.Filename = core.NameFromURL(u)
			if e.Title == "" {
				e.Title = fmt.Sprintf("screenshot %d", i+1)
			}
			e.SourceURL = pageURL
		}
	}
	m.Save()

	dlByID := map[string]string{}
	for _, f := range it.Files {
		dlByID[strconv.FormatInt(f.ID, 10)] = f.DLURL
	}

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size, Hashes: e.Hashes,
		}
		if e.Section == "files" {
			t.DirectURL = dlByID[e.ID]
		} else {
			t.DirectURL = e.ID
		}
		return t
	}), nil
}
