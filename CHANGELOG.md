# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-12

### Added

- `add`: pin a branch, tag, or commit of any git upstream (URL or local
  path), copy the whole tree or one `--path` subdirectory into `--dest`,
  and record full provenance in `vendorpin.lock` — upstream, ref, resolved
  40-hex commit, upstream commit time, and a SHA-256 digest per file plus
  one over the whole tree.
- Offline drift detection: `status` (aligned table or stable
  `--format json`, schema v1) and `verify` (exit 1 the moment any vendored
  file is modified, mode-flipped, missing, or joined by an untracked
  extra) — both purely digest-based, no upstream contact.
- `diff`: unified diff of local edits against the pinned content, with
  git-style `mode change` blocks and `/dev/null` sides for added/deleted
  files; contacts the upstream only when pinned content is actually needed.
- `update`: re-pin to a new ref with an added/removed/changed preview,
  `--dry-run`, and a safety gate that refuses to clobber local drift
  without `--force` (which doubles as a restore command).
- `remove`: delete exactly the tracked files, prune empty directories,
  keep local extras, and drop the lockfile entry (`--keep-files` to keep
  the tree).
- Strict lockfile validation on every load (versioned schema, 40-hex
  commits, digest and mode shape, duplicate detection, unknown-field
  rejection) with deterministic, byte-identical serialization.
- Supply-chain guardrails: every path from an upstream archive is
  validated against traversal (`..`, absolute, non-canonical); symbolic
  and hard links are refused outright.
- Runnable examples (`examples/make-demo-upstream.sh`,
  `examples/drift-gate.sh`) and a lockfile format reference
  (`docs/lockfile-format.md`).
- 89 deterministic offline tests (unit + in-process CLI integration
  against fabricated git repositories) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/vendorpin/releases/tag/v0.1.0
