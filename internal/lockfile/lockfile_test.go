// Tests for lockfile parsing, strict validation, deterministic
// serialization, and vendor-name derivation. The lockfile is the
// provenance record, so malformed input must fail loudly — several tests
// here exist purely to pin that promise.
package lockfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testDigest = "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
)

func entry(name string) Entry {
	return Entry{
		Name:       name,
		Upstream:   "https://example.test/acme/" + name,
		Ref:        "v1.0.0",
		Commit:     testCommit,
		CommitTime: "2026-01-05T10:00:00+00:00",
		Path:       "lib",
		Dest:       "vendor/" + name,
		Tree:       testDigest,
		Files:      []FileRecord{{Path: "a.txt", Digest: testDigest, Mode: "644"}},
	}
}

func saveLoad(t *testing.T, l *Lockfile) *Lockfile {
	t.Helper()
	path := filepath.Join(t.TempDir(), Filename)
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return back
}

func TestSaveLoadRoundTripsEveryField(t *testing.T) {
	l := New()
	l.SetEntry(entry("alpha"))
	back := saveLoad(t, l)
	if len(back.Vendors) != 1 {
		t.Fatalf("got %d vendors, want 1", len(back.Vendors))
	}
	got, want := back.Vendors[0], entry("alpha")
	if got.Name != want.Name || got.Upstream != want.Upstream || got.Ref != want.Ref ||
		got.Commit != want.Commit || got.CommitTime != want.CommitTime ||
		got.Path != want.Path || got.Dest != want.Dest || got.Tree != want.Tree ||
		len(got.Files) != 1 || got.Files[0] != want.Files[0] {
		t.Errorf("round trip mangled the entry: %+v", got)
	}
}

func TestSaveSortsVendorsByName(t *testing.T) {
	l := New()
	l.SetEntry(entry("zeta"))
	l.SetEntry(entry("alpha"))
	back := saveLoad(t, l)
	if back.Vendors[0].Name != "alpha" || back.Vendors[1].Name != "zeta" {
		t.Errorf("vendors not sorted: %s, %s", back.Vendors[0].Name, back.Vendors[1].Name)
	}
}

func TestSaveEmitsTrailingNewlineAndCleansUpTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Filename)
	l := New()
	l.SetEntry(entry("alpha"))
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Error("lockfile does not end with a single trailing newline")
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("temp file left behind after Save")
	}
}

func TestSaveIsByteIdenticalAcrossRuns(t *testing.T) {
	// The lockfile is committed to git; nondeterministic serialization
	// would produce phantom diffs in review.
	dir := t.TempDir()
	l := New()
	l.SetEntry(entry("alpha"))
	l.SetEntry(entry("beta"))
	p1, p2 := filepath.Join(dir, "one.lock"), filepath.Join(dir, "two.lock")
	if err := l.Save(p1); err != nil {
		t.Fatal(err)
	}
	if err := l.Save(p2); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if string(b1) != string(b2) {
		t.Error("two saves of the same lockfile differ")
	}
}

// corrupt saves a valid lockfile, applies fn to its JSON document, writes
// it back, and returns the Load error.
func corrupt(t *testing.T, fn func(doc map[string]any)) error {
	t.Helper()
	path := filepath.Join(t.TempDir(), Filename)
	l := New()
	l.SetEntry(entry("alpha"))
	if err := l.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	return err
}

func vendors(doc map[string]any) []any { return doc["vendors"].([]any) }

func TestLoadRejectsUnknownLockfileVersion(t *testing.T) {
	err := corrupt(t, func(doc map[string]any) { doc["lockfile_version"] = 2.0 })
	if err == nil || !strings.Contains(err.Error(), "lockfile_version") {
		t.Errorf("err = %v, want an unsupported-version error", err)
	}
}

func TestLoadRejectsDuplicateVendorNames(t *testing.T) {
	err := corrupt(t, func(doc map[string]any) {
		doc["vendors"] = append(vendors(doc), vendors(doc)[0])
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate vendor name") {
		t.Errorf("err = %v, want a duplicate-name error", err)
	}
}

func TestLoadRejectsMalformedCommitHash(t *testing.T) {
	err := corrupt(t, func(doc map[string]any) {
		vendors(doc)[0].(map[string]any)["commit"] = "abc123" // too short
	})
	if err == nil || !strings.Contains(err.Error(), "40-hex") {
		t.Errorf("err = %v, want a commit-hash error", err)
	}
}

func TestLoadRejectsMalformedFileDigest(t *testing.T) {
	err := corrupt(t, func(doc map[string]any) {
		files := vendors(doc)[0].(map[string]any)["files"].([]any)
		files[0].(map[string]any)["digest"] = "md5:abcdef"
	})
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Errorf("err = %v, want a digest error", err)
	}
}

func TestLoadRejectsEscapingDestPath(t *testing.T) {
	// A lockfile pointing dest outside the repo is exactly the attack a
	// provenance file must not enable.
	err := corrupt(t, func(doc map[string]any) {
		vendors(doc)[0].(map[string]any)["dest"] = "../outside"
	})
	if err == nil || !strings.Contains(err.Error(), "dest") {
		t.Errorf("err = %v, want a dest-path error", err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	err := corrupt(t, func(doc map[string]any) { doc["surprise"] = true })
	if err == nil {
		t.Error("unknown top-level field accepted")
	}
}

func TestLoadMissingFileSatisfiesErrNotExist(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.lock"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestNameFromUpstreamVariants(t *testing.T) {
	cases := map[string]string{
		"https://example.test/acme/libfoo.git": "libfoo",
		"https://example.test/acme/libfoo":     "libfoo",
		"https://example.test/acme/libfoo/":    "libfoo",
		"git@example.test:acme/libfoo.git":     "libfoo",
		"ssh://git@example.test/acme/libfoo":   "libfoo",
		"/srv/git/upstream-lib":                "upstream-lib",
		"../sibling/repo.git":                  "repo",
	}
	for in, want := range cases {
		got, err := NameFromUpstream(in)
		if err != nil || got != want {
			t.Errorf("NameFromUpstream(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNameFromUpstreamRejectsUnusableInput(t *testing.T) {
	for _, in := range []string{"", "///", ".git", "https://"} {
		if got, err := NameFromUpstream(in); err == nil {
			t.Errorf("NameFromUpstream(%q) = %q, want error", in, got)
		}
	}
}

func TestValidateNameRules(t *testing.T) {
	for _, ok := range []string{"libfoo", "lib.foo", "Lib_Foo-2", "a"} {
		if err := ValidateName(ok); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "-lead", ".lead", "has space", "a/b", strings.Repeat("x", 101)} {
		if err := ValidateName(bad); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", bad)
		}
	}
}

func TestRecordsFromSnapshotAreSortedWithDigests(t *testing.T) {
	s := snapshot.New()
	if err := s.Add("z.sh", snapshot.ModeExec, []byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("a.txt", snapshot.ModeRegular, []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	recs := RecordsFromSnapshot(s)
	if len(recs) != 2 || recs[0].Path != "a.txt" || recs[1].Path != "z.sh" {
		t.Fatalf("records not sorted: %+v", recs)
	}
	if recs[0].Digest != testDigest {
		t.Errorf("digest = %s, want %s", recs[0].Digest, testDigest)
	}
	if recs[1].Mode != snapshot.ModeExec {
		t.Errorf("mode = %s, want 755", recs[1].Mode)
	}
}

func TestSetEntryReplacesAndRemoveEntryDeletes(t *testing.T) {
	l := New()
	l.SetEntry(entry("alpha"))
	e := entry("alpha")
	e.Ref = "v2.0.0"
	l.SetEntry(e)
	if len(l.Vendors) != 1 || l.Vendors[0].Ref != "v2.0.0" {
		t.Fatalf("SetEntry did not replace in place: %+v", l.Vendors)
	}
	if !l.RemoveEntry("alpha") {
		t.Fatal("RemoveEntry(alpha) = false")
	}
	if l.RemoveEntry("alpha") {
		t.Fatal("RemoveEntry on absent entry = true")
	}
	if len(l.Vendors) != 0 {
		t.Fatalf("vendors not empty after removal: %+v", l.Vendors)
	}
}
