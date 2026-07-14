#!/usr/bin/env bash
# End-to-end smoke test for vendorpin: builds the binary, fabricates a
# deterministic upstream repository with two tagged releases, then walks the
# whole lifecycle — add, status, verify, tamper, diff, update, remove —
# asserting on real CLI output and exit codes. No network, idempotent,
# finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/vendorpin"
UPSTREAM="$WORKDIR/upstream"
PROJ="$WORKDIR/project"

# Isolate git completely from the host user's configuration.
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME="Upstream Dev"
export GIT_AUTHOR_EMAIL="dev@example.test"
export GIT_COMMITTER_NAME="Upstream Dev"
export GIT_COMMITTER_EMAIL="dev@example.test"

commit_on() {
  # commit_on <seq> <message>: stage everything, commit with a pinned date.
  local seq="$1"; shift
  local date
  date="$(printf '2026-01-%02dT10:00:00+00:00' "$seq")"
  git -C "$UPSTREAM" add -A
  GIT_AUTHOR_DATE="$date" GIT_COMMITTER_DATE="$date" \
    git -C "$UPSTREAM" commit -q --no-gpg-sign -m "$*"
}

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/vendorpin) || fail "go build failed"

echo "2. version matches the manifest"
[ "$("$BIN" version)" = "vendorpin 0.1.0" ] || fail "version mismatch"

echo "3. fabricate an upstream with two tagged releases"
git init -q -b main "$UPSTREAM"
mkdir -p "$UPSTREAM/lib/util" "$UPSTREAM/docs"
printf '# demo-lib\n' > "$UPSTREAM/README.md"
printf 'def parse(s):\n    return s.strip()\n' > "$UPSTREAM/lib/parse.py"
printf 'def dedent(s):\n    return s\n' > "$UPSTREAM/lib/util/text.py"
printf '#!/bin/sh\necho check\n' > "$UPSTREAM/lib/run.sh"
chmod +x "$UPSTREAM/lib/run.sh"
commit_on 1 "v1.0.0: initial library"
git -C "$UPSTREAM" tag v1.0.0
printf 'def parse(s):\n    if s is None:\n        raise ValueError("no input")\n    return s.strip()\n' > "$UPSTREAM/lib/parse.py"
printf 'def emit(v):\n    return str(v)\n' > "$UPSTREAM/lib/emit.py"
commit_on 2 "v1.1.0: null guard + emit"
git -C "$UPSTREAM" tag v1.1.0

echo "4. add pins lib/ at v1.0.0 and writes the lockfile"
mkdir -p "$PROJ" && cd "$PROJ"
OUT="$("$BIN" add --name demo-lib --ref v1.0.0 --path lib "$UPSTREAM")"
echo "$OUT" | grep -q "pinned demo-lib @" || fail "add did not confirm the pin"
[ -f vendorpin.lock ] || fail "vendorpin.lock not written"
grep -q '"commit_time": "2026-01-01T10:00:00+00:00"' vendorpin.lock \
  || fail "lockfile missing the pinned upstream commit time"
[ -x vendor/demo-lib/run.sh ] || fail "executable bit lost"

echo "5. status and verify are clean, offline"
"$BIN" status > "$WORKDIR/status.txt"
grep -q "demo-lib.*clean" "$WORKDIR/status.txt" || fail "status not clean"
"$BIN" verify > "$WORKDIR/verify.txt"
grep -q "verify: OK (1 vendor clean, 3 files intact)" "$WORKDIR/verify.txt" \
  || fail "verify not OK"

echo "6. tampering is detected as drift"
printf 'def parse(s):\n    return s\n' > vendor/demo-lib/parse.py
echo "local note" > vendor/demo-lib/NOTES.txt
"$BIN" status > "$WORKDIR/status.txt"
grep -q "drifted (1 modified, 1 extra)" "$WORKDIR/status.txt" || fail "drift not summarized"
if "$BIN" verify > /dev/null; then
  fail "verify should exit 1 on drift"
fi

echo "7. diff shows the exact local edits"
set +e
DIFF="$("$BIN" diff)"
[ $? -eq 1 ] || fail "diff should exit 1 when drift exists"
set -e
echo "$DIFF" | grep -q -- "-    return s.strip()" || fail "diff missing pinned line"
echo "$DIFF" | grep -q -- "+    return s" || fail "diff missing local line"
echo "$DIFF" | grep -q "+local note" || fail "diff missing the extra file"

echo "8. update refuses to clobber drift, then --force re-pins to v1.1.0"
if "$BIN" update --ref v1.1.0 demo-lib > /dev/null 2>&1; then
  fail "update should refuse while drifted"
fi
OUT="$("$BIN" update --ref v1.1.0 --force demo-lib)"
echo "$OUT" | grep -q "+ emit.py" || fail "forced update did not report the added file"
grep -q '"ref": "v1.1.0"' vendorpin.lock || fail "lockfile ref not moved"
grep -q "raise ValueError" vendor/demo-lib/parse.py || fail "files not moved to v1.1.0"

echo "9. the untracked local file survives and still counts as drift"
[ -f vendor/demo-lib/NOTES.txt ] || fail "update deleted an untracked file"
set +e
"$BIN" verify > "$WORKDIR/verify.txt"
[ $? -eq 1 ] || fail "verify should exit 1 while the extra file remains"
set -e
grep -q "drifted (1 extra)" "$WORKDIR/verify.txt" || fail "extra file not reported"

echo "10. remove deletes tracked files, keeps the extra, empties the lockfile"
OUT="$("$BIN" remove demo-lib)"
echo "$OUT" | grep -q "kept vendor/demo-lib/NOTES.txt" \
  || fail "remove did not report the kept file"
[ ! -f vendor/demo-lib/parse.py ] || fail "tracked file survived remove"
[ -f vendor/demo-lib/NOTES.txt ] || fail "remove deleted the untracked file"
[ "$("$BIN" status)" = "no vendors pinned yet" ] || fail "lockfile not emptied"

echo "11. usage errors exit 2"
set +e
"$BIN" status --format yaml > /dev/null 2>&1
[ $? -eq 2 ] || fail "bad --format should exit 2"
set -e

echo "SMOKE OK"
