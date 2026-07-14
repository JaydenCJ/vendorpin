// Tests for the snapshot fileset: path validation (the supply-chain
// boundary), digest determinism, disk round-trips, and tracked-file
// removal with directory pruning. Everything runs in t.TempDir().
package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustAdd(t *testing.T, s *Snapshot, path, mode, data string) {
	t.Helper()
	if err := s.Add(path, mode, []byte(data)); err != nil {
		t.Fatalf("Add(%q): %v", path, err)
	}
}

func TestValidatePathAcceptsNestedRelativePaths(t *testing.T) {
	for _, p := range []string{"a", "a/b", "a/b/c.txt", "with space/x", ".hidden", "a/.hidden"} {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
}

func TestValidatePathRejectsEscapesAndNonCanonicalForms(t *testing.T) {
	// Each of these could write outside the destination tree or mean
	// different things on different machines — all must be rejected.
	for _, p := range []string{
		"", "/etc/passwd", "../x", "a/../b", "a/..", "./a", "a/./b",
		"a//b", "a/", `a\b`, "a/\x00b", "..",
	} {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want error", p)
		}
	}
}

func TestAddRejectsDuplicatesBadModesAndBadPaths(t *testing.T) {
	s := New()
	mustAdd(t, s, "a.txt", ModeRegular, "x")
	if err := s.Add("a.txt", ModeRegular, []byte("y")); err == nil {
		t.Error("duplicate path accepted")
	}
	if err := s.Add("b.txt", "777", []byte("y")); err == nil {
		t.Error("unknown mode accepted")
	}
	if err := s.Add("../evil", ModeRegular, []byte("y")); err == nil {
		t.Error("escaping path accepted")
	}
}

func TestFileDigestMatchesKnownSHA256Vector(t *testing.T) {
	// sha256("hello\n") — a fixed vector so the digest algorithm can never
	// silently change without breaking this test.
	want := "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if got := FileDigest([]byte("hello\n")); got != want {
		t.Errorf("FileDigest = %s, want %s", got, want)
	}
}

func TestTreeDigestIsIndependentOfInsertionOrder(t *testing.T) {
	a := New()
	mustAdd(t, a, "x/1.txt", ModeRegular, "one")
	mustAdd(t, a, "y/2.txt", ModeExec, "two")
	b := New()
	mustAdd(t, b, "y/2.txt", ModeExec, "two")
	mustAdd(t, b, "x/1.txt", ModeRegular, "one")
	if a.TreeDigest() != b.TreeDigest() {
		t.Errorf("tree digest depends on insertion order: %s vs %s", a.TreeDigest(), b.TreeDigest())
	}
}

func TestTreeDigestChangesOnContentModeAndRename(t *testing.T) {
	base := func() *Snapshot {
		s := New()
		mustAdd(t, s, "a.txt", ModeRegular, "same")
		return s
	}
	orig := base().TreeDigest()

	content := New()
	mustAdd(t, content, "a.txt", ModeRegular, "different")
	mode := New()
	mustAdd(t, mode, "a.txt", ModeExec, "same")
	rename := New()
	mustAdd(t, rename, "b.txt", ModeRegular, "same")

	for name, s := range map[string]*Snapshot{"content": content, "mode": mode, "rename": rename} {
		if s.TreeDigest() == orig {
			t.Errorf("%s change did not alter the tree digest", name)
		}
	}
}

func TestWriteDirReadDirRoundTripsContentAndExecBit(t *testing.T) {
	dir := t.TempDir()
	s := New()
	mustAdd(t, s, "bin/run.sh", ModeExec, "#!/bin/sh\necho hi\n")
	mustAdd(t, s, "lib/a.txt", ModeRegular, "alpha")
	if err := s.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	back, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if back.TreeDigest() != s.TreeDigest() {
		t.Errorf("round trip changed the tree digest")
	}
	f, _ := back.Get("bin/run.sh")
	if f.Mode != ModeExec {
		t.Errorf("executable bit lost: mode %s", f.Mode)
	}
}

func TestWriteDirReplacesExistingFileAndItsMode(t *testing.T) {
	dir := t.TempDir()
	// Pre-existing file with the wrong mode and content.
	if err := os.WriteFile(filepath.Join(dir, "a.sh"), []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New()
	mustAdd(t, s, "a.sh", ModeRegular, "new")
	if err := s.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	back, err := ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := back.Get("a.sh")
	if string(f.Data) != "new" || f.Mode != ModeRegular {
		t.Errorf("got %q mode %s, want %q mode %s", f.Data, f.Mode, "new", ModeRegular)
	}
}

func TestReadDirMissingRootSatisfiesErrNotExist(t *testing.T) {
	_, err := ReadDir(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestReadDirRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("cannot create symlink here: %v", err)
	}
	_, err := ReadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "symbolic links") {
		t.Errorf("err = %v, want a symbolic-link rejection", err)
	}
}

func TestRemoveTrackedDeletesFilesAndPrunesEmptyDirs(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "vendor", "x")
	s := New()
	mustAdd(t, s, "deep/nested/a.txt", ModeRegular, "a")
	mustAdd(t, s, "b.txt", ModeRegular, "b")
	if err := s.WriteDir(root); err != nil {
		t.Fatal(err)
	}
	n, err := RemoveTracked(root, []string{"deep/nested/a.txt", "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed %d files, want 2", n)
	}
	if _, err := os.Stat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("root should have been pruned away, stat err = %v", err)
	}
}

func TestRemoveTrackedKeepsDirectoriesHoldingExtras(t *testing.T) {
	root := t.TempDir()
	s := New()
	mustAdd(t, s, "sub/a.txt", ModeRegular, "a")
	if err := s.WriteDir(root); err != nil {
		t.Fatal(err)
	}
	extra := filepath.Join(root, "sub", "extra.txt")
	if err := os.WriteFile(extra, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveTracked(root, []string{"sub/a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Errorf("extra file was deleted: %v", err)
	}
}

func TestRemoveTrackedToleratesMissingAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	s := New()
	mustAdd(t, s, "a.txt", ModeRegular, "a")
	if err := s.WriteDir(root); err != nil {
		t.Fatal(err)
	}
	// One tracked file was deleted by hand; RemoveTracked must not fail.
	n, err := RemoveTracked(root, []string{"a.txt", "gone.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("removed %d, want 1 (the file that existed)", n)
	}
	if _, err := RemoveTracked(root, []string{"../outside.txt"}); err == nil {
		t.Error("escaping path accepted by RemoveTracked")
	}
}

func TestPathsAreSorted(t *testing.T) {
	s := New()
	mustAdd(t, s, "z.txt", ModeRegular, "z")
	mustAdd(t, s, "a.txt", ModeRegular, "a")
	mustAdd(t, s, "m/x.txt", ModeRegular, "m")
	got := s.Paths()
	want := []string{"a.txt", "m/x.txt", "z.txt"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Paths() = %v, want %v", got, want)
		}
	}
}
