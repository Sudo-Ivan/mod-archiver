// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mod-archiver/internal/core"
)

var moddbBase = "https://www.moddb.com"

// ModDB scrapes moddb.com mod pages: profile, downloads list, file
// detail pages (which carry the MD5), then resolves downloads through
// the start page -> mirror page -> signed CDN URL chain.
type ModDB struct{}

func (ModDB) Name() string { return "moddb" }

func (ModDB) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasSuffix(host, "moddb.com") && len(segs) >= 2 && segs[0] == "mods" {
		return true
	}
	return strings.HasPrefix(input, "moddb:")
}

func (ModDB) DirName(input string) string {
	if s, err := moddbSlug(input); err == nil {
		return s
	}
	return "moddb-project"
}

var reModDBSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// moddbSlug normalizes user input into a mod slug.
func moddbSlug(s string) (string, error) {
	if strings.HasPrefix(s, "moddb:") {
		s = s[6:]
	} else {
		host, segs := SplitHost(s)
		if strings.HasSuffix(host, "moddb.com") {
			if len(segs) >= 2 && segs[0] == "mods" {
				s = segs[1]
			} else {
				s = ""
			}
		}
	}
	s = strings.Trim(s, "/")
	if !reModDBSlug.MatchString(s) {
		return "", fmt.Errorf("cannot parse mod slug from %q", s)
	}
	return s, nil
}

func (ModDB) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	slug, err := moddbSlug(cc.Input)
	if err != nil {
		return nil, err
	}
	m := cc.Manifest
	c := cc.Client
	term := cc.Term

	term.Infof("%s fetching profile", term.Bold(slug))
	body, err := c.Get(ctx, moddbBase+"/mods/"+slug, moddbBase+"/")
	if err != nil {
		return nil, err
	}
	mod := parseModPage(body, slug)
	m.Project = mod
	if err := os.WriteFile(filepath.Join(cc.Dir, "page.html"), body, 0o600); err != nil {
		term.Warnf("page snapshot: %v", err)
	}

	term.Okf("%s: %s", slug, mod.Title)
	if mod.Author != "" || mod.Game != "" {
		term.Infof("  %s by %s", term.Dim(mod.Game), mod.Author)
	}
	saveOr(term, m)
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	stubs, err := collectModDBStubs(ctx, c, slug)
	if err != nil {
		return nil, err
	}
	term.Infof("%s: %d file(s) listed", slug, len(stubs))

	for _, st := range stubs {
		if ctx.Err() != nil {
			return nil, core.ErrInterrupted
		}
		if prev := m.Find("files", st.Slug); prev != nil && prev.ID != st.Slug && prev.Filename != "" {
			continue
		}
		fb, err := c.Get(ctx, st.PageURL, mod.URL+"/downloads")
		if err != nil {
			term.Warnf("%s: file page %s: %v", slug, st.Slug, err)
			continue
		}
		fi := parseModDBFilePage(fb)
		if fi.ID == "" {
			term.Warnf("%s: no download id on %s", slug, st.Slug)
			continue
		}
		e := Upsert(m, "files", fi.ID)
		e.Title, e.SourceURL = st.Title, st.PageURL
		e.Filename = fi.Filename
		e.Category = firstNonEmpty(fi.Category, st.Category)
		e.Uploader, e.Added = fi.Uploader, firstNonEmpty(fi.Added, st.Added)
		e.Summary = st.Summary
		e.Size = fi.Size
		if fi.MD5 != "" {
			e.Hashes = map[string]string{"md5": fi.MD5}
		}
		e.Error = ""
		term.Debugf("  file %s: %s (%s)", fi.ID, fi.Filename, core.HumanBytes(fi.Size))
	}
	saveOr(term, m)

	if cc.Opts.Images {
		urls, err := collectModDBMedia(ctx, c, slug, "images", false)
		if err != nil {
			term.Warnf("%s: images: %v", slug, err)
		}
		for _, u := range urls {
			e := Upsert(m, "images", u)
			e.Filename = core.NameFromURL(u)
			e.SourceURL = mod.URL + "/images"
		}
	}
	if cc.Opts.Videos {
		urls, err := collectModDBMedia(ctx, c, slug, "videos", true)
		if err != nil {
			term.Warnf("%s: videos: %v", slug, err)
		}
		for _, u := range urls {
			e := Upsert(m, "videos", u)
			e.Filename = core.NameFromURL(u)
			e.SourceURL = mod.URL + "/videos"
		}
	}
	saveOr(term, m)

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size, Hashes: e.Hashes,
		}
		if e.Section == "files" {
			startURL := moddbBase + "/downloads/start/" + e.ID
			t.Resolve = func(rctx context.Context) (string, error) {
				return moddbResolve(rctx, c, startURL, e.SourceURL)
			}
		} else {
			t.DirectURL = e.ID // media entries key by url
		}
		return t
	}), nil
}

var reSearchMod = regexp.MustCompile(`href="(/mods/[a-zA-Z0-9-]+)"`)

var reSearchRow = regexp.MustCompile(`(?s)<h4><a [^>]*>(.*?)</a></h4>`)

var moddbNavSlugs = map[string]bool{
	"new": true, "popular": true, "featured": true, "search": true,
	"listing": true, "site": true,
}

// Search scrapes the site search listing. Rows reuse the rowcontent
// markup from the downloads pages.
func (ModDB) Search(ctx context.Context, sc *SearchCtx) ([]SearchResult, error) {
	u := moddbBase + "/mods?filter=t&sort=rank&order=desc&q=" + urlQueryEscape(sc.Query)
	body, err := sc.Client.Get(ctx, u, moddbBase+"/")
	if err != nil {
		return nil, err
	}
	s := string(body)
	var out []SearchResult
	seen := map[string]bool{}
	locs := reFileRowStart.FindAllStringIndex(s, -1)
	limit := sc.Limit
	if limit <= 0 {
		limit = 10
	}
	for i, loc := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := s[loc[1]:end]
		link := reSearchMod.FindStringSubmatch(chunk)
		if link == nil {
			continue
		}
		slug := strings.TrimPrefix(link[1], "/mods/")
		if moddbNavSlugs[slug] || strings.Contains(slug, "/") || seen[slug] {
			continue
		}
		seen[slug] = true
		r := SearchResult{Source: "moddb", Ref: "moddb:" + slug,
			URL: moddbBase + "/mods/" + slug}
		if h := reSearchRow.FindStringSubmatch(chunk); h != nil {
			r.Title = stripTags(h[1])
		}
		if g := reGameLink.FindStringSubmatch(chunk); g != nil {
			r.Game = g[1]
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

var reGameLink = regexp.MustCompile(`href="/games/([a-zA-Z0-9-]+)"`)

func collectModDBStubs(ctx context.Context, c *core.Client, slug string) ([]modDBFileStub, error) {
	base := moddbBase + "/mods/" + slug + "/downloads"
	body, err := c.Get(ctx, base, moddbBase+"/mods/"+slug)
	if err != nil {
		return nil, err
	}
	stubs := parseModDBDownloadsList(body)
	for p := 2; p <= parsePageNums(body, "downloads"); p++ {
		b, err := c.Get(ctx, fmt.Sprintf("%s/page/%d", base, p), base)
		if err != nil {
			return stubs, err
		}
		stubs = append(stubs, parseModDBDownloadsList(b)...)
	}
	return stubs, nil
}

func collectModDBMedia(ctx context.Context, c *core.Client, slug, section string, video bool) ([]string, error) {
	base := moddbBase + "/mods/" + slug + "/" + section
	body, err := c.Get(ctx, base, moddbBase+"/mods/"+slug)
	if err != nil {
		return nil, err
	}
	urls := parseModDBMediaList(body, video)
	for p := 2; p <= parsePageNums(body, section); p++ {
		b, err := c.Get(ctx, fmt.Sprintf("%s/page/%d", base, p), base)
		if err != nil {
			return urls, err
		}
		urls = append(urls, parseModDBMediaList(b, video)...)
	}
	return urls, nil
}

// moddbResolve walks start page -> mirror page -> signed CDN URL.
func moddbResolve(ctx context.Context, c *core.Client, startURL, referer string) (string, error) {
	body, err := c.Get(ctx, startURL, referer)
	if err != nil {
		return "", fmt.Errorf("download start page: %w", err)
	}
	mirror := parseModDBStartPage(body)
	if mirror == "" {
		return "", fmt.Errorf("no mirror link on %s", startURL)
	}
	cdn, err := c.Resolve(ctx, mirror, startURL)
	if err != nil {
		return "", fmt.Errorf("mirror: %w", err)
	}
	return cdn, nil
}

// --- parsing ---

// modDBFileStub is one row of the downloads list.
type modDBFileStub struct {
	PageURL  string
	Slug     string
	Title    string
	Category string
	Added    string
	Summary  string
	Thumb    string
}

// modDBFileInfo is the detail block of a single download page.
type modDBFileInfo struct {
	ID        string
	Filename  string
	Category  string
	Uploader  string
	Added     string
	Size      int64
	Downloads string
	MD5       string
	StartURL  string
}

var (
	reH1           = regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`)
	reMetaDesc     = regexp.MustCompile(`(?s)<meta name="description" content="([^"]*)"`)
	reAuthor       = regexp.MustCompile(`(?s)itemprop="author"[^>]*>.*?itemprop="name">([^<]+)`)
	reGame         = regexp.MustCompile(`"@id":"https://www\.moddb\.com/games/([^"]+)","name":"([^"]+)"`)
	reHeaderImg    = regexp.MustCompile(`headerbox"[^>]*url\(([^)]+)\)`)
	reH5Row        = regexp.MustCompile(`(?s)<h5>\s*([^<]+?)\s*</h5>\s*<span class="summary">\s*(.*?)\s*</span>`)
	reTime         = regexp.MustCompile(`<time datetime="([^"]+)"`)
	reStart        = regexp.MustCompile(`(?:href="|src=")(?:https://www\.moddb\.com)?/downloads/start/(\d+)`)
	reFileID       = regexp.MustCompile(`/(?:statistics/data/visit/downloads|download/medium)/(\d+)`)
	reMirror       = regexp.MustCompile(`(?:href="|location\.href=")(/downloads/mirror/[^"]+)`)
	reFileRowStart = regexp.MustCompile(`<div class="row rowcontent[^"]*">`)
	reFileLink     = regexp.MustCompile(`href="(/mods/[^"]+/downloads/[a-zA-Z0-9-]+)"(?:\s+title="([^"]*)")?`)
	reH4           = regexp.MustCompile(`(?s)<h4><a [^>]*>(.*?)</a></h4>`)
	reSubhead      = regexp.MustCompile(`(?s)<span class="subheading">\s*(.*?)\s*</span>`)
	reRowP         = regexp.MustCompile(`(?s)<p>(.*?)</p>`)
	reThumb        = regexp.MustCompile(`<img src="(https://media\.moddb\.com/cache/images/downloads/[^"]+)"`)
	rePageNum      = regexp.MustCompile(`href="[^"]*/(downloads|images|videos)/page/(\d+)[^"]*"`)
	reFullImg      = regexp.MustCompile(`(?:href|src)="(https://media\.moddb\.com/images/[^"]+\.(?:png|jpe?g|gif|webp))"`)
	reVideo        = regexp.MustCompile(`(?:href|src)="(https://media\.moddb\.com/[^"]+\.(?:mp4|webm|mov|wmv|avi))"`)
	reSize         = regexp.MustCompile(`\(([\d,]+) bytes\)`)
	reTags         = regexp.MustCompile(`<[^>]+>`)
)

func stripTags(s string) string {
	s = reTags.ReplaceAllString(s, "")
	return strings.TrimSpace(html.UnescapeString(s))
}

// matchDiv finds the end of the div that opens at pos and returns the
// inner markup. Pos must point at an opening "<div" tag.
func matchDiv(s string, pos int) string {
	if pos < 0 || pos+4 > len(s) || s[pos:pos+4] != "<div" {
		return ""
	}
	depth := 1
	i := pos + 4
	for i < len(s) {
		open := strings.Index(s[i:], "<div")
		cl := strings.Index(s[i:], "</div>")
		if cl == -1 {
			return ""
		}
		if open != -1 && open < cl {
			depth++
			i += open + 4
			continue
		}
		depth--
		if depth == 0 {
			gt := strings.IndexByte(s[pos:], '>')
			if gt == -1 {
				return ""
			}
			return s[pos+gt+1 : i+cl]
		}
		i += cl + 6
	}
	return ""
}

func parseModPage(body []byte, slug string) *core.Project {
	s := string(body)
	m := &core.Project{
		Source: "moddb",
		Slug:   slug,
		URL:    moddbBase + "/mods/" + slug,
	}
	if x := reH1.FindStringSubmatch(s); x != nil {
		m.Title = stripTags(x[1])
	}
	if x := reMetaDesc.FindStringSubmatch(s); x != nil {
		m.Description = html.UnescapeString(strings.TrimSpace(x[1]))
	}
	if x := reAuthor.FindStringSubmatch(s); x != nil {
		m.Author = html.UnescapeString(strings.TrimSpace(x[1]))
	}
	if x := reGame.FindStringSubmatch(s); x != nil {
		m.GameURL = moddbBase + "/games/" + x[1]
		m.Game = html.UnescapeString(strings.TrimSpace(x[2]))
	}
	if x := reHeaderImg.FindStringSubmatch(s); x != nil {
		m.HeaderImage = x[1]
	}
	if pos := strings.Index(s, `<div id="profiledescription"`); pos >= 0 {
		profile := matchDiv(s, pos)
		if m.Extra == nil {
			m.Extra = map[string]string{}
		}
		m.Extra["profile_text"] = stripTags(profile)
	}
	return m
}

func parseModDBDownloadsList(body []byte) []modDBFileStub {
	s := string(body)
	var out []modDBFileStub
	seen := map[string]bool{}
	locs := reFileRowStart.FindAllStringIndex(s, -1)
	for i, loc := range locs {
		end := len(s)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := s[loc[1]:end]
		link := reFileLink.FindStringSubmatch(chunk)
		if link == nil {
			continue
		}
		pageURL := link[1]
		if seen[pageURL] {
			continue
		}
		seen[pageURL] = true
		st := modDBFileStub{PageURL: moddbBase + pageURL}
		if i := strings.LastIndex(pageURL, "/"); i >= 0 {
			st.Slug = pageURL[i+1:]
		}
		if link[2] != "" {
			st.Title = html.UnescapeString(link[2])
		}
		if x := reH4.FindStringSubmatch(chunk); x != nil {
			st.Title = stripTags(x[1])
		}
		if x := reSubhead.FindStringSubmatch(chunk); x != nil {
			st.Category = stripTags(x[1])
		}
		if x := reTime.FindStringSubmatch(chunk); x != nil {
			st.Added = x[1]
		}
		if x := reRowP.FindStringSubmatch(chunk); x != nil {
			st.Summary = stripTags(x[1])
		}
		if x := reThumb.FindStringSubmatch(chunk); x != nil {
			st.Thumb = x[1]
		}
		out = append(out, st)
	}
	return out
}

func parsePageNums(body []byte, section string) int {
	max := 0
	for _, m := range rePageNum.FindAllStringSubmatch(string(body), -1) {
		if m[1] != section {
			continue
		}
		if n, err := strconv.Atoi(m[2]); err == nil && n > max {
			max = n
		}
	}
	return max
}

func parseModDBFilePage(body []byte) *modDBFileInfo {
	s := string(body)
	fi := &modDBFileInfo{}
	for _, row := range reH5Row.FindAllStringSubmatch(s, -1) {
		key := strings.TrimSpace(row[1])
		val := stripTags(row[2])
		switch key {
		case "Filename":
			fi.Filename = val
		case "Category":
			fi.Category = val
		case "Uploader":
			fi.Uploader = val
		case "Added":
			if x := reTime.FindStringSubmatch(row[2]); x != nil {
				fi.Added = x[1]
			} else {
				fi.Added = val
			}
		case "Size":
			if x := reSize.FindStringSubmatch(val); x != nil {
				n, _ := strconv.ParseInt(strings.ReplaceAll(x[1], ",", ""), 10, 64)
				fi.Size = n
			}
		case "Downloads":
			fi.Downloads = val
		case "MD5 Hash":
			fi.MD5 = val
		}
	}
	if x := reStart.FindStringSubmatch(s); x != nil {
		fi.StartURL = moddbBase + "/downloads/start/" + x[1]
		fi.ID = x[1]
	} else if x := reFileID.FindStringSubmatch(s); x != nil {
		fi.ID = x[1]
		fi.StartURL = moddbBase + "/downloads/start/" + fi.ID
	}
	return fi
}

func parseModDBStartPage(body []byte) string {
	if x := reMirror.FindStringSubmatch(string(body)); x != nil {
		return moddbBase + x[1]
	}
	return ""
}

func parseModDBMediaList(body []byte, video bool) []string {
	s := string(body)
	re := reFullImg
	if video {
		re = reVideo
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		u := m[1]
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
