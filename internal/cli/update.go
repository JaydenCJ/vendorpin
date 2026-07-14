package cli

import (
	"fmt"
	"io"
	"sort"

	"github.com/JaydenCJ/vendorpin/internal/gitio"
	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// runUpdate implements `vendorpin update [flags] <name>`: re-resolve the
// ref (or a new --ref) against the upstream and move the pin. The safety
// contract: if the local tree has drifted from its current pin, update
// refuses (exit 1) rather than silently destroying local edits; --force
// overrides, which also makes `update --force` a restore command.
func runUpdate(args []string, stdout, stderr io.Writer) int {
	fl := newFlagSet("update", "update [flags] <name>", stderr)
	ref := fl.String("ref", "", "new branch, tag, or commit to pin (default: the entry's recorded ref)")
	force := fl.Bool("force", false, "proceed even when the local tree drifted (discards local changes)")
	dryRun := fl.Bool("dry-run", false, "show what would change without writing anything")
	lockPath := fl.String("lock", lockfile.Filename, "lockfile path")
	if code := parseExit(fl.Parse(args)); code >= 0 {
		return code
	}
	if fl.NArg() != 1 {
		return failUsage(stderr, "update wants exactly one <name>; flags go before it")
	}
	name := fl.Arg(0)

	lock, err := loadLock(*lockPath)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	idx := lock.Find(name)
	if idx < 0 {
		return fail(stderr, "no vendor named %q in the lockfile", name)
	}
	entry := lock.Vendors[idx]
	if *ref == "" {
		*ref = entry.Ref
	}

	// Safety gate: never clobber local drift without an explicit --force.
	report, err := checkEntry(*lockPath, entry)
	if err != nil {
		return fail(stderr, "%s: %v", name, err)
	}
	if !report.Clean() && !*force {
		fmt.Fprintf(stderr, "vendorpin: %s has drifted from its pin (%s)\n", name, report.Summary())
		fmt.Fprintf(stderr, "  run \"vendorpin diff %s\" to inspect, or pass --force to discard local changes\n", name)
		return ExitDrift
	}

	var newEntry lockfile.Entry
	var newSnap *snapshot.Snapshot
	err = withUpstream(entry.Upstream, func(gitDir string) error {
		commit, err := gitio.ResolveCommit(gitDir, *ref)
		if err != nil {
			return err
		}
		commitTime, err := gitio.CommitTime(gitDir, commit)
		if err != nil {
			return err
		}
		snap, err := gitio.Archive(gitDir, commit, entry.Path)
		if err != nil {
			return err
		}
		newSnap = snap
		newEntry = lockfile.Entry{
			Name:       entry.Name,
			Upstream:   entry.Upstream,
			Ref:        *ref,
			Commit:     commit,
			CommitTime: commitTime,
			Path:       entry.Path,
			Dest:       entry.Dest,
			Tree:       snap.TreeDigest(),
			Files:      lockfile.RecordsFromSnapshot(snap),
		}
		return nil
	})
	if err != nil {
		return fail(stderr, "%s: %v", name, err)
	}

	samePin := newEntry.Commit == entry.Commit
	if samePin && report.Clean() {
		fmt.Fprintf(stdout, "%s: already pinned at %s (%s), tree clean\n", name, short(entry.Commit), entry.Ref)
		return ExitOK
	}

	added, removed, changed := diffRecords(entry.Files, newEntry.Files)
	if samePin {
		fmt.Fprintf(stdout, "%s: restoring %s (%s)\n", name, short(entry.Commit), entry.Ref)
	} else {
		fmt.Fprintf(stdout, "%s: %s (%s) -> %s (%s)\n", name, short(entry.Commit), entry.Ref, short(newEntry.Commit), newEntry.Ref)
	}
	for _, p := range changed {
		fmt.Fprintf(stdout, "  ~ %s\n", p)
	}
	for _, p := range added {
		fmt.Fprintf(stdout, "  + %s\n", p)
	}
	for _, p := range removed {
		fmt.Fprintf(stdout, "  - %s\n", p)
	}
	if *dryRun {
		fmt.Fprintln(stdout, "dry run: nothing written")
		return ExitOK
	}

	destDir := destPath(*lockPath, entry)
	tracked := make([]string, 0, len(entry.Files))
	for _, f := range entry.Files {
		tracked = append(tracked, f.Path)
	}
	if _, err := snapshot.RemoveTracked(destDir, tracked); err != nil {
		return fail(stderr, "%s: clearing old tree: %v", name, err)
	}
	if err := newSnap.WriteDir(destDir); err != nil {
		return fail(stderr, "%s: writing new tree: %v", name, err)
	}
	lock.SetEntry(newEntry)
	if err := lock.Save(*lockPath); err != nil {
		return fail(stderr, "writing %s: %v", *lockPath, err)
	}
	fmt.Fprintf(stdout, "updated %s: %d file%s, tree %s\n", newEntry.Dest, len(newEntry.Files), plural(len(newEntry.Files)), shortDigest(newEntry.Tree))
	return ExitOK
}

// diffRecords compares old and new pinned records and returns sorted lists
// of added, removed, and changed (content or mode) paths.
func diffRecords(old, new []lockfile.FileRecord) (added, removed, changed []string) {
	oldBy := make(map[string]lockfile.FileRecord, len(old))
	for _, r := range old {
		oldBy[r.Path] = r
	}
	newBy := make(map[string]lockfile.FileRecord, len(new))
	for _, r := range new {
		newBy[r.Path] = r
	}
	for p, nr := range newBy {
		or, ok := oldBy[p]
		switch {
		case !ok:
			added = append(added, p)
		case or.Digest != nr.Digest || or.Mode != nr.Mode:
			changed = append(changed, p)
		}
	}
	for p := range oldBy {
		if _, ok := newBy[p]; !ok {
			removed = append(removed, p)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}
