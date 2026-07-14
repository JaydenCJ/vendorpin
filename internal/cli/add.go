package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/JaydenCJ/vendorpin/internal/gitio"
	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// runAdd implements `vendorpin add [flags] <upstream>`: resolve a ref to a
// commit, copy the (sub)tree into dest, and record full provenance in the
// lockfile.
func runAdd(args []string, stdout, stderr io.Writer) int {
	fl := newFlagSet("add", "add [flags] <upstream>", stderr)
	name := fl.String("name", "", "vendor name (default: derived from the upstream URL)")
	ref := fl.String("ref", "HEAD", "upstream branch, tag, or commit to pin")
	subpath := fl.String("path", "", "subdirectory of the upstream to vendor (default: whole tree)")
	dest := fl.String("dest", "", "destination directory, relative to the lockfile (default: vendor/<name>)")
	lockPath := fl.String("lock", lockfile.Filename, "lockfile path")
	if code := parseExit(fl.Parse(args)); code >= 0 {
		return code
	}
	if fl.NArg() != 1 {
		return failUsage(stderr, "add wants exactly one <upstream> (a git URL or local path); flags go before it")
	}
	upstream := fl.Arg(0)

	if *name == "" {
		derived, err := lockfile.NameFromUpstream(upstream)
		if err != nil {
			return failUsage(stderr, "%v", err)
		}
		*name = derived
	}
	if err := lockfile.ValidateName(*name); err != nil {
		return failUsage(stderr, "%v", err)
	}
	if *dest == "" {
		*dest = "vendor/" + *name
	}
	if err := snapshot.ValidatePath(*dest); err != nil {
		return failUsage(stderr, "--dest: %v", err)
	}

	// Load the existing lockfile, or start a fresh one on first add.
	lock, err := lockfile.Load(*lockPath)
	if errors.Is(err, fs.ErrNotExist) {
		lock = lockfile.New()
	} else if err != nil {
		return fail(stderr, "%v", err)
	}
	if lock.Find(*name) >= 0 {
		return fail(stderr, "%q is already vendored; use \"vendorpin update %s\" to move its pin", *name, *name)
	}
	for _, e := range lock.Vendors {
		if e.Dest == *dest || strings.HasPrefix(*dest+"/", e.Dest+"/") || strings.HasPrefix(e.Dest+"/", *dest+"/") {
			return fail(stderr, "--dest %q overlaps the tree of vendor %q (%s)", *dest, e.Name, e.Dest)
		}
	}
	entryStub := lockfile.Entry{Name: *name, Dest: *dest}
	destDir := destPath(*lockPath, entryStub)
	if err := ensureDestFree(destDir); err != nil {
		return fail(stderr, "%v", err)
	}

	var entry lockfile.Entry
	err = withUpstream(upstream, func(gitDir string) error {
		commit, err := gitio.ResolveCommit(gitDir, *ref)
		if err != nil {
			return err
		}
		commitTime, err := gitio.CommitTime(gitDir, commit)
		if err != nil {
			return err
		}
		snap, err := gitio.Archive(gitDir, commit, *subpath)
		if err != nil {
			return err
		}
		if err := snap.WriteDir(destDir); err != nil {
			return err
		}
		entry = lockfile.Entry{
			Name:       *name,
			Upstream:   upstream,
			Ref:        *ref,
			Commit:     commit,
			CommitTime: commitTime,
			Path:       *subpath,
			Dest:       *dest,
			Tree:       snap.TreeDigest(),
			Files:      lockfile.RecordsFromSnapshot(snap),
		}
		return nil
	})
	if err != nil {
		return fail(stderr, "%v", err)
	}

	lock.SetEntry(entry)
	if err := lock.Save(*lockPath); err != nil {
		return fail(stderr, "writing %s: %v", *lockPath, err)
	}

	fmt.Fprintf(stdout, "pinned %s @ %s (%s)\n", entry.Name, short(entry.Commit), entry.Ref)
	fmt.Fprintf(stdout, "  upstream  %s\n", entry.Upstream)
	if entry.Path != "" {
		fmt.Fprintf(stdout, "  path      %s\n", entry.Path)
	}
	fmt.Fprintf(stdout, "  dest      %s\n", entry.Dest)
	fmt.Fprintf(stdout, "  files     %d\n", len(entry.Files))
	fmt.Fprintf(stdout, "  tree      %s\n", shortDigest(entry.Tree))
	return ExitOK
}

// ensureDestFree accepts a missing directory or an empty one; anything else
// would silently mix vendored and pre-existing files.
func ensureDestFree(destDir string) error {
	entries, err := os.ReadDir(destDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("destination %s already exists and is not empty; pick another --dest or remove it first", destDir)
	}
	return nil
}
