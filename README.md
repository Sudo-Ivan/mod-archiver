# mod-archiver

Archives mods from ModDB, Nexus Mods, Steam Workshop, CurseForge,
Modrinth, Thunderstore, GameBanana and mod.io: metadata, page
snapshots, downloadable files, image galleries and videos. Resumable,
hash verified, no dependencies beyond the Go standard library.

## Install

    make build

Produces bin/mod-archiver. Requires Go 1.27+.

## Usage

    mod-archiver [flags] <target ...>
    mod-archiver [flags] -list targets.txt

Targets may be prefixed source ids or full URLs:

    moddb:funny-project            https://www.moddb.com/mods/funny-project
    nexus:skyrim:3863              https://www.nexusmods.com/skyrim/mods/3863
    steam:294100:2009463077        https://steamcommunity.com/sharedfiles/filedetails/?id=2009463077
    cf:minecraft:jei               https://www.curseforge.com/minecraft/mc-mods/jei
    modrinth:sodium                https://modrinth.com/mod/sodium
    ts:bbepis:BepInExPack          https://thunderstore.io/c/riskofrain2/p/bbepis/BepInExPack/
    ts:ns:name:5.4.2122            pins a specific thunderstore version
    gb:415035                      https://gamebanana.com/mods/415035
    modio:drg:mod-slug             https://mod.io/g/drg/m/mod-slug

Bare slugs default to Modrinth. Add -src to force a source and -game
when a source needs game context (Nexus domain, Steam appid, CurseForge
slug).

Output layout per project:

    archive/<source>-<id>/manifest.json   metadata, hashes, resume state
    archive/<source>-<id>/page.html       ModDB profile snapshot
    archive/<source>-<id>/body.md         Modrinth project body
    archive/<source>-<id>/files/          downloads
    archive/<source>-<id>/images/         gallery
    archive/<source>-<id>/videos/         videos

Interrupt with Ctrl-C: open writes finish, state is saved, exit code
130. Rerun the same command to resume where it stopped.

## Source notes

- moddb: fully keyless. Files resolve through the mirror chain and are
  verified against the site published MD5.
- nexus: needs a personal api key (-nexus-key or NEXUS_API_KEY).
  Free accounts additionally need the key/expires pair from an nxm://
  link the site issues for manual downloads: -nxm-key/-nxm-expires.
- steam: metadata is keyless. File bytes come from file_url when the
  workshop item is a plain UGC file, otherwise steamcmd runs as a
  subprocess (anonymous login; -steam-user/-steam-pass for gated
  items). steamcmd must be installed or passed via -steamcmd.
- curseforge: needs an api key (-cf-key or CURSEFORGE_API_KEY, get one
  at console.curseforge.com). Falls back to a widely circulated public
  key when unset; bring your own for reliability. forgecdn links are
  constructed directly for distribution-restricted files.
- modrinth: fully public API, nothing needed.
- thunderstore: fully public for packages; archives latest version
  (plus a pinned one when ts:ns:name:version is used). Community-wide
  listing endpoints are Cloudflare gated, so full version history is
  not fetched.
- gamebanana: fully keyless via apiv11; verifies files against the
  API published md5. Handles mods, maps, wips and other item types.
- modio: needs an api key (-modio-key or MODIO_API_KEY, get one at
  mod.io/me/access). Download urls are signed and expire, so they are
  re-resolved on every run and retry.

## Flags

    -o DIR             output directory (default ./archive)
    -j N               concurrent downloads (default 2)
    -delay D           minimum gap between requests (default 1.2s)
    -hash MODE         fast (default), full or none
    -metadata-only     metadata and manifest only
    -fresh             ignore previous state
    -color MODE        auto, always, never
    -flaresolverr URL  challenge solver endpoint, off to disable
                       (default: probe http://localhost:8191)

Hash modes: fast verifies the remote published digest (MD5 on ModDB,
GameBanana and mod.io, sha512 on Modrinth, sha1 on CurseForge) and
records a sampled xxHash64
fingerprint for quick identity checks. full additionally records
SHA-256 of the whole file. none skips hashing.

When a page returns a bot challenge the client backs off, or routes
through a local FlareSolverr instance when -flaresolverr is reachable
(clearance cookies and the solver user agent are adopted afterwards).

License: 0BSD.
