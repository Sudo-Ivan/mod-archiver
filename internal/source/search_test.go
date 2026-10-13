// SPDX-License-Identifier: 0BSD

package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mod-archiver/internal/core"
)

func testSearchCtx(t *testing.T, base string) *SearchCtx {
	t.Helper()
	term := core.NewTerm(os.NewFile(0, os.DevNull), "never", true, false)
	c := core.NewClient("test", 0, 1, 30*time.Second, term)
	return &SearchCtx{Query: "x", Limit: 3, Client: c, Term: term, Opts: &Options{}}
}

func TestSteamSearchPage(t *testing.T) {
	body, err := os.ReadFile("testdata/steam_search.html")
	if err != nil {
		t.Fatal(err)
	}
	res := steamSearchPage(body, "294100", 4)
	if len(res) == 0 {
		t.Fatal("no results parsed from steam browse page")
	}
	for _, r := range res {
		if r.Ref == "" || !strings.HasPrefix(r.Ref, "steam:294100:") {
			t.Fatalf("bad ref %q", r.Ref)
		}
		if r.Title == "" {
			t.Fatal("empty title")
		}
	}
	t.Logf("%d hits, first: %s", len(res), res[0].Title)
}

func TestModrinthSearch(t *testing.T) {
	body, err := os.ReadFile("testdata/modrinth_search.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	old := modrinthAPI
	modrinthAPI = srv.URL
	defer func() { modrinthAPI = old }()

	sc := testSearchCtx(t, srv.URL)
	res, err := Modrinth{}.Search(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("want 3 hits, got %d", len(res))
	}
	if res[0].Ref != "modrinth:sodium" || res[0].Title != "Sodium" {
		t.Fatalf("bad first hit: %+v", res[0])
	}
	if res[0].Downloads < 1_000_000 {
		t.Fatalf("downloads not parsed: %d", res[0].Downloads)
	}
}

func TestNexusSearch(t *testing.T) {
	body, err := os.ReadFile("testdata/nexus_search.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	old := nxGraphQL
	nxGraphQL = srv.URL
	defer func() { nxGraphQL = old }()

	sc := testSearchCtx(t, srv.URL)
	res, err := Nexus{}.Search(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no nexus hits")
	}
	for _, r := range res {
		if !strings.HasPrefix(r.Ref, "nexus:") || len(strings.Split(r.Ref, ":")) != 3 {
			t.Fatalf("bad ref %q", r.Ref)
		}
	}
	t.Logf("%d hits, first: %s", len(res), res[0].Ref)
}

func TestThunderstoreSearch(t *testing.T) {
	body, err := os.ReadFile("testdata/ts_packages.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	old := tsAPI
	tsAPI = srv.URL + "/api/experimental"
	defer func() { tsAPI = old }()

	sc := testSearchCtx(t, srv.URL)
	sc.Query = "mutts"
	res, err := Thunderstore{}.Search(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("no ts hits")
	}
	for _, r := range res {
		if !strings.HasPrefix(r.Ref, "ts:") {
			t.Fatalf("bad ref %q", r.Ref)
		}
		if !strings.Contains(strings.ToLower(r.Title), "mutts") {
			t.Fatalf("non-matching hit %q", r.Title)
		}
	}
}

func TestModDBSearchParse(t *testing.T) {
	fixture := `<div class="row rowcontent"><div class="content">
<h4><a href="/mods/brutal-doom">Brutal Doom</a></h4>
<p class="summary">gore</p>
<span class="right"><a href="/games/doom" title="game"></a></span>
</div></div>
<div class="row rowcontent"><h4><a href="/mods/new">bogus nav</a></h4></div>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture))
	}))
	defer srv.Close()

	old := moddbBase
	moddbBase = srv.URL
	defer func() { moddbBase = old }()

	sc := testSearchCtx(t, srv.URL)
	res, err := ModDB{}.Search(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Ref != "moddb:brutal-doom" {
		t.Fatalf("bad parse: %+v", res)
	}
	if res[0].Title != "Brutal Doom" || res[0].Game != "doom" {
		t.Fatalf("missing fields: %+v", res[0])
	}
}
