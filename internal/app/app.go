// SPDX-License-Identifier: 0BSD

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mod-archiver/internal/core"
	"mod-archiver/internal/source"
)

const toolVersion = "1.0.0"

const defaultUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 mod-archiver/" + toolVersion

type options struct {
	out       string
	jobs      int
	delay     time.Duration
	metaOnly  bool
	images    bool
	videos    bool
	hashMode  string
	timeout   time.Duration
	retries   int
	ua        string
	fresh     bool
	dryRun    bool
	dryCheck  bool
	limit     int
	deps      bool
	depsDepth int
	interval  time.Duration
	color     string
	quiet     bool
	verbose   bool
	listFile  string
	srcHint   string
	gameHint  string
	vFilter   string
	fsURL     string
	nexusKey  string
	nxmKey    string
	nxmExp    string
	cfKey     string
	modioKey  string
	steamUser string
	steamPass string
	steamcmd  string
}

var errHelp = errors.New("help requested")

func envOr(flagVal, env string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(env)
}

func parseFlags(args []string, stderr io.Writer) (*options, []string, error) {
	o := &options{}
	fs := flag.NewFlagSet("mod-archiver", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.out, "out", "./archive", "output directory")
	fs.StringVar(&o.out, "o", "./archive", "output directory (shorthand)")
	fs.IntVar(&o.jobs, "jobs", 2, "concurrent downloads")
	fs.IntVar(&o.jobs, "j", 2, "concurrent downloads (shorthand)")
	fs.DurationVar(&o.delay, "delay", 1200*time.Millisecond, "minimum delay between requests")
	fs.BoolVar(&o.metaOnly, "metadata-only", false, "save only metadata and manifest, no downloads")
	fs.BoolVar(&o.images, "images", true, "archive images")
	fs.BoolVar(&o.videos, "videos", true, "archive videos")
	fs.StringVar(&o.hashMode, "hash", "fast", "hash mode: fast (xxh64 sample + remote hash verify), full (adds sha256), none")
	fs.DurationVar(&o.timeout, "timeout", 90*time.Second, "per request timeout")
	fs.IntVar(&o.retries, "retries", 4, "retries per request")
	fs.StringVar(&o.ua, "ua", defaultUA, "User-Agent header for page requests")
	fs.BoolVar(&o.fresh, "fresh", false, "ignore previous manifest, download everything again")
	fs.BoolVar(&o.dryRun, "dry-run", false, "list what would be downloaded, fetch nothing")
	fs.IntVar(&o.limit, "limit", 0, "cap number of files downloaded per target (0 = all)")
	fs.BoolVar(&o.deps, "deps", false, "also archive declared dependencies (thunderstore, modrinth, curseforge)")
	fs.IntVar(&o.depsDepth, "deps-depth", 2, "how deep -deps recursion goes")
	fs.DurationVar(&o.interval, "interval", 30*time.Minute, "watch loop pause between update passes")
	fs.StringVar(&o.color, "color", "auto", "color output: auto, always, never")
	fs.BoolVar(&o.quiet, "q", false, "only warnings and errors")
	fs.BoolVar(&o.verbose, "v", false, "verbose debug output")
	fs.StringVar(&o.listFile, "list", "", "file with URLs or ids, one per line")
	fs.StringVar(&o.srcHint, "src", "", "source hint: moddb, nexus, steam, curseforge, modrinth")
	fs.StringVar(&o.gameHint, "game", "", "game hint: nexus domain (skyrim), steam appid (4000), curseforge slug")
	fs.StringVar(&o.vFilter, "version-filter", "", "only download files whose version matches this substring")
	fs.StringVar(&o.fsURL, "flaresolverr", "auto", "flaresolverr URL for challenge pages, 'off' to disable (env FLARESOLVERR_URL)")
	fs.StringVar(&o.nexusKey, "nexus-key", "", "nexus mods api key (env NEXUS_API_KEY)")
	fs.StringVar(&o.nxmKey, "nxm-key", "", "nexus free account: key param from an nxm:// link")
	fs.StringVar(&o.nxmExp, "nxm-expires", "", "nexus free account: expires param from an nxm:// link")
	fs.StringVar(&o.cfKey, "cf-key", "", "curseforge api key (env CURSEFORGE_API_KEY)")
	fs.StringVar(&o.modioKey, "modio-key", "", "mod.io api key (env MODIO_API_KEY)")
	fs.StringVar(&o.steamUser, "steam-user", "", "steam login user, default anonymous (env STEAM_USER)")
	fs.StringVar(&o.steamPass, "steam-pass", "", "steam login password (env STEAM_PASS)")
	fs.StringVar(&o.steamcmd, "steamcmd", "", "path to steamcmd binary (env STEAMCMD)")
	ver := fs.Bool("version", false, "print version")

	fs.Usage = func() {
		fmt.Fprintln(stderr, `mod-archiver - archive mods from ModDB, Nexus Mods, Steam Workshop, CurseForge and Modrinth

usage:
  mod-archiver [flags] <target ...>
  mod-archiver [flags] -list targets.txt
  mod-archiver search <query> [-src x] [-game x] [-limit n]
  mod-archiver verify [dir ...]
  mod-archiver check <target ...>
  mod-archiver update [target ...]     # no targets: every archive under -out
  mod-archiver watch [-interval 30m]   # update on a loop until Ctrl-C

search hits every reachable source and prints refs you can paste as
targets. steam needs -game <appid>, curseforge needs -game <slug>,
mod.io needs a key plus -game. gamebanana has no search api.

verify recomputes hashes on disk, check lists what changed upstream
without downloading, update re-runs archived targets, watch repeats
update on -interval.

<target> examples:
  moddb:funny-project               or a moddb.com/mods/... URL
  nexus:skyrim:3863                 or a nexusmods.com/<game>/mods/<id> URL
  steam:4000:3350206986             or a steamcommunity.com sharedfiles URL (?id=)
  cf:minecraft:jei                  or a curseforge.com/<game>/<class>/<slug> URL
  modrinth:sodium                   or a modrinth.com/mod/<slug> URL
  ts:bbepis:BepInExPack             or a thunderstore.io/c/<comm>/p/<ns>/<name>/ URL
  gb:415035                         or a gamebanana.com/mods/<id> URL
  modio:drg:mod-slug                or a mod.io/g/<game>/m/<mod> URL

Bare slugs default to modrinth; add -src to force a source.

flags:`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	if *ver {
		fmt.Fprintf(stderr, "mod-archiver %s\n", toolVersion)
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
	if o.srcHint != "" {
		switch o.srcHint {
		case "moddb", "nexus", "steam", "curseforge", "modrinth", "thunderstore", "gamebanana", "modio":
		default:
			return nil, nil, fmt.Errorf("invalid -src %q", o.srcHint)
		}
	}

	o.nexusKey = envOr(o.nexusKey, "NEXUS_API_KEY")
	o.cfKey = envOr(o.cfKey, "CURSEFORGE_API_KEY")
	o.modioKey = envOr(o.modioKey, "MODIO_API_KEY")
	o.steamUser = envOr(o.steamUser, "STEAM_USER")
	o.steamPass = envOr(o.steamPass, "STEAM_PASS")
	o.steamcmd = envOr(o.steamcmd, "STEAMCMD")
	if o.fsURL == "auto" {
		o.fsURL = os.Getenv("FLARESOLVERR_URL")
		if o.fsURL == "" {
			o.fsURL = "http://localhost:8191" // probed below
		}
	}

	var targets []string
	if o.listFile != "" {
		b, err := os.ReadFile(o.listFile)
		if err != nil {
			return nil, nil, err
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			targets = append(targets, line)
		}
	}
	targets = append(targets, fs.Args()...)
	if len(targets) == 0 {
		fs.Usage()
		return nil, nil, errHelp
	}
	return o, targets, nil
}

// pickSubFlags pulls flags out of args that came after a subcommand.
// Go's flag parser stops at the first positional so these never reach
// the main flagset.
func pickSubFlags(o *options, sopts *source.Options, words []string) []string {
	rest := []string{}
	take := func(i *int) string {
		*i = *i + 1
		if *i < len(words) {
			return words[*i]
		}
		return ""
	}
	for i := 0; i < len(words); i++ {
		w := strings.TrimLeft(words[i], "-")
		name, eq, _ := strings.Cut(w, "=")
		switch name {
		case "out", "o":
			v := eq
			if v == "" {
				v = take(&i)
			}
			o.out = v
		case "src":
			v := eq
			if v == "" {
				v = take(&i)
			}
			o.srcHint = v
		case "game":
			v := eq
			if v == "" {
				v = take(&i)
			}
			sopts.GameHint = v
		case "limit":
			v := eq
			if v == "" {
				v = take(&i)
			}
			if n, err := strconv.Atoi(v); err == nil {
				o.limit = n
			}
		case "interval":
			v := eq
			if v == "" {
				v = take(&i)
			}
			if d, err := time.ParseDuration(v); err == nil {
				o.interval = d
			}
		case "delay":
			v := eq
			if v == "" {
				v = take(&i)
			}
			if d, err := time.ParseDuration(v); err == nil {
				o.delay = d
			}
		case "deps":
			o.deps = true
		case "deps-depth":
			v := eq
			if v == "" {
				v = take(&i)
			}
			if n, err := strconv.Atoi(v); err == nil {
				o.depsDepth = n
			}
		case "q", "quiet":
			o.quiet = true
		case "v", "verbose":
			o.verbose = true
		case "fresh":
			o.fresh = true
		case "metadata-only":
			o.metaOnly = true
		case "dry-run":
			o.dryRun = true
		case "nexus-key":
			sopts.NexusKey = take(&i)
		case "cf-key":
			sopts.CFKey = take(&i)
		case "modio-key":
			sopts.ModioKey = take(&i)
		default:
			rest = append(rest, words[i])
		}
	}
	return rest
}

// runSearch queries every searchable source and prints pasteable refs.
func runSearch(ctx context.Context, client *core.Client, term *core.Term, o *options, sopts *source.Options, words []string) error {
	query := strings.Join(words, " ")
	if query == "" {
		return fmt.Errorf("usage: mod-archiver search <query> [-src name] [-game x] [-limit n]")
	}
	sc := &source.SearchCtx{
		Query: query, Limit: o.limit,
		Client: client, Term: term, Opts: sopts,
	}
	term.Infof("searching %s across %s", term.Bold(query), scopeLabel(o.srcHint))
	results, notes := source.SearchAll(ctx, sc, o.srcHint)
	if len(results) == 0 {
		for _, n := range notes {
			term.Warnf("%s", n)
		}
		return fmt.Errorf("no results")
	}
	lastSrc := ""
	for _, r := range results {
		if r.Source != lastSrc {
			term.Infof("%s", term.Bold(r.Source))
			lastSrc = r.Source
		}
		line := fmt.Sprintf("  %s %-40.40s %-20.20s",
			term.Green(fmt.Sprintf("%-40s", r.Ref)), r.Title, r.Author)
		if r.Game != "" {
			line += " " + term.Dim(fmt.Sprintf("%-14.14s", r.Game))
		}
		if r.Downloads > 0 {
			line += " " + term.Dim(core.HumanCount(r.Downloads)+" dl")
		}
		term.Infof("%s", line)
	}
	for _, n := range notes {
		term.Warnf("  %s", n)
	}
	term.Okf("paste any ref to archive it")
	return nil
}

func scopeLabel(hint string) string {
	if hint == "" {
		return "all sources"
	}
	return hint
}

// runVerify re-checks every done file in the given archives against
// its recorded digests. No network access.
func runVerify(term *core.Term, o *options, args []string) int {
	var dirs []string
	if len(args) == 0 {
		var err error
		if dirs, err = core.ArchiveDirs(o.out); err != nil {
			term.Errf("%v", err)
			return 2
		}
	} else {
		dirs = args
	}
	bad := 0
	for _, d := range dirs {
		rep, err := core.VerifyManifest(d)
		if err != nil {
			term.Errf("%s: %v", d, err)
			bad++
			continue
		}
		for _, p := range rep.Missing {
			term.Errf("  missing: %s", p)
		}
		for _, p := range rep.Bad {
			term.Errf("  corrupt: %s", p)
		}
		if len(rep.Missing)+len(rep.Bad) > 0 {
			bad++
			term.Warnf("%s: %d ok, %d missing, %d corrupt", filepath.Base(d), rep.OK, len(rep.Missing), len(rep.Bad))
		} else {
			term.Okf("%s: %d file(s) verified", filepath.Base(d), rep.OK)
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

// runWatch loops an update pass every -interval until interrupted.
func runWatch(ctx context.Context, client *core.Client, term *core.Term, o *options, sopts *source.Options, targets []string) int {
	for pass := 0; ; pass++ {
		if ctx.Err() != nil {
			return 130
		}
		inputs := targets
		if len(inputs) == 0 {
			dirs, err := core.ArchiveDirs(o.out)
			if err != nil {
				term.Warnf("watch: %v", err)
			} else {
				for _, d := range dirs {
					if m, err := core.LoadManifest(d); err == nil && m.Project != nil && m.Project.Input != "" {
						inputs = append(inputs, m.Project.Input)
					}
				}
			}
		}
		if len(inputs) == 0 {
			term.Warnf("watch: no targets and no archives under %s", o.out)
		}
		visited := map[string]bool{}
		for _, input := range inputs {
			if ctx.Err() != nil {
				break
			}
			visited[strings.ToLower(input)] = true
			if err := archiveTarget(ctx, client, term, o, sopts, input, 0, visited); err != nil &&
				!errors.Is(err, core.ErrInterrupted) {
				term.Warnf("%s: %v", input, err)
			}
		}
		term.Infof("watch pass %d done, next at +%s", pass+1, o.interval)
		select {
		case <-ctx.Done():
			term.Warnf("watch stopped")
			return 130
		case <-time.After(o.interval):
		}
	}
}

// saveOr persists the manifest, warning when the write fails.
func saveOr(term *core.Term, m *core.Manifest) {
	if err := m.Save(); err != nil {
		term.Warnf("manifest save failed: %v", err)
	}
}

// probeFlareSolverr checks that the endpoint is up, returns false when
// it is not reachable so challenge handling falls back to backoff.
func probeFlareSolverr(u string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// #nosec G704 -- the solver endpoint is operator configured
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(u, "/")+"/", nil)
	if err != nil {
		return false
	}
	// #nosec G704 -- same endpoint as above
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode == http.StatusOK
}

var reBareSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Run is the program entry point, returns the process exit code.
func Run(args []string, stdout, stderr *os.File) int {
	o, targets, err := parseFlags(args, stderr)
	if err != nil {
		if errors.Is(err, errHelp) {
			return 0
		}
		return 2
	}

	term := core.NewTerm(stdout, o.color, o.quiet, o.verbose)
	client := core.NewClient(o.ua, o.delay, o.retries, o.timeout, term)

	if o.fsURL != "off" {
		if probeFlareSolverr(o.fsURL) {
			client.SetFlareSolverr(o.fsURL)
			term.Debugf("flaresolverr ready at %s", o.fsURL)
		} else {
			term.Debugf("flaresolverr not reachable at %s", o.fsURL)
		}
	}

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

	sopts := &source.Options{
		HashMode:      o.hashMode,
		Fresh:         o.fresh,
		Images:        o.images,
		Videos:        o.videos,
		MetadataOnly:  o.metaOnly,
		NexusKey:      o.nexusKey,
		NexusNXMKey:   o.nxmKey,
		NexusNXMExp:   o.nxmExp,
		CFKey:         o.cfKey,
		ModioKey:      o.modioKey,
		SteamUser:     o.steamUser,
		SteamPass:     o.steamPass,
		SteamcmdPath:  o.steamcmd,
		GameHint:      o.gameHint,
		VersionFilter: o.vFilter,
	}

	// subcommands. The flag parser stops at the first positional, so
	// trailing flags after the subcommand are picked out by hand.
	switch targets[0] {
	case "search":
		rest := pickSubFlags(o, sopts, targets[1:])
		if err := runSearch(ctx, client, term, o, sopts, rest); err != nil {
			term.Errf("%v", err)
			return 1
		}
		return 0
	case "verify":
		return runVerify(term, o, pickSubFlags(o, sopts, targets[1:]))
	case "check":
		o.dryRun, o.dryCheck = true, true
		targets = pickSubFlags(o, sopts, targets[1:])
	case "update":
		targets = pickSubFlags(o, sopts, targets[1:])
		if len(targets) == 0 {
			dirs, err := core.ArchiveDirs(o.out)
			if err != nil {
				term.Errf("%v", err)
				return 2
			}
			for _, d := range dirs {
				if m, err := core.LoadManifest(d); err == nil && m.Project != nil && m.Project.Input != "" {
					targets = append(targets, m.Project.Input)
				}
			}
		}
	case "watch":
		return runWatch(ctx, client, term, o, sopts, pickSubFlags(o, sopts, targets[1:]))
	}

	term.Infof("%s archiving %d target(s) to %s", term.Bold("mod-archiver "+toolVersion), len(targets), o.out)

	failed := 0
	visited := map[string]bool{}
	for _, target := range targets {
		if ctx.Err() != nil {
			break
		}
		visited[strings.ToLower(target)] = true
		if err := archiveTarget(ctx, client, term, o, sopts, target, 0, visited); err != nil {
			if errors.Is(err, core.ErrInterrupted) || ctx.Err() != nil {
				break
			}
			term.Errf("%s: %v", target, err)
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

func archiveTarget(ctx context.Context, c *core.Client, term *core.Term, o *options, sopts *source.Options, input string, depth int, visited map[string]bool) error {
	// bare slug: default source is modrinth unless hinted otherwise
	src, err := source.Lookup(input, o.srcHint)
	if err != nil {
		if reBareSlug.MatchString(input) && o.srcHint == "" {
			src = source.Modrinth{}
		} else {
			return err
		}
	}
	name := core.SanitizeName(src.Name() + "-" + src.DirName(input))
	dir := filepath.Join(o.out, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	manifest, err := core.LoadManifest(dir)
	if err != nil || o.fresh {
		manifest = core.NewManifest(dir)
	}
	manifest.Version = 2
	manifest.Tool = "mod-archiver/" + toolVersion
	manifest.ScrapedAt = time.Now().UTC()

	cc := &source.CollectCtx{
		Input:    input,
		Dir:      dir,
		Client:   c,
		Term:     term,
		Opts:     sopts,
		Manifest: manifest,
	}
	tasks, err := src.Collect(ctx, cc)
	if manifest.Project != nil {
		manifest.Project.Input = input
	}
	if err != nil {
		saveOr(term, manifest)
		if he := core.IsAuth(err); he {
			return fmt.Errorf("%v (check your api key or login credentials)", err)
		}
		return err
	}
	if o.dryRun {
		var total int64
		var newN, changedN int
		bySection := map[string]int{}
		for _, t := range tasks {
			total += t.Size
			bySection[t.Section]++
			label := "new"
			if t.Entry != nil && t.Entry.Path != "" {
				label = "changed"
				changedN++
			} else {
				newN++
			}
			prefix := "+"
			if o.dryCheck {
				prefix = "  " + label
			}
			term.Infof("%s %-7s %-40s %s", prefix, t.Section, t.Title, core.HumanBytes(t.Size))
		}
		parts := []string{}
		for sec, n := range bySection {
			parts = append(parts, fmt.Sprintf("%d %s", n, sec))
		}
		saveOr(term, manifest)
		switch {
		case o.dryCheck && len(tasks) == 0:
			term.Okf("%s: up to date", name)
		case o.dryCheck:
			term.Okf("%s: %d new, %d changed, %s total (check, nothing downloaded)", name, newN, changedN, core.HumanBytes(total))
		default:
			term.Okf("%s: %s, %s total (dry run, nothing downloaded)", name, strings.Join(parts, ", "), core.HumanBytes(total))
		}
		return nil
	}
	if len(tasks) == 0 {
		term.Okf("%s: nothing to download, archive is complete", name)
		saveOr(term, manifest)
		return nil
	}
	if o.limit > 0 {
		tasks = limitTasks(tasks, o.limit)
	}
	est := int64(0)
	for _, t := range tasks {
		est += t.Size
	}
	term.Infof("%s: %d item(s) to fetch (%s known size) with %d worker(s)", name, len(tasks), core.HumanBytes(est), o.jobs)

	stats := runPool(ctx, c, term, o, manifest, dir, tasks)

	saveOr(term, manifest)
	done, failedN, pending := manifest.Counts()

	// dependency fan-out before reporting the parent result
	if o.deps && depth < o.depsDepth && ctx.Err() == nil {
		if ds, ok := src.(source.DepsSource); ok {
			if refs, derr := ds.Dependencies(ctx, cc); derr != nil {
				term.Warnf("%s: dep lookup failed: %v", name, derr)
			} else {
				for _, ref := range refs {
					key := strings.ToLower(ref)
					if visited[key] || ctx.Err() != nil {
						continue
					}
					visited[key] = true
					term.Infof("%s requires %s", name, term.Green(ref))
					if derr := archiveTarget(ctx, c, term, o, sopts, ref, depth+1, visited); derr != nil &&
						!errors.Is(derr, core.ErrInterrupted) {
						term.Warnf("dep %s: %v", ref, derr)
					}
				}
			}
		}
	}

	switch {
	case ctx.Err() != nil:
		term.Warnf("%s: paused at %d done, %d pending", name, done, pending)
		return core.ErrInterrupted
	case failedN > 0:
		term.Warnf("%s: done=%d failed=%d pending=%d", name, done, failedN, pending)
		return fmt.Errorf("%d item(s) failed", failedN)
	default:
		term.Okf("%s: complete, %d item(s) archived, %s fetched in %s", name, done, core.HumanBytes(stats.bytes), core.HumanDur(stats.took))
		return nil
	}
}

// limitTasks keeps at most n file tasks plus all non-file tasks.
func limitTasks(tasks []*core.Task, n int) []*core.Task {
	files := 0
	out := tasks[:0]
	for _, t := range tasks {
		if t.Section == "files" {
			if files >= n {
				continue
			}
			files++
		}
		out = append(out, t)
	}
	return out
}

type poolStats struct {
	bytes int64
	took  time.Duration
}

// runPool feeds tasks to workers, each rendering progress on its own
// status line. One extra line tracks overall run progress.
func runPool(ctx context.Context, c *core.Client, term *core.Term, o *options, m *core.Manifest, dir string, tasks []*core.Task) poolStats {
	term.Reserve(o.jobs + 1)
	defer term.Release()
	overall := o.jobs

	var doneN, doneBytes int64
	var mu sync.Mutex
	render := func() {
		mu.Lock()
		defer mu.Unlock()
		frac := float64(doneN) / float64(len(tasks))
		term.Set(overall, fmt.Sprintf("%s overall %d/%d items %s %s",
			term.Dim("=="),
			doneN, len(tasks),
			core.RenderBar(14, frac),
			core.HumanBytes(doneBytes)))
	}

	start := time.Now()
	queue := make(chan *core.Task)
	var wg sync.WaitGroup
	for slot := 0; slot < o.jobs; slot++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			for t := range queue {
				if err := os.MkdirAll(t.DestDir, 0o750); err != nil {
					term.Errf("mkdir %s: %v", t.DestDir, err)
					continue
				}
				taskStart := time.Now()
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
						taskStart = now
					}
					var frac float64
					if total > 0 {
						frac = float64(done) / float64(total)
					}
					rate := float64(done-base) / max(now.Sub(taskStart).Seconds(), 0.1)
					term.Set(slot, term.ProgressLine(t.Title, frac, done, rate, total))
				}
				res, err := core.FetchTask(ctx, c, t, o.hashMode, prog)
				term.Set(slot, "")
				applyResult(term, m, dir, t, res, err, time.Since(taskStart))
				mu.Lock()
				doneN++
				if res != nil {
					doneBytes += res.Size
				}
				mu.Unlock()
				render()
				saveOr(term, m)
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
	term.Set(overall, "")
	return poolStats{bytes: doneBytes, took: time.Since(start)}
}

func applyResult(term *core.Term, m *core.Manifest, dir string, t *core.Task, res *core.Result, err error, took time.Duration) {
	m.Lock()
	defer m.Unlock()
	e := t.Entry
	if e == nil {
		return
	}
	switch {
	case errors.Is(err, core.ErrInterrupted):
		term.Debugf("paused: %s", t.Title)
	case err != nil || res == nil:
		e.Status = core.StFailed
		if err == nil {
			err = fmt.Errorf("no result")
		}
		e.Error = err.Error()
		term.Errf("failed: %s: %v", t.Title, err)
	case res.Skipped:
		e.Status = core.StDone
		e.Path = relPath(dir, res.Path)
		e.Size = res.Size
		e.Error = ""
		term.Okf("exists: %s (%s)", t.Title, core.HumanBytes(res.Size))
	default:
		e.Status = core.StDone
		e.Path = relPath(dir, res.Path)
		e.Size = res.Size
		e.FastHash = res.FastHash
		e.SHA256 = res.SHA256
		e.HashOK = res.HashOK
		e.VerifiedHashes = maps.Clone(t.Hashes)
		e.Error = ""
		tag := ""
		if res.HashOK {
			tag = ", hash verified"
		}
		term.Okf("saved: %s (%s in %s%s)", t.Title, core.HumanBytes(res.Size), core.HumanDur(took), tag)
	}
}

func relPath(dir, p string) string {
	if rel, err := filepath.Rel(dir, p); err == nil {
		return rel
	}
	return p
}
