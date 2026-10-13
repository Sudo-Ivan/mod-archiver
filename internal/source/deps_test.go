// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"mod-archiver/internal/core"
)

func TestTSDepRef(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"RiskofThunder-BepInEx_GUI-3.0.1", "ts:RiskofThunder:BepInEx_GUI", true},
		{"bbepis-BepInExPack-5.4.2122", "ts:bbepis:BepInExPack", true},
		{"ns-name-with-dash-1.0.0", "ts:ns:name-with-dash", true},
		{"nopackage", "", false},
		{"ns-", "", false},
		{"-", "", false},
	} {
		got, ok := tsDepRef(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("tsDepRef(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestThunderstoreDeps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"latest":{"version_number":"1.0.0","dependencies":["NS-One-1.0.0","Two-Pack-X-2.0.0","baddep"]}}`))
	}))
	defer srv.Close()
	old := tsAPI
	tsAPI = srv.URL
	defer func() { tsAPI = old }()

	term := core.NewTerm(nullFile(t), "never", true, false)
	cc := &CollectCtx{Input: "ts:ns:pkg", Client: testClient(t, term), Term: term, Opts: &Options{}}
	deps, err := Thunderstore{}.Dependencies(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 2 || deps[0] != "ts:NS:One" || deps[1] != "ts:Two:Pack-X" {
		t.Fatalf("bad deps: %v", deps)
	}
}

func TestModrinthDeps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":"v1","version_number":"1.0","name":"one","dependencies":[
			{"project_id":"ABC","dependency_type":"required"},
			{"project_id":"OPT","dependency_type":"optional"},
			{"project_id":"ABC","dependency_type":"required"},
			{"version_id":"VVV","dependency_type":"required"}
		]},{"id":"v2","version_number":"2.0","name":"two","dependencies":[
			{"project_id":"NEW","dependency_type":"required"}]}]`))
	}))
	defer srv.Close()
	old := modrinthAPI
	modrinthAPI = srv.URL
	defer func() { modrinthAPI = old }()

	term := core.NewTerm(nullFile(t), "never", true, false)
	cc := &CollectCtx{Input: "modrinth:slug", Client: testClient(t, term), Term: term, Opts: &Options{}}
	deps, err := Modrinth{}.Dependencies(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	// deduped, required only, version-only dep dropped (no project id)
	want := []string{"modrinth:ABC", "modrinth:NEW"}
	if len(deps) != len(want) {
		t.Fatalf("got %v", deps)
	}
	for i := range want {
		if deps[i] != want[i] {
			t.Fatalf("dep %d = %q want %q", i, deps[i], want[i])
		}
	}

	// version filter scopes the dep list
	cc.Opts.VersionFilter = "1.0"
	deps, err = Modrinth{}.Dependencies(context.Background(), cc)
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0] != "modrinth:ABC" {
		t.Fatalf("filtered deps: %v", deps)
	}
}

// Republished remote digest flips a done entry back to pending.
func TestPendingTasksDetectsRepublish(t *testing.T) {
	dir := t.TempDir()
	m := core.NewManifest(dir)
	m.Version = 2
	e := &core.ItemEntry{
		ID: "1", Section: "files", Filename: "a.bin",
		Status: core.StDone, Path: "files/a.bin",
		Hashes:         map[string]string{"md5": "new"},
		VerifiedHashes: map[string]string{"md5": "old"},
	}
	m.Items = append(m.Items, e)
	term := core.NewTerm(nullFile(t), "never", true, false)
	cc := &CollectCtx{Dir: dir, Term: term, Opts: &Options{}, Manifest: m}
	tasks := PendingTasks(cc, func(e *core.ItemEntry, dest string) *core.Task {
		return &core.Task{Title: e.Title, DestDir: dir, Filename: dest, Entry: e}
	})
	if len(tasks) != 1 {
		t.Fatal("republished hash should mark the entry pending")
	}
}

// Unchanged remote digest keeps a done entry skipped.
func TestPendingTasksSameHash(t *testing.T) {
	dir := t.TempDir()
	m := core.NewManifest(dir)
	m.Version = 2
	e := &core.ItemEntry{
		ID: "1", Section: "files", Filename: "a.bin",
		Status: core.StDone, Path: "files/a.bin",
		Hashes:         map[string]string{"md5": "same"},
		VerifiedHashes: map[string]string{"md5": "same"},
	}
	m.Items = append(m.Items, e)
	// file must exist for the done check to pass
	writeFile(t, dir+"/files/a.bin", []byte("x"))
	term := core.NewTerm(nullFile(t), "never", true, false)
	cc := &CollectCtx{Dir: dir, Term: term, Opts: &Options{}, Manifest: m}
	if tasks := PendingTasks(cc, nil); len(tasks) != 0 {
		t.Fatalf("unchanged hash should stay done, got %d tasks", len(tasks))
	}
}
