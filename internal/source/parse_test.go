// SPDX-License-Identifier: 0BSD

package source

import "testing"

func TestSourceLookup(t *testing.T) {
	cases := []struct {
		in, hint, want string
	}{
		{"moddb:my-mod", "", "moddb"},
		{"https://www.moddb.com/mods/my-mod", "", "moddb"},
		{"nexus:skyrim:3863", "", "nexus"},
		{"https://www.nexusmods.com/skyrim/mods/3863", "", "nexus"},
		{"steam:294100:2009463077", "", "steam"},
		{"https://steamcommunity.com/sharedfiles/filedetails/?id=123", "", "steam"},
		{"cf:minecraft:jei", "", "curseforge"},
		{"https://www.curseforge.com/minecraft/mc-mods/jei", "", "curseforge"},
		{"modrinth:sodium", "", "modrinth"},
		{"https://modrinth.com/mod/sodium", "", "modrinth"},
		{"294100:2009463077", "", "steam"},
		{"anything", "moddb", "moddb"},
	}
	for _, c := range cases {
		s, err := Lookup(c.in, c.hint)
		if err != nil || s.Name() != c.want {
			t.Errorf("Lookup(%q,%q) = %v, want %s", c.in, c.hint, s, c.want)
		}
	}
	if _, err := Lookup("???", ""); err == nil {
		t.Error("expected error for undetectable input")
	}
	if _, err := Lookup("x", "bogus"); err == nil {
		t.Error("expected error for bad hint")
	}
}

func TestNexusRef(t *testing.T) {
	g, id, err := nexusRef("nexus:skyrim:3863", "")
	if err != nil || g != "skyrim" || id != "3863" {
		t.Fatalf("nexus: got %q %q %v", g, id, err)
	}
	g, id, err = nexusRef("https://www.nexusmods.com/skyrimspecialedition/mods/1234", "")
	if err != nil || g != "skyrimspecialedition" || id != "1234" {
		t.Fatalf("url: got %q %q %v", g, id, err)
	}
	g, id, err = nexusRef("3863", "skyrim")
	if err != nil || g != "skyrim" || id != "3863" {
		t.Fatalf("hint: got %q %q %v", g, id, err)
	}
	if _, _, err = nexusRef("3863", ""); err == nil {
		t.Error("expected error without game hint")
	}
}

func TestSteamRef(t *testing.T) {
	a, i, err := steamRef("steam:294100:2009463077", "")
	if err != nil || a != "294100" || i != "2009463077" {
		t.Fatalf("got %q %q %v", a, i, err)
	}
	a, i, err = steamRef("https://steamcommunity.com/sharedfiles/filedetails/?id=3406691833&appid=255710", "")
	if err != nil || a != "255710" || i != "3406691833" {
		t.Fatalf("url: got %q %q %v", a, i, err)
	}
	a, i, err = steamRef("steam:2009463077", "294100")
	if err != nil || a != "294100" || i != "2009463077" {
		t.Fatalf("hint: got %q %q %v", a, i, err)
	}
}

func TestForgeCDNURL(t *testing.T) {
	got := forgecdnURL(4586220, "worldedit-bukkit-7.2.15.jar")
	want := "https://edge.forgecdn.net/files/4586/220/worldedit-bukkit-7.2.15.jar"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// spaces must be escaped or the CDN 403s
	got = forgecdnURL(1234567, "my mod-1.0.jar")
	want = "https://edge.forgecdn.net/files/1234/567/my%20mod-1.0.jar"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDirNames(t *testing.T) {
	cases := []struct {
		src  Source
		in   string
		want string
	}{
		{ModDB{}, "moddb:funny-project", "funny-project"},
		{Modrinth{}, "https://modrinth.com/mod/sodium", "sodium"},
		{Nexus{}, "nexus:skyrim:3863", "skyrim-3863"},
		{Steam{}, "steam:294100:2009463077", "2009463077"},
		{CurseForge{}, "https://www.curseforge.com/minecraft/mc-mods/jei", "jei"},
	}
	for _, c := range cases {
		if got := c.src.DirName(c.in); got != c.want {
			t.Errorf("%s.DirName(%q) = %q, want %q", c.src.Name(), c.in, got, c.want)
		}
	}
}
