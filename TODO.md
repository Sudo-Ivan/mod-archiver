# TODO

Pending live tests, both blocked on api keys:

- nexus: implemented (internal/source/nexus.go) but never tested
  against api.nexusmods.com. Needs NEXUS_API_KEY from a Nexus Mods
  account. Check metadata parse, files.json, download_link.json,
  CDN download and resume, X-RL rate headers, and the free account
  nxm key/expires path.
- modio: implemented (internal/source/modio.go) but never tested
  against api.mod.io. Needs MODIO_API_KEY from mod.io/me/access.
  Check name_id slug resolution, modfiles pagination, md5 verify,
  and binary_url expiry re-resolution.

Kaneo sync: tasks prepared in kaneo-tasks.json. The board at
todo.quad4.io was unreachable when this was written. Once it is back:

    node ~/.agents/skills/kaneo-sync/sync.mjs kaneo-tasks.json --dry-run
    node ~/.agents/skills/kaneo-sync/sync.mjs kaneo-tasks.json
