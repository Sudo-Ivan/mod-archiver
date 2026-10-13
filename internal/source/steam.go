// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"mod-archiver/internal/core"
)

// Steam archives Steam Workshop items. Metadata comes from the keyless
// ISteamRemoteStorage Web API. The bytes come from file_url when the
// item is a plain UGC file, otherwise from steamcmd which shells out.
type Steam struct{}

func (Steam) Name() string { return "steam" }

func (Steam) Match(input string) bool {
	host, segs := SplitHost(input)
	if strings.HasPrefix(input, "steam:") {
		return true
	}
	if strings.HasSuffix(host, "steamcommunity.com") && len(segs) >= 1 {
		return strings.Contains(input, "filedetails") || strings.Contains(input, "sharedfiles")
	}
	// bare "appid:itemid" pair
	parts := strings.Split(input, ":")
	if len(parts) == 2 {
		_, a := strconv.ParseUint(parts[0], 10, 64)
		_, b := strconv.ParseUint(parts[1], 10, 64)
		return a == nil && b == nil
	}
	return false
}

func (Steam) DirName(input string) string {
	_, itemid, err := steamRef(input, "")
	if err == nil && itemid != "" {
		return itemid
	}
	return "workshop-item"
}

// steamRef parses input into (appid, itemid). appid may be empty and
// resolved later from item metadata.
func steamRef(input, gameHint string) (appid, itemid string, err error) {
	if strings.HasPrefix(input, "steam:") {
		parts := strings.Split(input[6:], ":")
		switch len(parts) {
		case 1:
			return gameHint, parts[0], nil
		case 2:
			return parts[0], parts[1], nil
		}
		return "", "", fmt.Errorf("steam: want steam:<appid>:<itemid>")
	}
	if u, perr := url.Parse(input); perr == nil && strings.Contains(u.Host, "steamcommunity.com") {
		q := u.Query()
		id := q.Get("id")
		if id == "" {
			return "", "", fmt.Errorf("no id= in workshop url %q", input)
		}
		app := q.Get("appid")
		if app == "" {
			app = gameHint
		}
		return app, id, nil
	}
	parts := strings.Split(input, ":")
	if len(parts) == 2 {
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("cannot parse steam item from %q", input)
}

type publishedFile struct {
	Result          uint8  `json:"result"`
	PublishedFileID string `json:"publishedfileid"`
	Creator         string `json:"creator"`
	ConsumerAppID   uint32 `json:"consumer_appid"`
	FileName        string `json:"filename"`
	FileSize        string `json:"file_size"`
	FileURL         string `json:"file_url"`
	PreviewURL      string `json:"preview_url"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	FileDescription string `json:"file_description"`
	TimeCreated     int64  `json:"time_created"`
	TimeUpdated     int64  `json:"time_updated"`
	FileType        int    `json:"file_type"`
	Subscriptions   int64  `json:"subscriptions"`
	Views           int64  `json:"views"`
	Banned          int    `json:"banned"`
	BanReason       string `json:"ban_reason"`
	Tags            []struct {
		Tag string `json:"tag"`
	} `json:"tags"`
}

type pfResp struct {
	Response struct {
		Result      int             `json:"result"`
		ResultCount int             `json:"resultcount"`
		Details     []publishedFile `json:"publishedfiledetails"`
	} `json:"response"`
}

type collResp struct {
	Response struct {
		Details []struct {
			PublishedFileID string `json:"publishedfileid"`
			Result          int    `json:"result"`
			Children        []struct {
				PublishedFileID string `json:"publishedfileid"`
				SortOrder       int    `json:"sortorder"`
				FileType        int    `json:"filetype"`
			} `json:"children"`
		} `json:"collectiondetails"`
	} `json:"response"`
}

var steamAPI = "https://api.steampowered.com/ISteamRemoteStorage"

func getPublished(ctx context.Context, c *core.Client, ids []string) ([]publishedFile, error) {
	form := url.Values{}
	form.Set("itemcount", strconv.Itoa(len(ids)))
	for i, id := range ids {
		form.Set(fmt.Sprintf("publishedfileids[%d]", i), id)
	}
	var out pfResp
	err := c.PostJSON(ctx, steamAPI+"/GetPublishedFileDetails/v1/", form, nil, nil, &out)
	return out.Response.Details, err
}

// getCollection expands a collection into ordered child item ids.
func getCollection(ctx context.Context, c *core.Client, id string) ([]string, error) {
	form := url.Values{
		"collectioncount":     {"1"},
		"publishedfileids[0]": {id},
	}
	var out collResp
	if err := c.PostJSON(ctx, steamAPI+"/GetCollectionDetails/v1/", form, nil, nil, &out); err != nil {
		return nil, err
	}
	if len(out.Response.Details) == 0 || out.Response.Details[0].Result != 1 {
		return nil, fmt.Errorf("steam: collection %s not found", id)
	}
	var ids []string
	seen := map[string]bool{}
	for _, ch := range out.Response.Details[0].Children {
		if ch.FileType == 2 {
			// nested collection: expand one level deeper
			sub, err := getCollection(ctx, c, ch.PublishedFileID)
			if err == nil {
				for _, sid := range sub {
					if !seen[sid] {
						seen[sid] = true
						ids = append(ids, sid)
					}
				}
			}
			continue
		}
		if !seen[ch.PublishedFileID] {
			seen[ch.PublishedFileID] = true
			ids = append(ids, ch.PublishedFileID)
		}
	}
	return ids, nil
}

// findSteamcmd locates the binary: flag, env, PATH, common dirs.
func findSteamcmd(opt string) (string, error) {
	cands := []string{opt, os.Getenv("STEAMCMD"), "steamcmd", "steamcmd.sh"}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if strings.ContainsRune(c, '/') || strings.ContainsRune(c, os.PathSeparator) {
			// #nosec G703 -- steamcmd path is operator configured
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c, nil
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	for _, p := range []string{"/usr/games/steamcmd", "/opt/steamcmd/steamcmd.sh", "~/steamcmd/steamcmd.sh"} {
		if st, err := os.Stat(os.ExpandEnv(p)); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("steamcmd not found: install it (apt install steamcmd or the linux tarball), or pass -steamcmd /path/to/steamcmd")
}

var reSteamDone = regexp.MustCompile(`Downloaded item \d+ to "([^"]+)"`)

// steamFetch runs steamcmd for one workshop item and copies the result
// into dest.
func steamFetch(ctx context.Context, cc *CollectCtx, appid, itemid, dest string, prog core.ProgressFn) (*core.Result, error) {
	bin, err := findSteamcmd(cc.Opts.SteamcmdPath)
	if err != nil {
		return nil, err
	}
	user, pass := cc.Opts.SteamUser, cc.Opts.SteamPass
	if user == "" {
		user = "anonymous"
	}
	args := []string{
		"+@ShutdownOnFailedCommand", "1",
		"+@NoPromptForPassword", "1",
		"+login", user,
	}
	if pass != "" {
		args = append(args, pass)
	}
	args = append(args, "+workshop_download_item", appid, itemid, "+quit")

	cc.Term.Debugf("steamcmd %s", strings.Join(args, " "))
	const attempts = 3
	var lastErr error
	for range attempts {
		// #nosec G204 -- bin is the steamcmd binary, detected or user configured
		cmd := exec.CommandContext(ctx, bin, args...)
		out, _ := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return nil, core.ErrInterrupted
		}
		s := string(out)
		if m := reSteamDone.FindStringSubmatch(s); m != nil {
			// success: copy into a staging dir then rename so an
			// interrupted copy never looks complete
			src := m[1]
			tmp := dest + ".part"
			_ = os.RemoveAll(tmp)
			if err := copyTree(src, tmp); err != nil {
				_ = os.RemoveAll(tmp)
				return nil, err
			}
			_ = os.RemoveAll(dest)
			if err := os.Rename(tmp, dest); err != nil {
				return nil, err
			}
			size, _ := dirSize(dest)
			return &core.Result{Path: dest, Size: size}, nil
		}
		lastErr = fmt.Errorf("steamcmd failed: %s", lastLine(s))
	}
	return nil, lastErr
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o750)
		}
		// #nosec G304 G122 -- p is inside the steamcmd output tree we own
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304 G306 -- archive file
		if err != nil {
			_ = in.Close()
			return err
		}
		if _, err := out.ReadFrom(in); err != nil {
			_ = in.Close()
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			_ = in.Close()
			return err
		}
		return in.Close()
	})
}

func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return err
	})
	return n, err
}

type wsResult struct {
	PublishedFileID  string `json:"publishedfileid"`
	Title            string `json:"title"`
	ShortDescription string `json:"short_description"`
	ConsumerAppID    uint32 `json:"consumer_appid"`
	FileSize         string `json:"file_size"`
	Subscriptions    int64  `json:"subscriptions"`
	VotesUp          int64  `json:"votes_up"`
}

// steamSearch parses the workshop browse page. Results live inside an
// escaped JSON blob in the page hydration state.
func steamSearchPage(body []byte, appid string, limit int) []SearchResult {
	raw := string(body)
	idx := strings.Index(raw, `\"results\":[`)
	if idx < 0 {
		return nil
	}
	arr := steamExtractEscaped(raw, idx+len(`\"results\":`))
	if arr == "" {
		return nil
	}
	var results []wsResult
	if err := json.Unmarshal([]byte(arr), &results); err != nil {
		return nil
	}
	var out []SearchResult
	for _, r := range results {
		if limit > 0 && len(out) >= limit {
			break
		}
		app := appid
		if r.ConsumerAppID > 0 {
			app = strconv.FormatUint(uint64(r.ConsumerAppID), 10)
		}
		out = append(out, SearchResult{
			Source: "steam", Ref: "steam:" + app + ":" + r.PublishedFileID,
			Title: r.Title, Game: "appid " + app,
			Downloads: r.Subscriptions,
			URL:       "https://steamcommunity.com/sharedfiles/filedetails/?id=" + r.PublishedFileID,
			Summary:   r.ShortDescription,
		})
	}
	return out
}

// steamExtractEscaped walks the hydration blob at raw[i:] which is a
// JSON array embedded one escape level deep. Outer backslash pairs are
// decoded while tracking inner string state so a quote inside a value
// does not end the scan early. Returns the inner JSON array text.
func steamExtractEscaped(raw string, i int) string {
	var b strings.Builder
	depth, inStr, innerEsc := 0, false, false
	emit := func(c byte) bool {
		if inStr {
			if innerEsc {
				innerEsc = false
			} else if c == '"' {
				inStr = false
			} else if c == '\\' {
				innerEsc = true
			}
		} else {
			switch c {
			case '"':
				inStr = true
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
		b.WriteByte(c)
		return depth == 0
	}
	for i < len(raw) {
		c := raw[i]
		if c != '\\' || i+1 >= len(raw) {
			i++
			if emit(c) {
				return b.String()
			}
			continue
		}
		d := raw[i+1]
		switch {
		case d == '"' || d == '\\' || d == '/':
			// decoded char keeps inner JSON string semantics
			i += 2
			if emit(d) {
				return b.String()
			}
		case d == 'u' && i+5 < len(raw):
			// keep the escape for the inner JSON decoder
			for j := i; j < i+6; j++ {
				b.WriteByte(raw[j])
			}
			i += 6
		default:
			// n t r b f and friends: copy the pair verbatim so the
			// inner decoder sees a valid escape
			i += 2
			b.WriteByte('\\')
			b.WriteByte(d)
		}
	}
	return ""
}

// Search scrapes the workshop browse page. Needs -game <appid>.
func (Steam) Search(ctx context.Context, sc *SearchCtx) ([]SearchResult, error) {
	if sc.Opts.GameHint == "" {
		return nil, fmt.Errorf("steam search needs an appid, pass -game <appid>")
	}
	u := fmt.Sprintf("https://steamcommunity.com/workshop/browse/?appid=%s&searchtext=%s&browsesort=textsearch",
		urlQueryEscape(sc.Opts.GameHint), urlQueryEscape(sc.Query))
	body, err := sc.Client.Get(ctx, u, "https://steamcommunity.com/")
	if err != nil {
		return nil, err
	}
	return steamSearchPage(body, sc.Opts.GameHint, sc.Limit), nil
}

func (Steam) Collect(ctx context.Context, cc *CollectCtx) ([]*core.Task, error) {
	appid, itemid, err := steamRef(cc.Input, cc.Opts.GameHint)
	if err != nil {
		return nil, err
	}
	c := cc.Client
	term := cc.Term
	m := cc.Manifest

	term.Infof("%s fetching item %s", term.Bold("steam"), itemid)
	details, err := getPublished(ctx, c, []string{itemid})
	if err != nil {
		return nil, err
	}
	if len(details) == 0 || details[0].Result != 1 {
		return nil, fmt.Errorf("steam: item %s not found", itemid)
	}
	pf := details[0]
	if appid == "" {
		appid = strconv.FormatUint(uint64(pf.ConsumerAppID), 10)
	}
	desc := pf.Description
	if desc == "" {
		desc = pf.FileDescription
	}
	pageURL := "https://steamcommunity.com/sharedfiles/filedetails/?id=" + itemid

	m.Project = &core.Project{
		Source:      "steam",
		ID:          itemid,
		Slug:        itemid,
		Title:       pf.Title,
		Description: desc,
		Author:      pf.Creator,
		Game:        "appid " + appid,
		URL:         pageURL,
		HeaderImage: pf.PreviewURL,
		Extra: map[string]string{
			"consumer_appid": appid,
			"file_type":      strconv.Itoa(pf.FileType),
			"subscriptions":  strconv.FormatInt(pf.Subscriptions, 10),
			"views":          strconv.FormatInt(pf.Views, 10),
		},
	}
	var tags []string
	for _, t := range pf.Tags {
		tags = append(tags, t.Tag)
	}
	if len(tags) > 0 {
		m.Project.Extra["tags"] = strings.Join(tags, ",")
	}
	term.Okf("%s: %s (appid %s)", itemid, pf.Title, appid)
	if pf.Banned != 0 {
		term.Warnf("%s: banned on workshop: %s", pf.Title, pf.BanReason)
	}
	saveOr(term, m)
	if cc.Opts.MetadataOnly {
		return nil, nil
	}

	// collections expand into child items
	itemIDs := []string{itemid}
	if pf.FileType == 2 {
		term.Infof("%s: expanding collection", pf.Title)
		kids, err := getCollection(ctx, c, itemid)
		if err != nil {
			term.Warnf("collection expansion failed: %v", err)
		} else {
			itemIDs = kids
			term.Infof("%d items in collection", len(kids))
		}
	}
	var files []publishedFile
	for start := 0; start < len(itemIDs); start += 50 {
		end := min(start+50, len(itemIDs))
		batch, err := getPublished(ctx, c, itemIDs[start:end])
		if err != nil {
			return nil, err
		}
		files = append(files, batch...)
	}

	for _, f := range files {
		if f.Result != 1 {
			continue
		}
		fs, _ := strconv.ParseInt(f.FileSize, 10, 64)
		e := Upsert(m, "files", f.PublishedFileID)
		e.Title = f.Title
		e.Filename = f.PublishedFileID
		e.SourceURL = "https://steamcommunity.com/sharedfiles/filedetails/?id=" + f.PublishedFileID
		e.Size = fs
		if f.FileName != "" {
			e.Category = f.FileName
		}
		if pf.PreviewURL != "" && f.PublishedFileID == itemid && cc.Opts.Images {
			ie := Upsert(m, "images", pf.PreviewURL)
			ie.Filename = core.NameFromURL(pf.PreviewURL)
			ie.Title = "preview"
			ie.SourceURL = pageURL
		}
	}
	saveOr(term, m)

	// resolve each item to a fetch plan now so tasks are complete
	type plan struct {
		direct string
	}
	plans := map[string]plan{}
	for _, f := range files {
		if f.Result != 1 {
			continue
		}
		if f.FileURL != "" {
			plans[f.PublishedFileID] = plan{direct: f.FileURL}
		}
	}

	return PendingTasks(cc, func(e *core.ItemEntry, destName string) *core.Task {
		t := &core.Task{
			ID: e.ID, Section: e.Section, Title: e.Title,
			DestDir: filepath.Join(cc.Dir, e.Section), Filename: destName,
			PageURL: e.SourceURL, Size: e.Size,
		}
		if e.Section == "images" {
			t.DirectURL = e.ID
			return t
		}
		if p, ok := plans[e.ID]; ok {
			t.Filename = destName
			t.DirectURL = p.direct
			return t
		}
		// steamcmd path: each item lands in its own directory
		aid, iid := appid, e.ID
		t.Filename = iid // dest dir name
		t.Run = func(rctx context.Context, dest string, prog core.ProgressFn) (*core.Result, error) {
			prog(0, -1)
			return steamFetch(rctx, cc, aid, iid, dest, prog)
		}
		return t
	}), nil
}
