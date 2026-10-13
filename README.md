# mod-archiver

Downloads mods and their metadata from eight sites into one tidy
archive layout. Pure Go 1.27+, zero dependencies, works on Linux,
macOS and Windows.

Supported: ModDB, Nexus Mods, Steam Workshop (via steamcmd),
CurseForge, Modrinth, Thunderstore, GameBanana, mod.io.

    make build         # -> bin/mod-archiver

## Use it

    mod-archiver [flags] <target ...>
    mod-archiver [flags] -list targets.txt

A target is a site prefix or a full URL:

    moddb:funny-project            https://www.moddb.com/mods/funny-project
    nexus:skyrim:3863              https://www.nexusmods.com/skyrim/mods/3863
    steam:294100:2009463077        https://steamcommunity.com/sharedfiles/filedetails/?id=2009463077
    cf:minecraft:jei               https://www.curseforge.com/minecraft/mc-mods/jei
    modrinth:sodium                https://modrinth.com/mod/sodium
    ts:bbepis:BepInExPack          https://thunderstore.io/c/riskofrain2/p/bbepis/BepInExPack/
    ts:ns:name:5.4.2122            pin a specific thunderstore version
    gb:415035                      https://gamebanana.com/mods/415035
    modio:drg:mod-slug             https://mod.io/g/drg/m/mod-slug

Bare slugs default to Modrinth. `-src` forces a source, `-game`
supplies the game where the source needs one (Nexus domain, Steam
appid, CurseForge slug).

Each target lands in `archive/<source>-<id>/`:

    manifest.json   metadata, hashes and per-item state
    files/          downloaded mod files
    images/         icons, screenshots, gallery
    videos/         video embeds
    page.html       ModDB profile snapshot

Ctrl-C once: in-flight writes close, state saves, exit 130. Run the
same command again and it picks up where it left off. Press Ctrl-C
twice to force quit.

Useful flags:

    -o DIR             output dir (default ./archive)
    -j N               concurrent downloads (default 2)
    -limit N           only fetch the first N files per target
    -dry-run           list what would be fetched, then stop
    -metadata-only     metadata + manifest, skip downloads
    -hash MODE         fast | full | none (default fast)
    -delay D           min gap between requests (default 1.2s)
    -fresh             drop saved state, refetch
    -color MODE        auto | always | never
    -flaresolverr URL  challenge solver (default probes :8191, off disables)

Hashing: fast verifies the site's published digest (md5 on ModDB,
GameBanana, mod.io; sha512 on Modrinth; sha1 on CurseForge) and records
a sampled xxHash64 fingerprint so reruns can spot corrupt or changed
files without rereading gigabytes. full adds sha256.

If a page answers with a bot challenge the client slows down or hands
off to a local FlareSolverr, then adopts the clearance cookie and user
agent so later requests pass on their own.

## Auth

| source | key needed? |
|---|---|
| moddb, modrinth, thunderstore, gamebanana | no |
| steam | no for metadata; steamcmd anonymous login for downloads, `-steam-user/-steam-pass` for gated items |
| nexus | `NEXUS_API_KEY` or `-nexus-key`; free accounts also need `-nxm-key/-nxm-expires` from an nxm:// link |
| curseforge | `CURSEFORGE_API_KEY` or `-cf-key`; falls back to a shared public key |
| mod.io | `MODIO_API_KEY` or `-modio-key` |

## Caveats

- Nexus free accounts: the site mints `nxm://` links for manual
  downloads. Pass its `key` and `expires` query values via
  `-nxm-key`/`-nxm-expires`.
- Thunderstore archives the latest version, or one pinned version.
  Full version history needs the whole community index, so it is
  skipped on purpose.
- Steam items without a `file_url` go through steamcmd. Install it
  (`apt install steamcmd` or the tarball) or pass `-steamcmd /path`.
- CurseForge CDN links are constructed directly for mods that disable
  third-party distribution, respecting `allowModDistribution=false`
  in spirit by still requiring your own api key.

License: 0BSD.
