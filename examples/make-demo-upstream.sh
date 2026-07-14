#!/usr/bin/env bash
# Fabricates a small local "upstream" repository with two tagged releases,
# so you can try every vendorpin command without touching the network.
#
#   bash examples/make-demo-upstream.sh /tmp/demo-upstream
#   vendorpin add --ref v1.0.0 --path lib /tmp/demo-upstream
#   vendorpin status
#   vendorpin update --ref v1.1.0 demo-upstream
#
# Author dates and identities are pinned, so the repository is identical on
# every machine.
set -euo pipefail

DEST="${1:?usage: make-demo-upstream.sh <directory>}"
[ -e "$DEST" ] && { echo "refusing to overwrite existing $DEST" >&2; exit 1; }

export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME="Upstream Dev"
export GIT_AUTHOR_EMAIL="dev@example.test"
export GIT_COMMITTER_NAME="Upstream Dev"
export GIT_COMMITTER_EMAIL="dev@example.test"

commit_on() {
  local seq="$1"; shift
  local date
  date="$(printf '2026-01-%02dT10:00:00+00:00' "$seq")"
  git -C "$DEST" add -A
  GIT_AUTHOR_DATE="$date" GIT_COMMITTER_DATE="$date" \
    git -C "$DEST" commit -q --no-gpg-sign -m "$*"
}

git init -q -b main "$DEST"
mkdir -p "$DEST/lib/util" "$DEST/docs"
printf '# demo-lib\nA tiny demo library for trying vendorpin.\n' > "$DEST/README.md"
printf 'def parse(s):\n    return s.strip()\n' > "$DEST/lib/parse.py"
printf 'def dedent(s):\n    return s\n' > "$DEST/lib/util/text.py"
printf '#!/bin/sh\necho check\n' > "$DEST/lib/run.sh"
chmod +x "$DEST/lib/run.sh"
printf 'docs live here\n' > "$DEST/docs/index.md"
commit_on 1 "v1.0.0: initial library"
git -C "$DEST" tag v1.0.0

printf 'def parse(s):\n    if s is None:\n        raise ValueError("no input")\n    return s.strip()\n' > "$DEST/lib/parse.py"
printf 'def emit(v):\n    return str(v)\n' > "$DEST/lib/emit.py"
commit_on 2 "v1.1.0: null guard + emit helper"
git -C "$DEST" tag v1.1.0

echo "demo upstream ready at $DEST (tags: v1.0.0, v1.1.0)"
