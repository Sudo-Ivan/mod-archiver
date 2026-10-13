# moddb-archiver

Archives ModDB mods: profile metadata, raw page snapshot, downloadable
files, image gallery and videos. Resumable, integrity checked, no
dependencies beyond the Go standard library.

## Install

    make build

Produces bin/moddb-archiver. Requires Go 1.27+.

## Usage

    moddb-archiver [flags] <mod ...>
    moddb-archiver [flags] -list mods.txt

A mod may be a slug, a path or a full URL:

    moddb-archiver my-mod
    moddb-archiver https://www.moddb.com/mods/my-mod

Output layout per mod:

    archive/<slug>/manifest.json   metadata, hashes, resume state
    archive/<slug>/page.html       raw profile page snapshot
    archive/<slug>/files/          downloaded files
    archive/<slug>/images/         gallery images
    archive/<slug>/videos/         videos

Interrupt with Ctrl-C: open writes finish, state is saved, exit code
130. Rerun the same command to resume where it stopped.

Useful flags:

    -o DIR             output directory (default ./archive)
    -j N               concurrent downloads (default 2)
    -delay D           minimum gap between requests (default 1.2s)
    -hash MODE         fast (default), full or none
    -metadata-only     profile and manifest only, no downloads
    -fresh             ignore previous state
    -color MODE        auto, always, never

Hash modes: fast verifies the site provided MD5 and records a sampled
xxHash64 fingerprint (head, middle and tail blocks) for quick identity
checks. full additionally records SHA-256 of the whole file. none
skips hashing.

License: 0BSD.
