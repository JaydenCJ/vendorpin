# The vendorpin.lock format

`vendorpin.lock` is the provenance record for every vendored tree. It is
plain JSON, deliberately boring, and designed to be committed to git and
reviewed like code: entries are sorted, indentation is stable, and two
saves of the same state are byte-identical, so a lockfile diff shows
exactly one thing — a pin that moved.

## Top level

```json
{
  "lockfile_version": 1,
  "generated_by": "vendorpin 0.1.0",
  "vendors": []
}
```

| Key | Meaning |
|---|---|
| `lockfile_version` | Schema version. vendorpin refuses to load any version it does not understand — a provenance record must never be reinterpreted silently. |
| `generated_by` | The tool version that last wrote the file. Informational. |
| `vendors` | One entry per vendored tree, sorted by `name`. |

## Vendor entries

```json
{
  "name": "demo-lib",
  "upstream": "https://example.test/acme/demo-lib",
  "ref": "v1.0.0",
  "commit": "82e1ce5…40 hex…",
  "commit_time": "2026-01-05T10:00:00+00:00",
  "path": "lib",
  "dest": "vendor/demo-lib",
  "tree": "sha256:…64 hex…",
  "files": [
    { "path": "parse.py", "digest": "sha256:…64 hex…", "mode": "644" }
  ]
}
```

| Key | Meaning |
|---|---|
| `name` | Unique handle used on the CLI (`[A-Za-z0-9][A-Za-z0-9._-]*`, ≤100 chars). |
| `upstream` | The git URL or local path the tree came from. |
| `ref` | What the user asked to pin (branch, tag, or commit). Re-resolved by `update`. |
| `commit` | The resolved pin: a full 40-hex commit hash. Never abbreviated. |
| `commit_time` | The upstream commit's author date (git `%aI`). Deterministic — vendorpin records no wall-clock timestamps anywhere. |
| `path` | Subdirectory of the upstream that was vendored; omitted when the whole tree was. |
| `dest` | Where the tree lives, relative to the lockfile. Validated: no absolute paths, no `..`. |
| `tree` | One digest over the whole snapshot (see below). |
| `files` | Per-file records, sorted by path: relative `path`, content `digest`, and `mode` (`644` or `755` — the executable bit is the only permission git itself tracks). |

## Digests

File digests are `"sha256:" + hex(sha256(content))`, lowercase.

The tree digest hashes one line per file, in sorted path order:

```
<mode> <file-digest> <path>\n
```

so any content change, mode flip, rename, addition, or removal changes it.
This pre-image is frozen for `lockfile_version` 1.

## Drift semantics

`status` and `verify` compare the records against the files on disk —
no upstream contact, fully offline. Each pinned file is `ok`, `modified`
(digest differs), `mode` (content identical, executable bit flipped), or
`missing`; any file under `dest` that the pin does not track is `extra`.
A tree is *clean* only when every record is `ok` and nothing extra exists.

## Validation on load

vendorpin validates the whole document on every load and refuses to
proceed on: an unknown `lockfile_version`, duplicate vendor names or file
paths, a commit that is not 40-hex, malformed digests, unknown modes,
unknown JSON fields, and any `dest`/`path`/file path that is absolute,
non-canonical, or contains `..`. Hand edits are welcome; broken ones fail
loudly with the entry name in the error.
