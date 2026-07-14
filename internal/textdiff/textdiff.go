// Package textdiff produces unified diffs for `vendorpin diff`. It is pure
// (bytes in, string out) and deterministic. The line-matching core trims
// the common prefix and suffix, then runs an exact LCS on what remains;
// for pathologically large middles it degrades to a full replace hunk —
// still a valid, applyable unified diff, just not a minimal one.
package textdiff

import (
	"bytes"
	"fmt"
	"strings"
)

// context is the number of unchanged lines shown around each change,
// matching the diff -u default.
const context = 3

// lcsCellCap bounds the DP table (rows*cols) after prefix/suffix trimming.
// 4M cells keeps worst-case memory around 32 MB; beyond that the middle is
// emitted as one replace hunk.
const lcsCellCap = 4_000_000

// IsBinary reports whether data looks binary: a NUL byte in the first 8 KiB,
// the same heuristic git uses.
func IsBinary(data []byte) bool {
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	return bytes.IndexByte(probe, 0) >= 0
}

// Unified renders a unified diff of a → b with the given header names.
// Identical inputs yield "". Binary inputs yield a one-line notice.
func Unified(aName, bName string, a, b []byte) string {
	if bytes.Equal(a, b) {
		return ""
	}
	if IsBinary(a) || IsBinary(b) {
		return fmt.Sprintf("Binary files %s and %s differ\n", aName, bName)
	}
	aLines, aNoEOL := splitLines(a)
	bLines, bNoEOL := splitLines(b)
	ops := diffOps(compareKeys(aLines, aNoEOL), compareKeys(bLines, bNoEOL))
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	for _, h := range buildHunks(ops) {
		sb.WriteString(hunkHeader(h))
		for _, op := range h.ops {
			line := ""
			markNoEOL := false
			switch op.kind {
			case ' ', '-':
				// For a context op the keys matched, so the two finals have
				// the same termination; checking a's side covers both.
				line = aLines[op.aIdx]
				markNoEOL = aNoEOL && op.aIdx == len(aLines)-1
			case '+':
				line = bLines[op.bIdx]
				markNoEOL = bNoEOL && op.bIdx == len(bLines)-1
			}
			sb.WriteByte(op.kind)
			sb.WriteString(line)
			sb.WriteByte('\n')
			if markNoEOL {
				sb.WriteString("\\ No newline at end of file\n")
			}
		}
	}
	return sb.String()
}

// compareKeys returns the slice the diff actually compares. An unterminated
// final line gets an impossible sentinel suffix so that "x\n" and "x" (no
// newline) never match as context — they differ as files and must render as
// a -/+ pair with the no-newline marker.
func compareKeys(lines []string, noEOL bool) []string {
	if !noEOL || len(lines) == 0 {
		return lines
	}
	keys := make([]string, len(lines))
	copy(keys, lines)
	keys[len(keys)-1] += "\x00<no-eol>"
	return keys
}

// splitLines splits data into lines without their trailing newline and
// reports whether the final line was unterminated.
func splitLines(data []byte) (lines []string, noEOL bool) {
	if len(data) == 0 {
		return nil, false
	}
	s := string(data)
	noEOL = !strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n"), noEOL
}

// op is one diff operation: keep (' '), delete ('-'), or insert ('+').
// aIdx/bIdx are the source indices; -1 when the side does not apply.
type op struct {
	kind byte
	aIdx int
	bIdx int
}

// diffOps computes the op sequence transforming a into b.
func diffOps(a, b []string) []op {
	// Trim the common prefix.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	// Trim the common suffix (without overlapping the prefix).
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	midA := a[pre : len(a)-suf]
	midB := b[pre : len(b)-suf]

	ops := make([]op, 0, len(a)+len(b))
	for i := 0; i < pre; i++ {
		ops = append(ops, op{' ', i, i})
	}
	ops = append(ops, middleOps(midA, midB, pre)...)
	for i := 0; i < suf; i++ {
		ops = append(ops, op{' ', len(a) - suf + i, len(b) - suf + i})
	}
	return ops
}

// middleOps diffs the trimmed middle. off is the prefix length, the index
// offset of the middle inside both originals (a-offset == b-offset because
// the trimmed prefix has equal length on both sides).
func middleOps(a, b []string, off int) []op {
	if len(a)*len(b) > lcsCellCap {
		return replaceOps(a, b, off)
	}
	// Classic LCS dynamic program: lcs[i][j] = LCS length of a[i:], b[j:].
	lcs := make([][]int32, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	ops := make([]op, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', off + i, off + j})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{'-', off + i, -1})
			i++
		default:
			ops = append(ops, op{'+', -1, off + j})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, op{'-', off + i, -1})
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', -1, off + j})
	}
	return ops
}

// replaceOps emits delete-all + insert-all: the valid-but-not-minimal
// fallback for oversized middles.
func replaceOps(a, b []string, off int) []op {
	ops := make([]op, 0, len(a)+len(b))
	for i := range a {
		ops = append(ops, op{'-', off + i, -1})
	}
	for j := range b {
		ops = append(ops, op{'+', -1, off + j})
	}
	return ops
}

// hunk is a run of ops rendered under one @@ header.
type hunk struct {
	ops []op
}

// buildHunks groups changed ops with `context` unchanged lines around them,
// merging changes whose gap is small enough that their context would touch.
func buildHunks(ops []op) []hunk {
	// Indices of changed ops.
	changed := []int{}
	for i, o := range ops {
		if o.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var hunks []hunk
	start := changed[0]
	end := changed[0]
	flush := func() {
		lo := start - context
		if lo < 0 {
			lo = 0
		}
		hi := end + context
		if hi > len(ops)-1 {
			hi = len(ops) - 1
		}
		hunks = append(hunks, hunk{ops: ops[lo : hi+1]})
	}
	for _, idx := range changed[1:] {
		if idx-end <= 2*context {
			end = idx
			continue
		}
		flush()
		start, end = idx, idx
	}
	flush()
	return hunks
}

// hunkHeader renders "@@ -l,c +l,c @@" with 1-based starts. Per the unified
// format, a count of 1 omits the ",1". A count of 0 can only happen when
// that side's file is empty — any interior change carries context lines
// counted on both sides — so its position is always 0 ("0,0").
func hunkHeader(h hunk) string {
	aStart, aCount, bStart, bCount := 0, 0, 0, 0
	for _, o := range h.ops {
		if o.aIdx >= 0 {
			if aCount == 0 {
				aStart = o.aIdx + 1
			}
			aCount++
		}
		if o.bIdx >= 0 {
			if bCount == 0 {
				bStart = o.bIdx + 1
			}
			bCount++
		}
	}
	return fmt.Sprintf("@@ -%s +%s @@\n", side(aStart, aCount), side(bStart, bCount))
}

// side renders one half of a hunk header.
func side(start, count int) string {
	switch count {
	case 0:
		return "0,0"
	case 1:
		return fmt.Sprintf("%d", start)
	default:
		return fmt.Sprintf("%d,%d", start, count)
	}
}
