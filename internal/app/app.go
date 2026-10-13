// SPDX-License-Identifier: 0BSD

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

const toolVersion = "1.0.0"

const defaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 moddb-archiver/" + toolVersion

type options struct {
	out      string
	jobs     int
	delay    time.Duration
	metaOnly bool
	images   bool
	videos   bool
	hashMode string
	timeout  time.Duration
	retries  int
	ua       string
	fresh    bool
	color    string
	quiet    bool
	verbose  bool
	listFile string
}

var errHelp = errors.New("help requested")

func parseFlags(args []string, stderr io.Writer) (*options, []string, error) {
	o := &options{}
	fs := flag.NewFlagSet("moddb-archiver", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.out, "out", "./archive", "output directory")
	fs.StringVar(&o.out, "o", "./archive", "output directory (shorthand)")
	fs.IntVar(&o.jobs, "jobs", 2, "concurrent downloads")
	fs.IntVar(&o.jobs, "j", 2, "concurrent downloads (shorthand)")
	fs.DurationVar(&o.delay, "delay", 1200*time.Millisecond, "minimum delay between requests")
	fs.BoolVar(&o.metaOnly, "metadata-only", false, "save only the mod page and manifest, no downloads")
	fs.BoolVar(&o.images, "images", true, "archive image gallery")
	fs.BoolVar(&o.videos, "videos", true, "archive video gallery")
	fs.StringVar(&o.hashMode, "hash", "fast", "hash mode: fast (xxh64 sample + md5 verify), full (adds sha256), none")
	fs.DurationVar(&o.timeout, "timeout", 90*time.Second, "per request timeout")
	fs.IntVar(&o.retries, "retries", 4, "retries per request")
	fs.StringVar(&o.ua, "ua", defaultUA, "User-Agent header")
	fs.BoolVar(&o.fresh, "fresh", false, "ignore previous manifest, download everything again")
	fs.StringVar(&o.color, "color", "auto", "color output: auto, always, never")
	fs.BoolVar(&o.quiet, "q", false, "only warnings and errors")
	fs.BoolVar(&o.verbose, "v", false, "verbose debug output")
	fs.StringVar(&o.listFile, "list", "", "file with mod URLs or slugs, one per line")
	ver := fs.Bool("version", false, "print version")

	fs.Usage = func() {
		fmt.Fprintln(stderr, `moddb-archiver - archive ModDB mods (files, images, videos, metadata)

usage:
  moddb-archiver [flags] <mod ...>
  moddb-archiver [flags] -list mods.txt

<mod> may be a slug (my-mod), a path (mods/my-mod) or a full URL.

flags:`)
		fs.PrintDefaults()
		fmt.Fprintln(stderr, `
layout per mod:
  archive/<slug>/manifest.json   metadata, hashes, resume state
  archive/<slug>/page.html       raw profile page snapshot
  archive/<slug>/files/          downloads
  archive/<slug>/images/         gallery
  archive/<slug>/videos/         videos

Ctrl-C once saves state and exits; run again to resume.`)
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	if *ver {
		fmt.Fprintf(stderr, "moddb-archiver %s\n", toolVersion)
		return nil, nil, errHelp
	}
	switch o.hashMode {
	case "fast", "full", "none":
	default:
		return nil, nil, fmt.Errorf("invalid -hash %q", o.hashMode)
	}
	switch o.color {
	case "auto", "always", "never":
	default:
		return nil, nil, fmt.Errorf("invalid -color %q", o.color)
	}
	if o.jobs < 1 {
		o.jobs = 1
	}
	if o.jobs > 8 {
		o.jobs = 8
	}

	var mods []string
	if o.listFile != "" {
		b, err := os.ReadFile(o.listFile)
		if err != nil {
			return nil, nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			mods = append(mods, line)
		}
	}
	mods = append(mods, fs.Args()...)
	if len(mods) == 0 {
		fs.Usage()
		return nil, nil, errHelp
	}
	return o, mods, nil
}

var reSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// modSlug normalizes user input into a mod slug.
func modSlug(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://", "http://", "//"} {
		s = strings.TrimPrefix(s, p)
	}
	for _, p := range []string{"www.moddb.com", "moddb.com"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.Trim(s, "/")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	if strings.HasPrefix(s, "mods/") {
		s = s[5:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if !reSlug.MatchString(s) {
		return "", fmt.Errorf("cannot parse mod slug from %q", s)
	}
	return s, nil
}

// Run is the program entry point, returns the process exit code.
func Run(args []string, stdout, stderr *os.File) int {
	o, rawMods, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, errHelp) {
			return 0
		}
		return 2
	}

	var cm colorMode
	switch o.color {
	case "always":
		cm = colorAlways
	case "never":
		cm = colorNever
	}
	term := NewTerm(stdout, cm, o.quiet, o.verbose)
	client := NewClient(o.ua, o.delay, o.retries, o.timeout, term)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigc := make(chan os.Signal, 2)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigc
		term.Warnf("interrupted: finishing open writes, saving state (Ctrl-C again to force quit)")
		cancel()
		<-sigc
		os.Exit(130)
	}()

	var slugs []string
	for _, raw := range rawMods {
		slug, err := modSlug(raw)
		if err != nil {
			term.Errf("%v", err)
			continue
		}
		slugs = append(slugs, slug)
	}
	if len(slugs) == 0 {
		return 2
	}

	term.Infof("%s archiving %d mod(s) to %s", term.Bold("moddb-archiver "+toolVersion), len(slugs), o.out)

	failed := 0
	for _, slug := range slugs {
		if ctx.Err() != nil {
			break
		}
		if err := archiveMod(ctx, client, term, o, slug); err != nil {
			if errors.Is(err, ErrInterrupted) || ctx.Err() != nil {
				break
			}
			term.Errf("%s: %v", slug, err)
			failed++
		}
	}

	if ctx.Err() != nil {
		term.Warnf("run interrupted, rerun the same command to resume")
		return 130
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func archiveMod(ctx context.Context, c *Client, term *Term, o *options, slug string) error {
	dir := filepath.Join(o.out, slug)
	for _, sub := range []string{"", "files", "images", "videos"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}

	manifest, err := LoadManifest(dir)
	if err != nil || o.fresh {
		manifest = &Manifest{path: manifestPath(dir)}
	}
	manifest.Version = 1
	manifest.Tool = "moddb-archiver/" + toolVersion
	manifest.ScrapedAt = time.Now().UTC()

	term.Infof("%s fetching profile", term.Bold(slug))

	body, err := c.Get(ctx, siteBase+"/mods/"+slug, siteBase+"/")
	if err != nil {
		manifest.Save()
		return err
	}
	mod := ParseModPage(body, slug)
	manifest.Mod = mod
	os.WriteFile(filepath.Join(dir, "page.html"), body, 0o644)

	term.Okf("%s: %s", slug, mod.Title)
	if mod.Author != "" || mod.Game != "" {
		term.Infof("  %s by %s", term.Dim(mod.Game), mod.Author)
	}
	if err := manifest.Save(); err != nil {
		term.Warnf("manifest: %v", err)
	}
	if o.metaOnly {
		return nil
	}

	// collect file stubs across listing pages
	stubs, err := collectStubs(ctx, c, slug)
	if err != nil {
		manifest.Save()
		return err
	}
	term.Infof("%s: %d file(s) listed", slug, len(stubs))

	// fetch each file detail page, skipping entries already scraped
	// on a previous run so resumes stay cheap
	for _, st := range stubs {
		if ctx.Err() != nil {
			manifest.Save()
			return ErrInterrupted
		}
		if prev := manifest.findFileBySlug(st.Slug); prev != nil && prev.ID != "" && prev.Filename != "" {
			continue
		}
		fb, err := c.Get(ctx, st.PageURL, mod.URL+"/downloads")
		if err != nil {
			term.Warnf("%s: file page %s: %v", slug, st.Slug, err)
			continue
		}
		fi := ParseFilePage(fb)
		if fi.ID == "" {
			term.Warnf("%s: no download id on %s", slug, st.Slug)
			continue
		}
		e := manifest.findFile(fi.ID)
		if e == nil {
			e = manifest.findFileBySlug(st.Slug)
		}
		if e == nil {
			e = &FileEntry{Status: stPending}
			manifest.Files = append(manifest.Files, e)
		}
		e.ID = fi.ID
		e.Slug, e.Title, e.PageURL = st.Slug, st.Title, st.PageURL
		e.Filename = fi.Filename
		e.Category = firstNonEmpty(fi.Category, st.Category)
		e.Uploader, e.Added = fi.Uploader, firstNonEmpty(fi.Added, st.Added)
		e.Summary = st.Summary
		e.Size = fi.Size
		e.MD5 = fi.MD5
		term.Debugf("  file %s: %s (%s)", fi.ID, fi.Filename, humanBytes(fi.Size))
	}
	if err := manifest.Save(); err != nil {
		term.Warnf("manifest: %v", err)
	}

	// collect media
	if o.images {
		urls, err := collectMedia(ctx, c, slug, "images", false)
		if err != nil {
			term.Warnf("%s: images: %v", slug, err)
		}
		for _, u := range urls {
			if manifest.findMedia(manifest.Images, u) == nil {
				manifest.Images = append(manifest.Images, &MediaEntry{URL: u, Status: stPending})
			}
		}
	}
	if o.videos {
		urls, err := collectMedia(ctx, c, slug, "videos", true)
		if err != nil {
			term.Warnf("%s: videos: %v", slug, err)
		}
		for _, u := range urls {
			if manifest.findMedia(manifest.Videos, u) == nil {
				manifest.Videos = append(manifest.Videos, &MediaEntry{URL: u, Status: stPending})
			}
		}
	}

	// build the task list for everything still pending
	seenNames := map[string]map[string]int{
		"files": {}, "images": {}, "videos": {},
	}
	var tasks []*dlTask
	for _, e := range manifest.Files {
		if e.Status == stDone {
			p := filepath.Join(dir, e.Path)
			if e.Path == "" {
				p = filepath.Join(dir, "files", e.Filename)
			}
			if st, err := os.Stat(p); err == nil && (e.Size == 0 || st.Size() == e.Size) {
				// Cheap identity check: sampled fast hash catches a
				// corrupt or replaced file without reading all of it.
				if e.FastHash == "" {
					continue
				}
				if fh, _, ferr := FastHash(p); ferr == nil && fh == e.FastHash {
					continue
				}
				term.Warnf("%s: %s changed on disk, re-fetching", slug, e.Filename)
			}
			e.Status = stPending
		}
		name := SanitizeName(firstNonEmpty(e.Filename, e.Slug+".bin"))
		name = uniqueName(name, seenNames["files"])
		tasks = append(tasks, &dlTask{
			id: e.ID, title: e.Title, kind: kindFile,
			destDir: filepath.Join(dir, "files"), filename: name,
			pageURL: e.PageURL, startURL: siteBase + "/downloads/start/" + e.ID,
			size: e.Size, md5: e.MD5,
		})
	}
	for _, sec := range []struct {
		name string
		list []*MediaEntry
		sub  string
	}{
		{"images", manifest.Images, "images"},
		{"videos", manifest.Videos, "videos"},
	} {
		for _, e := range sec.list {
			if e.Status == stDone {
				p := filepath.Join(dir, e.Path)
				if st, err := os.Stat(p); err == nil && (e.Size == 0 || st.Size() == e.Size) {
					continue
				}
				e.Status = stPending
			}
			name := uniqueName(SanitizeName(nameFromURL(e.URL)), seenNames[sec.sub])
			tasks = append(tasks, &dlTask{
				id: e.URL, title: name, kind: kindMedia,
				destDir: filepath.Join(dir, sec.sub), filename: name,
				pageURL: mod.URL + "/" + sec.name, mediaURL: e.URL,
			})
		}
	}

	if len(tasks) == 0 {
		term.Okf("%s: nothing to download, archive is complete", slug)
		manifest.Save()
		return nil
	}
	term.Infof("%s: %d item(s) to fetch with %d worker(s)", slug, len(tasks), o.jobs)

	runPool(ctx, c, term, o, manifest, dir, tasks)

	manifest.Save()
	done, failedN, pending := manifest.counts()
	switch {
	case ctx.Err() != nil:
		term.Warnf("%s: paused at %d done, %d pending", slug, done, pending)
		return ErrInterrupted
	case failedN > 0:
		term.Warnf("%s: done=%d failed=%d pending=%d", slug, done, failedN, pending)
		return fmt.Errorf("%d item(s) failed", failedN)
	default:
		term.Okf("%s: complete, %d item(s) archived", slug, done)
		return nil
	}
}

func collectStubs(ctx context.Context, c *Client, slug string) ([]FileStub, error) {
	base := siteBase + "/mods/" + slug + "/downloads"
	body, err := c.Get(ctx, base, siteBase+"/mods/"+slug)
	if err != nil {
		return nil, err
	}
	stubs := ParseDownloadsList(body)
	for p := 2; p <= ParsePageNums(body, "downloads"); p++ {
		b, err := c.Get(ctx, fmt.Sprintf("%s/page/%d", base, p), base)
		if err != nil {
			return stubs, err
		}
		stubs = append(stubs, ParseDownloadsList(b)...)
	}
	return stubs, nil
}

func collectMedia(ctx context.Context, c *Client, slug, section string, video bool) ([]string, error) {
	base := siteBase + "/mods/" + slug + "/" + section
	body, err := c.Get(ctx, base, siteBase+"/mods/"+slug)
	if err != nil {
		return nil, err
	}
	urls := ParseMediaList(body, video)
	for p := 2; p <= ParsePageNums(body, section); p++ {
		b, err := c.Get(ctx, fmt.Sprintf("%s/page/%d", base, p), base)
		if err != nil {
			return urls, err
		}
		urls = append(urls, ParseMediaList(b, video)...)
	}
	return urls, nil
}

// runPool feeds tasks to o.jobs workers, each rendering progress on
// its own status line.
func runPool(ctx context.Context, c *Client, term *Term, o *options, m *Manifest, dir string, tasks []*dlTask) {
	term.Reserve(o.jobs)
	defer term.Release()

	queue := make(chan *dlTask)
	var wg sync.WaitGroup
	for slot := 0; slot < o.jobs; slot++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			for t := range queue {
				start := time.Now()
				var base int64 = -1
				last := time.Time{}
				prog := func(done, total int64) {
					now := time.Now()
					if now.Sub(last) < 150*time.Millisecond && total > 0 && done < total {
						return
					}
					last = now
					if base < 0 {
						base = done
						start = now
					}
					var frac float64
					if total > 0 {
						frac = float64(done) / float64(total)
					}
					rate := float64(done-base) / max(now.Sub(start).Seconds(), 0.1)
					line := fmt.Sprintf("%s %-22.22s %s %5.1f%% %s %s",
						term.paint(ansiBlue, ">>"),
						t.title,
						renderBar(14, frac),
						frac*100,
						humanBytes(done),
						humanRate(rate))
					if total > 0 {
						line += " of " + humanBytes(total)
					}
					term.Set(slot, line)
				}
				res, err := downloadOne(ctx, c, t, o.hashMode, prog)
				term.Set(slot, "")
				applyResult(term, m, dir, t, res, err, o.hashMode, time.Since(start))
				m.Save()
			}
		}(slot)
	}

feed:
	for _, t := range tasks {
		select {
		case queue <- t:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()
}

func applyResult(term *Term, m *Manifest, dir string, t *dlTask, res *dlResult, err error, hashMode string, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	setEntry := func(status, path string, size int64, fh string, e error) {
		if t.kind == kindFile {
			fe := m.findFile(t.id)
			if fe == nil {
				return
			}
			fe.Status, fe.Path, fe.FastHash = status, path, fh
			if fe.Filename == "" {
				fe.Filename = filepath.Base(path)
			}
			if size > 0 {
				fe.Size = size
			}
			if e != nil {
				fe.Error = e.Error()
			} else {
				fe.Error = ""
			}
			if res != nil && status == stDone {
				fe.MD5OK = res.md5OK
			}
			return
		}
		var list []*MediaEntry
		if strings.Contains(t.destDir, "images") {
			list = m.Images
		} else {
			list = m.Videos
		}
		me := m.findMedia(list, t.id)
		if me == nil {
			return
		}
		me.Status, me.Path, me.FastHash = status, path, fh
		if size > 0 {
			me.Size = size
		}
		if e != nil {
			me.Error = e.Error()
		} else {
			me.Error = ""
		}
	}

	switch {
	case errors.Is(err, ErrInterrupted):
		term.Debugf("paused: %s", t.title)
	case err != nil:
		setEntry(stFailed, "", 0, "", err)
		term.Errf("failed: %s: %v", t.title, err)
	case res.skipped:
		setEntry(stDone, relPath(dir, res.path), res.size, res.fastHash, nil)
		term.Okf("exists: %s (%s)", t.title, humanBytes(res.size))
	default:
		if hashMode == "full" {
			if s, herr := SHA256File(res.path); herr == nil {
				if t.kind == kindFile {
					if fe := m.findFile(t.id); fe != nil {
						fe.SHA256 = s
					}
				}
			}
		}
		setEntry(stDone, relPath(dir, res.path), res.size, res.fastHash, nil)
		tag := ""
		if t.md5 != "" && res.md5OK {
			tag = ", md5 verified"
		}
		term.Okf("saved: %s (%s in %s%s)", t.title, humanBytes(res.size), humanDur(took), tag)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func relPath(dir, p string) string {
	if rel, err := filepath.Rel(dir, p); err == nil {
		return rel
	}
	return p
}
