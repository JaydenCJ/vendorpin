# vendorpin examples

Two runnable scripts, both offline and self-contained.

## make-demo-upstream.sh

Fabricates a local git repository with two tagged releases (`v1.0.0`,
`v1.1.0`), a `lib/` subdirectory, a nested module, and an executable
script — everything needed to exercise `add`, `status`, `verify`, `diff`,
`update`, and `remove` without a network connection.

```bash
bash examples/make-demo-upstream.sh /tmp/demo-upstream
vendorpin add --name demo-lib --ref v1.0.0 --path lib /tmp/demo-upstream
vendorpin status
echo "edit" >> vendor/demo-lib/parse.py && vendorpin diff
vendorpin update --ref v1.1.0 --force demo-lib
```

Author dates and identities are pinned, so the repository — and therefore
every commit hash and lockfile digest — is identical on every machine.

## drift-gate.sh

Shows `vendorpin verify` as a policy gate: it exits non-zero when any
vendored tree no longer matches its pin, so it can back a pre-commit hook
or any local automation. Drift checking reads only the lockfile and the
files on disk — no upstream contact, no network.

```bash
bash examples/drift-gate.sh; echo "exit: $?"
```
