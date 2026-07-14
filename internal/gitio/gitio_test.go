// Tests for the git plumbing layer, run against real repositories built in
// temp dirs with pinned identities and dates — fully offline and
// deterministic. These tests need a `git` binary on PATH, exactly like the
// tool itself.
package gitio

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// gitEnv isolates git from the host user's configuration and pins all
// dates so commit hashes and timestamps are reproducible per test run.
func gitEnv(seq int) []string {
	date := fmt.Sprintf("2026-01-%02dT10:00:00+00:00", seq)
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Upstream Dev",
		"GIT_AUTHOR_EMAIL=dev@example.test",
		"GIT_COMMITTER_NAME=Upstream Dev",
		"GIT_COMMITTER_EMAIL=dev@example.test",
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_DATE="+date,
	)
}

func mustGit(t *testing.T, dir string, seq int, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(seq)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string, exec bool) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	perm := os.FileMode(0o644)
	if exec {
		perm = 0o755
	}
	if err := os.WriteFile(abs, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// makeRepo builds the canonical upstream: a README at the root, three files
// under lib/ (one executable, one nested), and a docs dir, tagged v1.0.0.
func makeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, 1, "init", "-q", "-b", "main")
	write(t, dir, "README.md", "# upstream\n", false)
	write(t, dir, "lib/parse.py", "def parse(s):\n    return s.strip()\n", false)
	write(t, dir, "lib/util/text.py", "def dedent(s):\n    return s\n", false)
	write(t, dir, "lib/run.sh", "#!/bin/sh\necho check\n", true)
	write(t, dir, "docs/index.md", "docs\n", false)
	mustGit(t, dir, 1, "add", "-A")
	mustGit(t, dir, 1, "commit", "-q", "--no-gpg-sign", "-m", "v1.0.0: initial")
	mustGit(t, dir, 1, "tag", "v1.0.0")
	return dir
}

// bareClone clones the repo the way the tool does and returns the git dir.
func bareClone(t *testing.T, upstream string) string {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), "clone.git")
	if err := CloneBare(upstream, gitDir); err != nil {
		t.Fatalf("CloneBare: %v", err)
	}
	return gitDir
}

func TestCloneBareAndResolveHEAD(t *testing.T) {
	repo := makeRepo(t)
	gitDir := bareClone(t, repo)
	head, err := ResolveCommit(gitDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 40 {
		t.Errorf("HEAD = %q, want 40-hex", head)
	}
	want := mustGit(t, repo, 1, "rev-parse", "HEAD")
	if head != want {
		t.Errorf("resolved HEAD %s differs from upstream HEAD %s", head, want)
	}
}

func TestResolveCommitTagAndBranchAgree(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	tag, err := ResolveCommit(gitDir, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	branch, err := ResolveCommit(gitDir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if tag != branch {
		t.Errorf("tag %s and branch %s should point at the same commit", tag, branch)
	}
}

func TestResolveCommitUnknownRefFails(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	_, err := ResolveCommit(gitDir, "v9.9.9")
	if err == nil || !strings.Contains(err.Error(), "not found in upstream") {
		t.Errorf("err = %v, want a ref-not-found error", err)
	}
}

func TestCommitTimeIsThePinnedAuthorDate(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	ts, err := CommitTime(gitDir, head)
	if err != nil {
		t.Fatal(err)
	}
	if ts != "2026-01-01T10:00:00+00:00" {
		t.Errorf("CommitTime = %q, want the pinned author date", ts)
	}
}

func TestArchiveWholeTree(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	snap, err := Archive(gitDir, head, "")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Len() != 5 {
		t.Errorf("Len = %d, want 5: %v", snap.Len(), snap.Paths())
	}
	if _, ok := snap.Get("README.md"); !ok {
		t.Error("README.md missing from whole-tree archive")
	}
	if _, ok := snap.Get("lib/util/text.py"); !ok {
		t.Error("nested file missing from whole-tree archive")
	}
}

func TestArchiveSubdirStripsThePrefix(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	snap, err := Archive(gitDir, head, "lib")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"parse.py", "run.sh", "util/text.py"}
	got := snap.Paths()
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths = %v, want %v", got, want)
		}
	}
}

func TestArchiveMissingSubdirFails(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	_, err := Archive(gitDir, head, "no-such-dir")
	if err == nil || !strings.Contains(err.Error(), "not found at commit") {
		t.Errorf("err = %v, want a path-not-found error", err)
	}
}

func TestArchivePathThatIsAFileFails(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	_, err := Archive(gitDir, head, "README.md")
	if err == nil || !strings.Contains(err.Error(), "is a file, not a directory") {
		t.Errorf("err = %v, want a file-not-directory error", err)
	}
}

func TestArchivePreservesTheExecutableBit(t *testing.T) {
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	snap, err := Archive(gitDir, head, "lib")
	if err != nil {
		t.Fatal(err)
	}
	run, _ := snap.Get("run.sh")
	parse, _ := snap.Get("parse.py")
	if run.Mode != snapshot.ModeExec {
		t.Errorf("run.sh mode = %s, want 755", run.Mode)
	}
	if parse.Mode != snapshot.ModeRegular {
		t.Errorf("parse.py mode = %s, want 644", parse.Mode)
	}
}

func TestArchiveRejectsSymlinks(t *testing.T) {
	repo := makeRepo(t)
	if err := os.Symlink("README.md", filepath.Join(repo, "alias.md")); err != nil {
		t.Skipf("cannot create symlink here: %v", err)
	}
	mustGit(t, repo, 2, "add", "-A")
	mustGit(t, repo, 2, "commit", "-q", "--no-gpg-sign", "-m", "add symlink")
	gitDir := bareClone(t, repo)
	head, _ := ResolveCommit(gitDir, "HEAD")
	_, err := Archive(gitDir, head, "")
	if err == nil || !strings.Contains(err.Error(), "links are not supported") {
		t.Errorf("err = %v, want a link rejection", err)
	}
}

func TestCloneBareUnreachableUpstreamFails(t *testing.T) {
	err := CloneBare(filepath.Join(t.TempDir(), "does-not-exist"), filepath.Join(t.TempDir(), "clone.git"))
	if err == nil || !strings.Contains(err.Error(), "cannot reach upstream") {
		t.Errorf("err = %v, want a cannot-reach error", err)
	}
}

func TestArchiveRejectsRefsPassedAsSubdirFlags(t *testing.T) {
	// A subdir starting with '-' could be smuggled to git as a flag; the
	// validation layer must reject it before git ever sees it.
	gitDir := bareClone(t, makeRepo(t))
	head, _ := ResolveCommit(gitDir, "HEAD")
	_, err := Archive(gitDir, head, "-o=evil")
	if err == nil {
		t.Error("subdir starting with '-' was accepted")
	}
}
