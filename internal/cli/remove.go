package cli

import (
	"fmt"
	"io"

	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// runRemove implements `vendorpin remove [flags] <name>`: delete the files
// the pin tracks (never local extras — those are the user's) and drop the
// lockfile entry. --keep-files drops only the entry, turning a vendored
// tree into ordinary unmanaged files.
func runRemove(args []string, stdout, stderr io.Writer) int {
	fl := newFlagSet("remove", "remove [flags] <name>", stderr)
	keepFiles := fl.Bool("keep-files", false, "drop the lockfile entry but leave the files on disk")
	lockPath := fl.String("lock", lockfile.Filename, "lockfile path")
	if code := parseExit(fl.Parse(args)); code >= 0 {
		return code
	}
	if fl.NArg() != 1 {
		return failUsage(stderr, "remove wants exactly one <name>; flags go before it")
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

	deleted := 0
	var kept []string
	if !*keepFiles {
		// Note which non-tracked files will survive, before deleting.
		local, destMissing, err := readLocal(*lockPath, entry)
		if err != nil {
			return fail(stderr, "%s: %v", name, err)
		}
		if !destMissing {
			tracked := make(map[string]bool, len(entry.Files))
			paths := make([]string, 0, len(entry.Files))
			for _, f := range entry.Files {
				tracked[f.Path] = true
				paths = append(paths, f.Path)
			}
			for _, p := range local.Paths() {
				if !tracked[p] {
					kept = append(kept, p)
				}
			}
			deleted, err = snapshot.RemoveTracked(destPath(*lockPath, entry), paths)
			if err != nil {
				return fail(stderr, "%s: %v", name, err)
			}
		}
	}

	lock.RemoveEntry(name)
	if err := lock.Save(*lockPath); err != nil {
		return fail(stderr, "writing %s: %v", *lockPath, err)
	}

	if *keepFiles {
		fmt.Fprintf(stdout, "removed %s from the lockfile; files kept in %s\n", name, entry.Dest)
		return ExitOK
	}
	fmt.Fprintf(stdout, "removed %s: %d file%s deleted from %s\n", name, deleted, plural(deleted), entry.Dest)
	for _, p := range kept {
		fmt.Fprintf(stdout, "  kept %s (not tracked by the pin)\n", joinDest(entry, p))
	}
	return ExitOK
}
