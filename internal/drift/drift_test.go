// Tests for offline drift detection: every drift class (modified, mode,
// missing, extra), ordering, and the exact summary wording the CLI and
// README rely on.
package drift

import (
	"testing"

	"github.com/JaydenCJ/vendorpin/internal/lockfile"
	"github.com/JaydenCJ/vendorpin/internal/snapshot"
)

// pin builds the records for a canonical two-file tree.
func pin(t *testing.T) ([]lockfile.FileRecord, *snapshot.Snapshot) {
	t.Helper()
	s := snapshot.New()
	if err := s.Add("lib/a.py", snapshot.ModeRegular, []byte("alpha\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("lib/run.sh", snapshot.ModeExec, []byte("#!/bin/sh\n")); err != nil {
		t.Fatal(err)
	}
	return lockfile.RecordsFromSnapshot(s), s
}

// rebuild clones the pinned tree into a fresh snapshot with one mutation.
func rebuild(t *testing.T, src *snapshot.Snapshot, skip, addPath, addMode, addData string) *snapshot.Snapshot {
	t.Helper()
	out := snapshot.New()
	for _, p := range src.Paths() {
		if p == skip {
			continue
		}
		f, _ := src.Get(p)
		if err := out.Add(f.Path, f.Mode, f.Data); err != nil {
			t.Fatal(err)
		}
	}
	if addPath != "" {
		if err := out.Add(addPath, addMode, []byte(addData)); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestCleanWhenLocalMatchesPin(t *testing.T) {
	records, local := pin(t)
	r := Check(records, local)
	if !r.Clean() || r.Summary() != "clean" || r.Tracked != 2 {
		t.Errorf("report = %+v, want clean with 2 tracked", r)
	}
}

func TestDetectsModifiedContent(t *testing.T) {
	records, local := pin(t)
	mutated := rebuild(t, local, "lib/a.py", "lib/a.py", snapshot.ModeRegular, "beta\n")
	r := Check(records, mutated)
	if r.Modified != 1 || len(r.Drifts) != 1 || r.Drifts[0].State != StateModified {
		t.Errorf("report = %+v, want exactly one modified", r)
	}
}

func TestDetectsModeFlip(t *testing.T) {
	records, local := pin(t)
	mutated := rebuild(t, local, "lib/run.sh", "lib/run.sh", snapshot.ModeRegular, "#!/bin/sh\n")
	r := Check(records, mutated)
	if r.ModeChanged != 1 || r.Modified != 0 {
		t.Errorf("report = %+v, want exactly one mode drift (content is identical)", r)
	}
	if r.Drifts[0].State.String() != "mode" {
		t.Errorf("state = %s, want mode", r.Drifts[0].State)
	}
}

func TestDetectsMissingFile(t *testing.T) {
	records, local := pin(t)
	mutated := rebuild(t, local, "lib/a.py", "", "", "")
	r := Check(records, mutated)
	if r.Missing != 1 || r.Drifts[0].State != StateMissing {
		t.Errorf("report = %+v, want one missing", r)
	}
}

func TestDetectsExtraFile(t *testing.T) {
	records, local := pin(t)
	mutated := rebuild(t, local, "", "lib/local-patch.txt", snapshot.ModeRegular, "mine\n")
	r := Check(records, mutated)
	if r.Extra != 1 || r.Drifts[0].State != StateExtra {
		t.Errorf("report = %+v, want one extra", r)
	}
}

func TestMixedDriftIsSortedByPath(t *testing.T) {
	records, local := pin(t)
	// Remove a.py, flip run.sh's mode, and add z-extra.txt: three classes.
	mutated := rebuild(t, local, "lib/a.py", "lib/z-extra.txt", snapshot.ModeRegular, "x\n")
	m2 := rebuild(t, mutated, "lib/run.sh", "lib/run.sh", snapshot.ModeRegular, "#!/bin/sh\n")
	r := Check(records, m2)
	wantOrder := []string{"lib/a.py", "lib/run.sh", "lib/z-extra.txt"}
	if len(r.Drifts) != 3 {
		t.Fatalf("drifts = %+v, want 3", r.Drifts)
	}
	for i, w := range wantOrder {
		if r.Drifts[i].Path != w {
			t.Errorf("drifts[%d] = %s, want %s", i, r.Drifts[i].Path, w)
		}
	}
}

func TestSummaryWordingIsExact(t *testing.T) {
	// The status table and verify output quote these strings verbatim; the
	// README shows them, so wording changes must be deliberate.
	records, local := pin(t)
	mutated := rebuild(t, local, "lib/a.py", "lib/extra.txt", snapshot.ModeRegular, "x\n")
	r := Check(records, mutated)
	want := "drifted (1 missing, 1 extra)"
	if got := r.Summary(); got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
}

func TestEmptyLocalReportsEveryFileMissingAndDestMissingWins(t *testing.T) {
	records, _ := pin(t)
	r := Check(records, snapshot.New())
	if r.Missing != 2 || r.Clean() {
		t.Errorf("report = %+v, want 2 missing", r)
	}
	r.DestMissing = true
	if r.Summary() != "missing" || r.Clean() {
		t.Errorf("Summary() = %q, want %q", r.Summary(), "missing")
	}
}
