// Package drift compares a lockfile entry's pinned file records against
// what is actually on disk. The comparison is pure — records in, report
// out — and needs neither the upstream nor the network: this is what makes
// `vendorpin status` and `vendorpin verify` fully offline.
package drift

import (
	"fmt"
	"sort"
	"strings"

	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// State classifies one file relative to its pin.
type State int

const (
	StateOK       State = iota // digest and mode both match
	StateModified              // content digest differs
	StateMode                  // content matches but the executable bit flipped
	StateMissing               // pinned file absent on disk
	StateExtra                 // on-disk file the pin knows nothing about
)

// String returns the lowercase state name used in CLI output and JSON.
func (s State) String() string {
	switch s {
	case StateOK:
		return "ok"
	case StateModified:
		return "modified"
	case StateMode:
		return "mode"
	case StateMissing:
		return "missing"
	case StateExtra:
		return "extra"
	}
	return "unknown"
}

// FileDrift is one non-clean file.
type FileDrift struct {
	Path  string
	State State
}

// Report summarizes an entry's drift.
type Report struct {
	Tracked     int         // files the pin records
	Drifts      []FileDrift // every non-OK file, sorted by path
	Modified    int
	ModeChanged int
	Missing     int
	Extra       int
	DestMissing bool // the destination directory does not exist at all
}

// Clean reports whether the tree matches its pin exactly.
func (r Report) Clean() bool { return len(r.Drifts) == 0 && !r.DestMissing }

// Summary renders the one-word-or-so state shown in the status table, e.g.
// "clean", "missing", or "drifted (1 modified, 2 extra)".
func (r Report) Summary() string {
	if r.DestMissing {
		return "missing"
	}
	if r.Clean() {
		return "clean"
	}
	parts := []string{}
	if r.Modified > 0 {
		parts = append(parts, fmt.Sprintf("%d modified", r.Modified))
	}
	if r.ModeChanged > 0 {
		parts = append(parts, fmt.Sprintf("%d mode", r.ModeChanged))
	}
	if r.Missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", r.Missing))
	}
	if r.Extra > 0 {
		parts = append(parts, fmt.Sprintf("%d extra", r.Extra))
	}
	return "drifted (" + strings.Join(parts, ", ") + ")"
}

// Check compares pinned records against the local snapshot. Pass an empty
// snapshot (and set DestMissing yourself) when the dest directory is gone.
func Check(records []lockfile.FileRecord, local *snapshot.Snapshot) Report {
	r := Report{Tracked: len(records)}
	tracked := make(map[string]bool, len(records))
	for _, rec := range records {
		tracked[rec.Path] = true
		f, ok := local.Get(rec.Path)
		switch {
		case !ok:
			r.Drifts = append(r.Drifts, FileDrift{rec.Path, StateMissing})
			r.Missing++
		case snapshot.FileDigest(f.Data) != rec.Digest:
			r.Drifts = append(r.Drifts, FileDrift{rec.Path, StateModified})
			r.Modified++
		case f.Mode != rec.Mode:
			r.Drifts = append(r.Drifts, FileDrift{rec.Path, StateMode})
			r.ModeChanged++
		}
	}
	for _, p := range local.Paths() {
		if !tracked[p] {
			r.Drifts = append(r.Drifts, FileDrift{p, StateExtra})
			r.Extra++
		}
	}
	sort.Slice(r.Drifts, func(i, j int) bool { return r.Drifts[i].Path < r.Drifts[j].Path })
	return r
}
