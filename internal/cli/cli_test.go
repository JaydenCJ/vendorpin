// End-to-end tests: build a real (temporary, offline) upstream repository
// with pinned identities and dates, then drive the CLI in-process and
// assert on stdout, stderr, exit codes, the lockfile, and the files on
// disk. Everything is deterministic; no network is ever touched.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeUpstream builds the canonical upstream repository:
//
//	v1.0.0  README.md, docs/index.md, lib/{parse.py, util/text.py, run.sh*}
//	v1.1.0  lib/parse.py rewritten, lib/emit.py added
//
// so tests can pin one tag and update to the other.
func makeUpstream(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, 1, "init", "-q", "-b", "main")
	write(t, dir, "README.md", "# demo-lib\nA tiny demo library.\n")
	write(t, dir, "docs/index.md", "docs\n")
	write(t, dir, "lib/parse.py", "def parse(s):\n    return s.strip()\n")
	write(t, dir, "lib/util/text.py", "def dedent(s):\n    return s\n")
	write(t, dir, "lib/run.sh", "#!/bin/sh\necho check\n")
	if err := os.Chmod(filepath.Join(dir, "lib", "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, 1, "add", "-A")
	mustGit(t, dir, 1, "commit", "-q", "--no-gpg-sign", "-m", "v1.0.0: initial library")
	mustGit(t, dir, 1, "tag", "v1.0.0")

	write(t, dir, "lib/parse.py", "def parse(s):\n    if s is None:\n        raise ValueError(\"no input\")\n    return s.strip()\n")
	write(t, dir, "lib/emit.py", "def emit(v):\n    return str(v)\n")
	mustGit(t, dir, 2, "add", "-A")
	mustGit(t, dir, 2, "commit", "-q", "--no-gpg-sign", "-m", "v1.1.0: null guard + emit")
	mustGit(t, dir, 2, "tag", "v1.1.0")
	return dir
}

// project is a consumer repo directory with its lockfile path.
type project struct {
	dir  string
	lock string
}

func newProject(t *testing.T) project {
	t.Helper()
	dir := t.TempDir()
	return project{dir: dir, lock: filepath.Join(dir, "vendorpin.lock")}
}

// run drives the CLI in-process.
func run(args ...string) (stdout, stderr string, code int) {
	var out, errb bytes.Buffer
	code = Run(args, &out, &errb)
	return out.String(), errb.String(), code
}

// addDemo vendors upstream's lib/ as "demo-lib" at v1.0.0 and fails the
// test on any error.
func addDemo(t *testing.T, p project, upstream string) {
	t.Helper()
	stdout, stderr, code := run("add", "--lock", p.lock, "--name", "demo-lib", "--ref", "v1.0.0", "--path", "lib", upstream)
	if code != ExitOK {
		t.Fatalf("add failed (%d): %s%s", code, stdout, stderr)
	}
}

func (p project) vendored(rel string) string {
	return filepath.Join(p.dir, "vendor", "demo-lib", filepath.FromSlash(rel))
}

func TestVersionCommandAndFlagForms(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		stdout, _, code := run(arg)
		if code != ExitOK || stdout != "vendorpin 0.1.0\n" {
			t.Errorf("%s: got %q (exit %d), want \"vendorpin 0.1.0\"", arg, stdout, code)
		}
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	stdout, _, code := run("help")
	if code != ExitOK {
		t.Fatalf("help exited %d", code)
	}
	for _, cmd := range []string{"add", "status", "verify", "diff", "update", "remove", "version"} {
		if !strings.Contains(stdout, cmd) {
			t.Errorf("help output is missing %q", cmd)
		}
	}
	// Per-command help (-h) exits 0 with a synopsis, not a usage error.
	_, stderr, code := run("add", "-h")
	if code != ExitOK || !strings.Contains(stderr, "Usage: vendorpin add") {
		t.Errorf("add -h: exit %d, stderr %q; want help text", code, stderr)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	p := newProject(t)
	cases := []struct {
		args []string
		want string // substring of stderr
	}{
		{nil, "Usage"}, // no command at all
		{[]string{"frobnicate"}, "unknown command"},        //
		{[]string{"add", "--lock", p.lock}, "exactly one"}, // missing <upstream>
		{[]string{"status", "--lock", p.lock, "--format", "yaml"}, "--format"},
		{[]string{"update", "--lock", p.lock}, "exactly one"}, // missing <name>
	}
	for _, c := range cases {
		_, stderr, code := run(c.args...)
		if code != ExitUsage || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 mentioning %q", c.args, code, stderr, c.want)
		}
	}
}

func TestAddPinsTreeWritesFilesAndLockfile(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	stdout, stderr, code := run("add", "--lock", p.lock, "--name", "demo-lib", "--ref", "v1.0.0", "--path", "lib", up)
	if code != ExitOK {
		t.Fatalf("add failed (%d): %s", code, stderr)
	}
	for _, want := range []string{"pinned demo-lib @ ", "(v1.0.0)", "dest      vendor/demo-lib", "files     3", "tree      sha256:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("add output missing %q:\n%s", want, stdout)
		}
	}
	// Files landed with the subdir prefix stripped.
	data, err := os.ReadFile(p.vendored("parse.py"))
	if err != nil || !strings.Contains(string(data), "def parse") {
		t.Errorf("vendored parse.py wrong: %v %q", err, data)
	}
	// Executable bit survived the pipeline.
	info, err := os.Stat(p.vendored("run.sh"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("run.sh should be executable: %v %v", err, info)
	}
	// The lockfile is valid JSON with a full-length commit.
	raw, err := os.ReadFile(p.lock)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("lockfile is not JSON: %v", err)
	}
	v := doc["vendors"].([]any)[0].(map[string]any)
	if len(v["commit"].(string)) != 40 {
		t.Errorf("commit = %q, want 40-hex", v["commit"])
	}
	if v["commit_time"] != "2026-01-01T10:00:00+00:00" {
		t.Errorf("commit_time = %q, want the pinned upstream author date", v["commit_time"])
	}
}

func TestAddDerivesNameAndDestFromUpstream(t *testing.T) {
	// The temp upstream has a random basename, so mirror it under a stable
	// name first — the derived vendor name comes from that basename.
	up := makeUpstream(t)
	p := newProject(t)
	stable := filepath.Join(t.TempDir(), "tinylib")
	mustGit(t, filepath.Dir(stable), 1, "clone", "-q", "--bare", up, stable)
	stdout, stderr, code := run("add", "--lock", p.lock, stable)
	if code != ExitOK {
		t.Fatalf("add failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "pinned tinylib @") || !strings.Contains(stdout, "dest      vendor/tinylib") {
		t.Errorf("derived name/dest wrong:\n%s", stdout)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "vendor", "tinylib", "README.md")); err != nil {
		t.Errorf("whole-tree vendoring missed README.md: %v", err)
	}
}

func TestAddRefusesDuplicateName(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	_, stderr, code := run("add", "--lock", p.lock, "--name", "demo-lib", "--dest", "vendor/other", up)
	if code != ExitRuntime || !strings.Contains(stderr, "already vendored") {
		t.Errorf("exit %d, stderr %q; want already-vendored error", code, stderr)
	}
}

func TestAddRefusesNonEmptyDest(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	write(t, p.dir, "vendor/demo-lib/precious.txt", "do not clobber\n")
	_, stderr, code := run("add", "--lock", p.lock, "--name", "demo-lib", up)
	if code != ExitRuntime || !strings.Contains(stderr, "not empty") {
		t.Errorf("exit %d, stderr %q; want non-empty-dest refusal", code, stderr)
	}
	if _, err := os.Stat(p.vendored("precious.txt")); err != nil {
		t.Errorf("pre-existing file was touched: %v", err)
	}
}

func TestAddRefusesOverlappingDest(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	_, stderr, code := run("add", "--lock", p.lock, "--name", "nested", "--dest", "vendor/demo-lib/nested", up)
	if code != ExitRuntime || !strings.Contains(stderr, "overlaps") {
		t.Errorf("exit %d, stderr %q; want overlap refusal", code, stderr)
	}
}

func TestAddFailsCleanlyOnBadInput(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	// Unknown ref: runtime error, and no files may be left behind.
	_, stderr, code := run("add", "--lock", p.lock, "--name", "demo-lib", "--ref", "v9.9.9", up)
	if code != ExitRuntime || !strings.Contains(stderr, "not found in upstream") {
		t.Errorf("exit %d, stderr %q; want ref-not-found error", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(p.dir, "vendor")); !os.IsNotExist(err) {
		t.Error("failed add left files behind")
	}
	// Unreachable upstream.
	_, stderr, code = run("add", "--lock", p.lock, "--name", "x", filepath.Join(p.dir, "no-such-repo"))
	if code != ExitRuntime || !strings.Contains(stderr, "cannot reach upstream") {
		t.Errorf("exit %d, stderr %q; want cannot-reach error", code, stderr)
	}
	// Invalid --name is a usage error, caught before any cloning.
	_, stderr, code = run("add", "--lock", p.lock, "--name", "-bad", up)
	if code != ExitUsage || !strings.Contains(stderr, "vendor name") {
		t.Errorf("exit %d, stderr %q; want name-validation usage error", code, stderr)
	}
}

func TestStatusCleanTable(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	stdout, _, code := run("status", "--lock", p.lock)
	if code != ExitOK {
		t.Fatalf("status exited %d", code)
	}
	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "STATE") {
		t.Errorf("missing table header:\n%s", stdout)
	}
	if !strings.Contains(stdout, "demo-lib") || !strings.Contains(stdout, "clean") || !strings.Contains(stdout, "v1.0.0") {
		t.Errorf("missing row content:\n%s", stdout)
	}
}

func TestStatusJSONHasStableSchema(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	stdout, _, code := run("status", "--lock", p.lock, "--format", "json")
	if code != ExitOK {
		t.Fatalf("status exited %d", code)
	}
	var doc struct {
		Tool          string `json:"tool"`
		SchemaVersion int    `json:"schema_version"`
		Clean         bool   `json:"clean"`
		Vendors       []struct {
			Name  string `json:"name"`
			State string `json:"state"`
			Files int    `json:"files"`
		} `json:"vendors"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("status --format json is not JSON: %v\n%s", err, stdout)
	}
	if doc.Tool != "vendorpin" || doc.SchemaVersion != 1 || !doc.Clean {
		t.Errorf("envelope wrong: %+v", doc)
	}
	if len(doc.Vendors) != 1 || doc.Vendors[0].State != "clean" || doc.Vendors[0].Files != 3 {
		t.Errorf("vendor row wrong: %+v", doc.Vendors)
	}
}

func TestStatusUnknownFormatIsUsageError(t *testing.T) {
	p := newProject(t)
	_, stderr, code := run("status", "--lock", p.lock, "--format", "yaml")
	if code != ExitUsage || !strings.Contains(stderr, "--format") {
		t.Errorf("exit %d, stderr %q; want format usage error", code, stderr)
	}
}

func TestStatusWithoutLockfileGivesActionableError(t *testing.T) {
	p := newProject(t)
	_, stderr, code := run("status", "--lock", p.lock)
	if code != ExitRuntime || !strings.Contains(stderr, "vendorpin add") {
		t.Errorf("exit %d, stderr %q; want hint to run add", code, stderr)
	}
}

func TestStatusReflectsEveryDriftClass(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	write(t, p.dir, "vendor/demo-lib/parse.py", "def parse(s):\n    return s\n") // modified
	write(t, p.dir, "vendor/demo-lib/NOTES.txt", "local note\n")                 // extra
	if err := os.Remove(p.vendored("util/text.py")); err != nil {                // missing
		t.Fatal(err)
	}
	stdout, _, code := run("status", "--lock", p.lock)
	if code != ExitOK {
		t.Fatalf("status exited %d", code)
	}
	if !strings.Contains(stdout, "drifted (1 modified, 1 missing, 1 extra)") {
		t.Errorf("summary wrong:\n%s", stdout)
	}
}

func TestVerifyCleanExitsZero(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	stdout, _, code := run("verify", "--lock", p.lock)
	if code != ExitOK {
		t.Fatalf("verify exited %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "verify: OK (1 vendor clean, 3 files intact)") {
		t.Errorf("verify output wrong:\n%s", stdout)
	}
}

func TestVerifyDriftExitsOneAndNamesFiles(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	write(t, p.dir, "vendor/demo-lib/parse.py", "tampered\n")
	stdout, _, code := run("verify", "--lock", p.lock)
	if code != ExitDrift {
		t.Fatalf("verify exited %d, want 1", code)
	}
	if !strings.Contains(stdout, "modified") || !strings.Contains(stdout, "vendor/demo-lib/parse.py") {
		t.Errorf("drifted file not named:\n%s", stdout)
	}
	if !strings.Contains(stdout, "verify: FAIL (1 of 1 vendor drifted)") {
		t.Errorf("FAIL footer wrong:\n%s", stdout)
	}
}

func TestVerifyReportsMissingDest(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	if err := os.RemoveAll(filepath.Join(p.dir, "vendor")); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := run("verify", "--lock", p.lock)
	if code != ExitDrift || !strings.Contains(stdout, "missing (vendor/demo-lib is not on disk)") {
		t.Errorf("exit %d:\n%s", code, stdout)
	}
}

func TestNameSelectionAcrossCommands(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	_, _, code := run("add", "--lock", p.lock, "--name", "second", "--dest", "vendor/second", "--ref", "v1.1.0", up)
	if code != ExitOK {
		t.Fatal("second add failed")
	}
	write(t, p.dir, "vendor/second/extra.txt", "drift\n")
	// Checking only the clean vendor must pass despite the other drifting.
	_, _, code = run("verify", "--lock", p.lock, "demo-lib")
	if code != ExitOK {
		t.Errorf("verify demo-lib exited %d, want 0", code)
	}
	// Unknown names are runtime errors for every selecting command.
	for _, cmd := range []string{"verify", "update", "remove"} {
		_, stderr, code := run(cmd, "--lock", p.lock, "no-such")
		if code != ExitRuntime || !strings.Contains(stderr, "no vendor named") {
			t.Errorf("%s: exit %d, stderr %q; want unknown-vendor error", cmd, code, stderr)
		}
	}
}

func TestDiffCleanPrintsNothingAndExitsZero(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	stdout, _, code := run("diff", "--lock", p.lock)
	if code != ExitOK || stdout != "" {
		t.Errorf("exit %d, stdout %q; want silent success", code, stdout)
	}
}

func TestDiffShowsModifiedExtraMissingAndMode(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	write(t, p.dir, "vendor/demo-lib/parse.py", "def parse(s):\n    return s\n")
	write(t, p.dir, "vendor/demo-lib/NOTES.txt", "local note\n")
	if err := os.Remove(p.vendored("util/text.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p.vendored("run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run("diff", "--lock", p.lock)
	if code != ExitDrift {
		t.Fatalf("diff exited %d: %s", code, stderr)
	}
	for _, want := range []string{
		"--- a/vendor/demo-lib/parse.py (pinned ",
		"+++ b/vendor/demo-lib/parse.py (local)",
		"-    return s.strip()",
		"+    return s",
		"--- /dev/null",                      // the extra file appears as created
		"+local note",                        //
		"+++ /dev/null",                      // the missing file appears as deleted
		"-def dedent(s):",                    //
		"mode change vendor/demo-lib/run.sh", // the exec-bit flip
		"old mode 755",
		"new mode 644",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("diff missing %q:\n%s", want, stdout)
		}
	}
}

func TestUpdateRefusesToClobberDriftWithoutForce(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	write(t, p.dir, "vendor/demo-lib/parse.py", "my local fix\n")
	_, stderr, code := run("update", "--lock", p.lock, "--ref", "v1.1.0", "demo-lib")
	if code != ExitDrift || !strings.Contains(stderr, "has drifted from its pin") {
		t.Fatalf("exit %d, stderr %q; want drift refusal", code, stderr)
	}
	// The local edit must be untouched.
	data, _ := os.ReadFile(p.vendored("parse.py"))
	if string(data) != "my local fix\n" {
		t.Errorf("local edit was clobbered: %q", data)
	}
}

func TestUpdateForceDiscardsDriftAndMovesPin(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	write(t, p.dir, "vendor/demo-lib/parse.py", "my local fix\n")
	stdout, stderr, code := run("update", "--lock", p.lock, "--ref", "v1.1.0", "--force", "demo-lib")
	if code != ExitOK {
		t.Fatalf("update exited %d: %s", code, stderr)
	}
	for _, want := range []string{"(v1.0.0) -> ", "(v1.1.0)", "~ parse.py", "+ emit.py", "updated vendor/demo-lib: 4 files"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("update output missing %q:\n%s", want, stdout)
		}
	}
	data, _ := os.ReadFile(p.vendored("parse.py"))
	if !strings.Contains(string(data), "raise ValueError") {
		t.Errorf("parse.py not moved to v1.1.0 content: %q", data)
	}
	if _, _, code := run("verify", "--lock", p.lock); code != ExitOK {
		t.Error("tree should verify clean after a forced update")
	}
}

func TestUpdateDryRunWritesNothing(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	before, _ := os.ReadFile(p.lock)
	stdout, _, code := run("update", "--lock", p.lock, "--ref", "v1.1.0", "--dry-run", "demo-lib")
	if code != ExitOK || !strings.Contains(stdout, "dry run: nothing written") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "+ emit.py") {
		t.Errorf("dry run should preview the change:\n%s", stdout)
	}
	after, _ := os.ReadFile(p.lock)
	if !bytes.Equal(before, after) {
		t.Error("dry run modified the lockfile")
	}
	if _, err := os.Stat(p.vendored("emit.py")); !os.IsNotExist(err) {
		t.Error("dry run wrote files")
	}
}

func TestUpdateIsANoopWhenAlreadyPinned(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	stdout, _, code := run("update", "--lock", p.lock, "demo-lib")
	if code != ExitOK || !strings.Contains(stdout, "already pinned at") {
		t.Errorf("exit %d:\n%s", code, stdout)
	}
}

func TestUpdateForceRestoresAMissingTree(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	if err := os.RemoveAll(filepath.Join(p.dir, "vendor")); err != nil {
		t.Fatal(err)
	}
	stdout, _, code := run("update", "--lock", p.lock, "--force", "demo-lib")
	if code != ExitOK || !strings.Contains(stdout, "restoring") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	if _, _, code := run("verify", "--lock", p.lock); code != ExitOK {
		t.Error("restored tree should verify clean")
	}
}

func TestRemoveDeletesTrackedFilesButKeepsExtras(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	write(t, p.dir, "vendor/demo-lib/NOTES.txt", "mine\n")
	stdout, _, code := run("remove", "--lock", p.lock, "demo-lib")
	if code != ExitOK {
		t.Fatalf("remove exited %d", code)
	}
	if !strings.Contains(stdout, "removed demo-lib: 3 files deleted") {
		t.Errorf("remove output wrong:\n%s", stdout)
	}
	if !strings.Contains(stdout, "kept vendor/demo-lib/NOTES.txt") {
		t.Errorf("extra file not reported as kept:\n%s", stdout)
	}
	if _, err := os.Stat(p.vendored("NOTES.txt")); err != nil {
		t.Errorf("extra file was deleted: %v", err)
	}
	if _, err := os.Stat(p.vendored("parse.py")); !os.IsNotExist(err) {
		t.Error("tracked file survived removal")
	}
	stdout, _, _ = run("status", "--lock", p.lock)
	if !strings.Contains(stdout, "no vendors pinned yet") {
		t.Errorf("lockfile still lists vendors:\n%s", stdout)
	}
}

func TestRemoveKeepFilesLeavesTreeOnDisk(t *testing.T) {
	up := makeUpstream(t)
	p := newProject(t)
	addDemo(t, p, up)
	stdout, _, code := run("remove", "--lock", p.lock, "--keep-files", "demo-lib")
	if code != ExitOK || !strings.Contains(stdout, "files kept in vendor/demo-lib") {
		t.Fatalf("exit %d:\n%s", code, stdout)
	}
	if _, err := os.Stat(p.vendored("parse.py")); err != nil {
		t.Errorf("--keep-files deleted files: %v", err)
	}
}
