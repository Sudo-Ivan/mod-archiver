// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"crypto/md5"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"mod-archiver/internal/core"
)

// fakeModDB serves the whole moddb chain against httptest: profile,
// downloads list, file detail page, start page and mirror redirect
// into a Range aware CDN.
func fakeModDB(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	md5sum := fmt.Sprintf("%x", md5.Sum(payload))
	slug := "test-mod"

	modPage := `<html><head>
		<meta name="description" content="a test mod">
		<title>x</title>
		<script type="application/ld+json">{"@id":"https://www.moddb.com/games/testgame","name":"Test Game"}</script>
		</head><body>
		<h1>Smoke Test Mod</h1>
		<div itemprop="author"><span itemprop="name">Test Author</span></div>
		<div id="profiledescription"><p>profile body</p></div>
		</body></html>`

	listPage := `<html><body>
		<div class="row rowcontent">
		  <h4><a href="/mods/` + slug + `/downloads/test-file" title="x">Smoke File</a></h4>
		  <span class="subheading">Full Version</span>
		  <time datetime="2024-01-02T00:00:00Z">x</time>
		  <p>a file</p>
		</div>
		</body></html>`

	filePage := `<html><body>
		<h5>Filename</h5><span class="summary">smoke.bin</span>
		<h5>Size</h5><span class="summary">` + fmt.Sprintf("(%d bytes)", len(payload)) + `</span>
		<h5>MD5 Hash</h5><span class="summary">` + md5sum + `</span>
		<h5>Uploader</h5><span class="summary">Tester</span>
		<a href="/downloads/start/4242">Download</a>
		</body></html>`

	startPage := `<html><body>
		<a href="/downloads/mirror/4242/1/deadbeef">mirror</a>
		</body></html>`

	srv := httptest.NewServer(nil)
	mux := http.NewServeMux()
	cdn := srv.URL + "/cdn/blob"

	modHandler := func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/downloads/test-file") {
			fmt.Fprint(w, filePage)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/downloads") {
			fmt.Fprint(w, listPage)
			return
		}
		fmt.Fprint(w, modPage)
	}
	mux.HandleFunc("/mods/"+slug, modHandler)
	mux.HandleFunc("/mods/"+slug+"/", modHandler)
	mux.HandleFunc("/downloads/start/4242", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, startPage)
	})
	mux.HandleFunc("/downloads/mirror/4242/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn, http.StatusFound)
	})
	mux.HandleFunc("/cdn/blob", func(w http.ResponseWriter, r *http.Request) {
		lo := 0
		if rng := r.Header.Get("Range"); rng != "" {
			fmt.Sscanf(rng, "bytes=%d-", &lo)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", lo, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-lo))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[lo:])
			return
		}
		w.Write(payload)
	})
	srv.Config.Handler = mux
	t.Cleanup(srv.Close)
	return srv
}

func quietTerm(t *testing.T) *core.Term {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { devnull.Close() })
	return core.NewTerm(devnull, "never", true, false)
}

func TestModDBEndToEnd(t *testing.T) {
	payload := []byte(strings.Repeat("smoke-payload-", 3000))
	srv := fakeModDB(t, payload)

	old := moddbBase
	moddbBase = srv.URL
	defer func() { moddbBase = old }()

	dir := t.TempDir()
	term := quietTerm(t)
	client := core.NewClient("test", 0, 2, 10*time.Second, term)
	m := core.NewManifest(dir)
	cc := &CollectCtx{
		Input: "moddb:test-mod", Dir: dir, Client: client, Term: term,
		Opts: &Options{Images: false, Videos: false}, Manifest: m,
	}
	tasks, err := (ModDB{}).Collect(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if m.Project == nil || m.Project.Title != "Smoke Test Mod" {
		t.Fatalf("project = %+v", m.Project)
	}

	res, err := core.FetchTask(context.Background(), client, tasks[0], "fast", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HashOK {
		t.Fatal("md5 not verified")
	}
	got, _ := os.ReadFile(res.Path)
	if len(got) != len(payload) {
		t.Fatalf("got %d bytes", len(got))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("byte %d differs", i)
		}
	}

	// second collect must produce zero tasks (entry done + file exists)
	tasks[0].Entry.Status = core.StDone
	tasks[0].Entry.Path = "files/smoke.bin"
	tasks[0].Entry.FastHash = res.FastHash
	tasks2, err := (ModDB{}).Collect(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks2) != 0 {
		t.Fatalf("re-collect produced %d tasks", len(tasks2))
	}
}
