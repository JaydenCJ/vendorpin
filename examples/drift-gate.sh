#!/usr/bin/env bash
# vendorpin verify as a local policy gate.
#
# Drop this in a pre-commit or pre-push hook: it exits non-zero the moment
# any vendored tree no longer matches its pin, so an accidental edit under
# vendor/ (or a missing file after a bad merge) can never slip into history
# unnoticed. Verification is digest-based and fully offline.
#
#   bash examples/drift-gate.sh; echo "exit: $?"
set -euo pipefail

if ! command -v vendorpin > /dev/null 2>&1; then
  echo "drift-gate: vendorpin is not on PATH (go build ./cmd/vendorpin)" >&2
  exit 3
fi

if vendorpin verify; then
  echo "drift-gate: all vendored trees match their pins"
else
  status=$?
  echo "drift-gate: vendored trees have drifted." >&2
  echo "  inspect:  vendorpin diff" >&2
  echo "  restore:  vendorpin update --force <name>" >&2
  echo "  adopt:    edit upstream instead, or re-pin deliberately" >&2
  exit "$status"
fi
