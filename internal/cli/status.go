package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/JaydenCJ/vendorpin/internal/lockfile"
)

// statusRow is one vendor in status output; the JSON shape is stable
// (schema_version 1).
type statusRow struct {
	Name   string       `json:"name"`
	Commit string       `json:"commit"`
	Ref    string       `json:"ref"`
	Dest   string       `json:"dest"`
	Files  int          `json:"files"`
	State  string       `json:"state"`
	Drift  *driftCounts `json:"drift,omitempty"`
}

type driftCounts struct {
	Modified int `json:"modified"`
	Mode     int `json:"mode"`
	Missing  int `json:"missing"`
	Extra    int `json:"extra"`
}

type statusDoc struct {
	Tool          string      `json:"tool"`
	SchemaVersion int         `json:"schema_version"`
	Vendors       []statusRow `json:"vendors"`
	Clean         bool        `json:"clean"`
}

// runStatus implements `vendorpin status [flags] [name...]`: a purely
// offline drift summary. Informational — always exits 0 when it could
// check; `verify` is the gate.
func runStatus(args []string, stdout, stderr io.Writer) int {
	fl := newFlagSet("status", "status [flags] [name...]", stderr)
	format := fl.String("format", "text", "output format: text or json")
	lockPath := fl.String("lock", lockfile.Filename, "lockfile path")
	if code := parseExit(fl.Parse(args)); code >= 0 {
		return code
	}
	if *format != "text" && *format != "json" {
		return failUsage(stderr, "unknown --format %q (want text or json)", *format)
	}
	lock, err := loadLock(*lockPath)
	if err != nil {
		return fail(stderr, "%v", err)
	}
	entries, err := selectEntries(lock, fl.Args())
	if err != nil {
		return fail(stderr, "%v", err)
	}

	doc := statusDoc{Tool: "vendorpin", SchemaVersion: 1, Vendors: []statusRow{}, Clean: true}
	for _, e := range entries {
		r, err := checkEntry(*lockPath, e)
		if err != nil {
			return fail(stderr, "%s: %v", e.Name, err)
		}
		row := statusRow{
			Name:   e.Name,
			Commit: e.Commit,
			Ref:    e.Ref,
			Dest:   e.Dest,
			Files:  r.Tracked,
			State:  r.Summary(),
		}
		if !r.Clean() {
			doc.Clean = false
			row.Drift = &driftCounts{Modified: r.Modified, Mode: r.ModeChanged, Missing: r.Missing, Extra: r.Extra}
		}
		doc.Vendors = append(doc.Vendors, row)
	}

	if *format == "json" {
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return fail(stderr, "%v", err)
		}
		fmt.Fprintln(stdout, string(out))
		return ExitOK
	}
	if len(doc.Vendors) == 0 {
		fmt.Fprintln(stdout, "no vendors pinned yet")
		return ExitOK
	}
	renderStatusTable(stdout, doc.Vendors)
	return ExitOK
}

// renderStatusTable prints the aligned text table.
func renderStatusTable(w io.Writer, rows []statusRow) {
	nameW, refW := len("NAME"), len("REF")
	for _, r := range rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(r.Ref) > refW {
			refW = len(r.Ref)
		}
	}
	fmt.Fprintf(w, "%-*s  %-7s  %-*s  %5s  %s\n", nameW, "NAME", "COMMIT", refW, "REF", "FILES", "STATE")
	for _, r := range rows {
		fmt.Fprintf(w, "%-*s  %-7s  %-*s  %5d  %s\n", nameW, r.Name, short(r.Commit), refW, r.Ref, r.Files, r.State)
	}
}

// runVerify implements `vendorpin verify [flags] [name...]`: the offline
// drift gate. Exit 0 only when every selected vendor matches its pin.
func runVerify(args []string, stdout, stderr io.Writer) int {
	fl := newFlagSet("verify", "verify [flags] [name...]", stderr)
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

	drifted, filesIntact := 0, 0
	for _, e := range entries {
		r, err := checkEntry(*lockPath, e)
		if err != nil {
			return fail(stderr, "%s: %v", e.Name, err)
		}
		switch {
		case r.DestMissing:
			drifted++
			fmt.Fprintf(stdout, "%s: missing (%s is not on disk)\n", e.Name, e.Dest)
		case r.Clean():
			filesIntact += r.Tracked
			fmt.Fprintf(stdout, "%s: clean (%d file%s)\n", e.Name, r.Tracked, plural(r.Tracked))
		default:
			drifted++
			fmt.Fprintf(stdout, "%s: %s\n", e.Name, r.Summary())
			for _, d := range r.Drifts {
				fmt.Fprintf(stdout, "  %-8s  %s\n", d.State, joinDest(e, d.Path))
			}
		}
	}
	if drifted > 0 {
		fmt.Fprintf(stdout, "verify: FAIL (%d of %d vendor%s drifted)\n", drifted, len(entries), plural(len(entries)))
		return ExitDrift
	}
	fmt.Fprintf(stdout, "verify: OK (%d vendor%s clean, %d file%s intact)\n",
		len(entries), plural(len(entries)), filesIntact, plural(filesIntact))
	return ExitOK
}
