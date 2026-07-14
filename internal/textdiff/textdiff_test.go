// Tests for the unified-diff renderer: golden hunks, header arithmetic,
// hunk merging/splitting, the no-newline marker, binary detection, and the
// oversized-middle fallback. All inputs are literal — nothing depends on
// the environment.
package textdiff

import (
	"fmt"
	"strings"
	"testing"
)

func TestIdenticalInputsProduceEmptyDiff(t *testing.T) {
	if got := Unified("a", "b", []byte("same\n"), []byte("same\n")); got != "" {
		t.Errorf("diff of identical inputs = %q, want empty", got)
	}
	if got := Unified("a", "b", nil, nil); got != "" {
		t.Errorf("diff of two empty inputs = %q, want empty", got)
	}
}

func TestSingleLineChangeGolden(t *testing.T) {
	got := Unified("a", "b", []byte("one\ntwo\nthree\n"), []byte("one\nTWO\nthree\n"))
	want := "--- a\n+++ b\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAdditionAtEndOfFile(t *testing.T) {
	got := Unified("a", "b", []byte("one\ntwo\n"), []byte("one\ntwo\nthree\n"))
	want := "--- a\n+++ b\n@@ -1,2 +1,3 @@\n one\n two\n+three\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDeletionAtStartOfFile(t *testing.T) {
	got := Unified("a", "b", []byte("one\ntwo\nthree\n"), []byte("two\nthree\n"))
	want := "--- a\n+++ b\n@@ -1,3 +1,2 @@\n-one\n two\n three\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// lines renders n numbered lines, with substitutions at given 1-based
// positions.
func lines(n int, subs map[int]string) []byte {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		if s, ok := subs[i]; ok {
			sb.WriteString(s)
		} else {
			fmt.Fprintf(&sb, "line %d", i)
		}
		sb.WriteByte('\n')
	}
	return []byte(sb.String())
}

func TestFarApartChangesProduceTwoHunks(t *testing.T) {
	a := lines(20, nil)
	b := lines(20, map[int]string{2: "CHANGED-2", 18: "CHANGED-18"})
	got := Unified("a", "b", a, b)
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Errorf("got %d hunks, want 2:\n%s", n, got)
	}
	// Unchanged middle lines far from both changes must not appear.
	if strings.Contains(got, "line 10") {
		t.Errorf("line 10 should not be in any hunk:\n%s", got)
	}
}

func TestNearbyChangesMergeIntoOneHunk(t *testing.T) {
	a := lines(10, nil)
	b := lines(10, map[int]string{2: "X", 5: "Y"})
	got := Unified("a", "b", a, b)
	if n := strings.Count(got, "@@ -"); n != 1 {
		t.Errorf("got %d hunks, want 1 (changes 3 lines apart share context):\n%s", n, got)
	}
}

func TestTrailingNewlineOnlyChangeIsDetected(t *testing.T) {
	// "x\n" and "x" are different files; a diff that treats the final line
	// as unchanged context would hide the drift entirely.
	got := Unified("a", "b", []byte("x\n"), []byte("x"))
	want := "--- a\n+++ b\n@@ -1 +1 @@\n-x\n+x\n\\ No newline at end of file\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNoNewlineMarkerAppearsOnBothSides(t *testing.T) {
	got := Unified("a", "b", []byte("one\ntwo"), []byte("one\nTWO"))
	want := "--- a\n+++ b\n@@ -1,2 +1,2 @@\n one\n-two\n\\ No newline at end of file\n+TWO\n\\ No newline at end of file\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmptySidesUseZeroZeroHeaders(t *testing.T) {
	got := Unified("/dev/null", "b", nil, []byte("x\ny\n"))
	want := "--- /dev/null\n+++ b\n@@ -0,0 +1,2 @@\n+x\n+y\n"
	if got != want {
		t.Errorf("create: got:\n%s\nwant:\n%s", got, want)
	}
	got = Unified("a", "/dev/null", []byte("x\ny\n"), nil)
	want = "--- a\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-x\n-y\n"
	if got != want {
		t.Errorf("delete: got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBinaryDetectionAndNotice(t *testing.T) {
	got := Unified("a", "b", []byte("\x00\x01\x02"), []byte("text\n"))
	want := "Binary files a and b differ\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if IsBinary([]byte("plain text\n")) {
		t.Error("plain text classified as binary")
	}
	if !IsBinary([]byte{0x7f, 0x45, 0x00, 0x02}) {
		t.Error("NUL-bearing data classified as text")
	}
	// The probe window is 8 KiB, matching git; a NUL beyond it is ignored.
	late := append([]byte(strings.Repeat("a", 9000)), 0x00)
	if IsBinary(late) {
		t.Error("NUL beyond the 8 KiB probe window should not flip the heuristic")
	}
}

func TestOversizedMiddleFallsBackToValidFullReplace(t *testing.T) {
	// 2100×2100 distinct middle lines exceed the LCS cell cap, forcing the
	// delete-all/insert-all path — which must still be a valid unified diff.
	const n = 2100
	var a, b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&a, "a%d\n", i)
		fmt.Fprintf(&b, "b%d\n", i)
	}
	got := Unified("a", "b", []byte(a.String()), []byte(b.String()))
	if !strings.Contains(got, fmt.Sprintf("@@ -1,%d +1,%d @@\n", n, n)) {
		t.Fatalf("missing full-replace hunk header:\n%.200s", got)
	}
	if minus := strings.Count(got, "\n-a"); minus != n {
		t.Errorf("got %d deletions, want %d", minus, n)
	}
	if plus := strings.Count(got, "\n+b"); plus != n {
		t.Errorf("got %d insertions, want %d", plus, n)
	}
}

func TestCommonPrefixAndSuffixStayOutOfHunks(t *testing.T) {
	a := lines(100, nil)
	b := lines(100, map[int]string{50: "MIDDLE"})
	got := Unified("a", "b", a, b)
	if strings.Contains(got, "line 1\n") || strings.Contains(got, "line 100") {
		t.Errorf("prefix/suffix lines leaked into the hunk:\n%s", got)
	}
	if !strings.Contains(got, "@@ -47,7 +47,7 @@") {
		t.Errorf("unexpected hunk header:\n%s", got)
	}
}
