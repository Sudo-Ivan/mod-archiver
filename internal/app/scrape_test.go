// SPDX-License-Identifier: 0BSD

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseModPage(t *testing.T) {
	m := ParseModPage(fixture(t, "mod.html"), "funny-project-another-generic-doom-mod")
	if m.Title == "" {
		t.Error("empty title")
	}
	if m.Description == "" {
		t.Error("empty description")
	}
	if m.Game != "Doom II" {
		t.Errorf("game = %q", m.Game)
	}
	if m.ProfileHTML == "" {
		t.Error("profile html not extracted")
	}
	if m.URL != "https://www.moddb.com/mods/funny-project-another-generic-doom-mod" {
		t.Errorf("url = %q", m.URL)
	}
}

func TestParseDownloadsList(t *testing.T) {
	stubs := ParseDownloadsList(fixture(t, "downloads.html"))
	if len(stubs) != 1 {
		t.Fatalf("got %d stubs, want 1", len(stubs))
	}
	st := stubs[0]
	if st.Slug != "funny-project-beta-public-0-1-0" {
		t.Errorf("slug = %q", st.Slug)
	}
	if st.Title != "Funny Project Beta Public 0 1 0" {
		t.Errorf("title = %q", st.Title)
	}
	if st.Category != "Demo" {
		t.Errorf("category = %q", st.Category)
	}
	if st.Added == "" {
		t.Error("empty added")
	}
	if st.PageURL != "https://www.moddb.com/mods/funny-project-another-generic-doom-mod/downloads/funny-project-beta-public-0-1-0" {
		t.Errorf("page url = %q", st.PageURL)
	}
}

func TestParseFilePage(t *testing.T) {
	fi := ParseFilePage(fixture(t, "file.html"))
	if fi.ID != "318765" {
		t.Errorf("id = %q", fi.ID)
	}
	if fi.Filename != "Funny_Project_Beta_Public_0.1.0.rar" {
		t.Errorf("filename = %q", fi.Filename)
	}
	if fi.Size != 27673880 {
		t.Errorf("size = %d", fi.Size)
	}
	if fi.MD5 != "260cee006ba1e47ffaf7eb41282e3430" {
		t.Errorf("md5 = %q", fi.MD5)
	}
	if fi.Uploader != "DotWocht11K" {
		t.Errorf("uploader = %q", fi.Uploader)
	}
	if fi.StartURL != "https://www.moddb.com/downloads/start/318765" {
		t.Errorf("start url = %q", fi.StartURL)
	}
}

func TestParseStartPage(t *testing.T) {
	got := ParseStartPage(fixture(t, "start.html"))
	want := "https://www.moddb.com/downloads/mirror/318765/134/1ebe1d0fe48187066ce936a0e1b9d57f"
	if got != want {
		t.Fatalf("mirror = %q, want %q", got, want)
	}
}

func TestModSlug(t *testing.T) {
	cases := map[string]string{
		"my-mod":                                 "my-mod",
		"mods/my-mod":                            "my-mod",
		"https://www.moddb.com/mods/my-mod":      "my-mod",
		"http://moddb.com/mods/my-mod/downloads": "my-mod",
		"https://www.moddb.com/mods/my-mod?x=1/": "my-mod",
	}
	for in, want := range cases {
		got, err := modSlug(in)
		if err != nil || got != want {
			t.Errorf("modSlug(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := modSlug("https://example.com/??"); err == nil {
		t.Error("expected error for bad slug")
	}
}
