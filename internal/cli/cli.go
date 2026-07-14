// Package cli implements the vendorpin command-line interface. Run takes
// argv and two writers and returns an exit code, so the whole surface is
// testable in-process without building a binary.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/JaydenCJ/vendorpin/internal/drift"
	"github.com/JaydenCJ/vendorpin/internal/gitio"
	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
	"github.com/JaydenCJ/vendorpin/internal/version"
)

// Exit codes. Documented in the README; 1 is the machine-readable drift
// verdict used by `verify`, `diff`, and a refused `update`.
const (
	ExitOK      = 0
	ExitDrift   = 1
	ExitUsage   = 2
	ExitRuntime = 3
)

// Run dispatches argv and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return ExitUsage
	}
	switch args[0] {
	case "add":
		return runAdd(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "diff":
		return runDiff(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "remove":
		return runRemove(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "vendorpin %s\n", version.Version)
		return ExitOK
	case "help", "--help", "-h":
		usage(stdout)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "vendorpin: unknown command %q\n\n", args[0])
		usage(stderr)
		return ExitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `vendorpin %s — vendor a subdirectory of another repo at a pinned commit.

Usage:
  vendorpin <command> [flags] [args]

Commands:
  add <upstream>    pin an upstream (sub)tree and copy it into this repo
  status [name...]  show every vendored tree and whether it drifted (offline)
  verify [name...]  exit 1 if any vendored tree drifted from its pin (offline)
  diff [name...]    unified diff of local edits against the pinned content
  update <name>     re-pin to a new upstream commit; refuses to clobber drift
  remove <name>     delete a vendored tree and its lockfile entry
  version           print the vendorpin version

Flags go before positional arguments: vendorpin add --ref v1.2.3 <upstream>.
Run "vendorpin <command> -h" for the flags of one command.
Exit codes: 0 ok, 1 drift found, 2 usage error, 3 runtime error.
`, version.Version)
}

// newFlagSet builds a flag set that reports errors (and -h help) on stderr.
func newFlagSet(name, synopsis string, stderr io.Writer) *flag.FlagSet {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(stderr)
	fl.Usage = func() {
		fmt.Fprintf(stderr, "Usage: vendorpin %s\n", synopsis)
		fl.PrintDefaults()
	}
	return fl
}

// parseExit maps a flag.Parse error to an exit code (-1 = keep going).
func parseExit(err error) int {
	if err == nil {
		return -1
	}
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	return ExitUsage
}

// fail prints a runtime error in the standard shape.
func fail(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "vendorpin: "+format+"\n", a...)
	return ExitRuntime
}

// failUsage prints a usage error in the standard shape.
func failUsage(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "vendorpin: "+format+"\n", a...)
	return ExitUsage
}

// loadLock reads and validates the lockfile, translating a missing file
// into an actionable hint.
func loadLock(lockPath string) (*lockfile.Lockfile, error) {
	l, err := lockfile.Load(lockPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no lockfile at %s (run \"vendorpin add <upstream>\" first, or pass --lock)", lockPath)
		}
		return nil, err
	}
	return l, nil
}

// selectEntries resolves the optional trailing name arguments to lockfile
// entries; with no names it returns every entry in lockfile (sorted) order.
func selectEntries(l *lockfile.Lockfile, names []string) ([]lockfile.Entry, error) {
	if len(names) == 0 {
		return l.Vendors, nil
	}
	out := make([]lockfile.Entry, 0, len(names))
	for _, n := range names {
		i := l.Find(n)
		if i < 0 {
			return nil, fmt.Errorf("no vendor named %q in the lockfile", n)
		}
		out = append(out, l.Vendors[i])
	}
	return out, nil
}

// destPath resolves an entry's destination directory on disk.
func destPath(lockPath string, e lockfile.Entry) string {
	return filepath.Join(lockfile.Dir(lockPath), filepath.FromSlash(e.Dest))
}

// readLocal loads the on-disk tree for an entry. A missing destination is
// not an error — it is drift, reported via destMissing.
func readLocal(lockPath string, e lockfile.Entry) (local *snapshot.Snapshot, destMissing bool, err error) {
	local, err = snapshot.ReadDir(destPath(lockPath, e))
	if errors.Is(err, fs.ErrNotExist) {
		return snapshot.New(), true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return local, false, nil
}

// checkEntry runs offline drift detection for one entry.
func checkEntry(lockPath string, e lockfile.Entry) (drift.Report, error) {
	local, destMissing, err := readLocal(lockPath, e)
	if err != nil {
		return drift.Report{}, err
	}
	r := drift.Check(e.Files, local)
	r.DestMissing = destMissing
	return r, nil
}

// withUpstream clones the upstream into a temporary bare repository and
// hands its path to fn; the clone is always removed afterwards.
func withUpstream(upstream string, fn func(gitDir string) error) error {
	dir, err := os.MkdirTemp("", "vendorpin-upstream-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	gitDir := filepath.Join(dir, "clone.git")
	if err := gitio.CloneBare(upstream, gitDir); err != nil {
		return err
	}
	return fn(gitDir)
}

// short abbreviates a commit hash for display.
func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// shortDigest abbreviates a "sha256:<64 hex>" digest for display; the full
// value lives in the lockfile.
func shortDigest(d string) string {
	const keep = len("sha256:") + 12
	if len(d) > keep {
		return d[:keep] + "…"
	}
	return d
}

// plural is the tiny s-suffix helper for human-facing counts.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// joinDest renders the dest-relative display path of a pinned file.
func joinDest(e lockfile.Entry, rel string) string {
	return e.Dest + "/" + rel
}
