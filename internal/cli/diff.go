package cli

import (
	"fmt"
	"io"

	"github.com/JaydenCJ/vendorpin/internal/drift"
	"github.com/JaydenCJ/vendorpin/internal/gitio"
	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
	"github.com/JaydenCJ/vendorpin/internal/textdiff"
)

// runDiff implements `vendorpin diff [flags] [name...]`: a unified diff of
// local edits against the pinned upstream content. Clean vendors print
// nothing. The upstream is contacted only when pinned *content* is actually
// needed (modified or missing files); extra files and mode flips are
// rendered from local state alone.
func runDiff(args []string, stdout, stderr io.Writer) int {
	fl := newFlagSet("diff", "diff [flags] [name...]", stderr)
	lockPath := fl.String("lock", lockfile.Filename, "lockfile path")
	if code := parseExit(fl.Parse(args)); code >= 0 {
		return code
	}
	lock, err := loadLock(*lockPath)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	entries, err := selectEntries(lock, fl.Args())
	if err != nil {
		return fail(stderr, "%v", err)
	}

	anyOutput := false
	for _, e := range entries {
		local, destMissing, err := readLocal(*lockPath, e)
		if err != nil {
			return fail(stderr, "%s: %v", e.Name, err)
		}
		r := drift.Check(e.Files, local)
		r.DestMissing = destMissing
		if r.Clean() {
			continue
		}
		var pinned *snapshot.Snapshot
		if r.Modified > 0 || r.Missing > 0 {
			err := withUpstream(e.Upstream, func(gitDir string) error {
				snap, err := gitio.Archive(gitDir, e.Commit, e.Path)
				if err != nil {
					return err
				}
				pinned = snap
				return nil
			})
			if err != nil {
				return fail(stderr, "%s: %v", e.Name, err)
			}
		}
		out, err := renderEntryDiff(e, r, pinned, local)
		if err != nil {
			return fail(stderr, "%s: %v", e.Name, err)
		}
		if out != "" {
			anyOutput = true
			fmt.Fprint(stdout, out)
		}
	}
	if anyOutput {
		return ExitDrift
	}
	return ExitOK
}

// renderEntryDiff produces the diff text for one drifted entry, in the
// report's (sorted) file order.
func renderEntryDiff(e lockfile.Entry, r drift.Report, pinned, local *snapshot.Snapshot) (string, error) {
	out := ""
	for _, d := range r.Drifts {
		disp := joinDest(e, d.Path)
		aName := fmt.Sprintf("a/%s (pinned %s)", disp, short(e.Commit))
		bName := fmt.Sprintf("b/%s (local)", disp)
		switch d.State {
		case drift.StateModified:
			pf, ok := pinned.Get(d.Path)
			if !ok {
				return "", fmt.Errorf("pinned content for %q not found at commit %s — was the lockfile edited by hand?", d.Path, short(e.Commit))
			}
			lf, _ := local.Get(d.Path)
			out += textdiff.Unified(aName, bName, pf.Data, lf.Data)
		case drift.StateMissing:
			pf, ok := pinned.Get(d.Path)
			if !ok {
				return "", fmt.Errorf("pinned content for %q not found at commit %s — was the lockfile edited by hand?", d.Path, short(e.Commit))
			}
			out += textdiff.Unified(aName, "/dev/null", pf.Data, nil)
		case drift.StateExtra:
			lf, _ := local.Get(d.Path)
			out += textdiff.Unified("/dev/null", bName, nil, lf.Data)
		case drift.StateMode:
			lf, _ := local.Get(d.Path)
			oldMode := otherMode(lf.Mode)
			out += fmt.Sprintf("mode change %s\nold mode %s\nnew mode %s\n", disp, oldMode, lf.Mode)
		}
	}
	return out, nil
}

// otherMode flips between the two tracked modes; a mode drift means the pin
// recorded the opposite of what is on disk now.
func otherMode(mode string) string {
	if mode == snapshot.ModeExec {
		return snapshot.ModeRegular
	}
	return snapshot.ModeExec
}
