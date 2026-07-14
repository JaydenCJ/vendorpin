# Contributing to vendorpin

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22 and git ≥2.20; nothing else.

```bash
git clone https://github.com/JaydenCJ/vendorpin && cd vendorpin
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, fabricates a deterministic upstream
repository with two tagged releases in a temp dir, and walks the whole
lifecycle — add, verify, tamper, diff, update, remove — asserting on real
CLI output; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (89 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (digests, drift, and diff never shell out — only `gitio` does).

## Ground rules

- Keep dependencies at zero; adding one needs strong justification in the PR.
- No network calls except the user's own upstream during `add`, `update`,
  and `diff` — never at startup, never for `status`/`verify`. No telemetry.
- The lockfile format is a contract: any pre-image or schema change bumps
  `lockfile_version` and updates `docs/lockfile-format.md` in the same PR.
- Path handling is security-sensitive: everything written to disk goes
  through `snapshot.ValidatePath`, with tests for each rejection.
- Code comments and doc comments are written in English.
- Determinism first: identical input must produce byte-identical output,
  including the lockfile and all orderings.

## Reporting bugs

Include the output of `vendorpin version`, the full command you ran, the
relevant `vendorpin.lock` entry (redact private upstream URLs if needed),
and — for drift misreports — the output of `vendorpin status --format json`,
since that is exactly what the detector computed.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
