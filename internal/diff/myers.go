// =============================================================================
// File: internal/diff/myers.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream equivalent. Adds an in-process Myers diff so Vincent can build
// its own unified-diff text (for buffer-vs-disk comparisons) instead of
// shelling out to `git diff --no-index`, and so the word-level tint in
// assignWordRanges can use a real diff instead of a prefix/suffix guess.
// =============================================================================

package diff

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxLineEditDistance bounds how much work Unified will do reconstructing an
// exact edit script between two texts. The classic O(ND) algorithm needs
// O(D^2) memory to keep enough history to backtrack a path of length D — for
// two files that are genuinely unrelated (D approaching the total line
// count) that quadratic term stops being a rounding error, and Vincent has
// no business spending seconds or hundreds of megabytes proving that a file
// was rewritten wholesale. Past this cap, Unified stops trying to find the
// minimal edit script and reports the whole file as replaced instead — the
// +/- markers stay correct, only the "smallest possible diff" property is
// given up, and only on inputs large and different enough that a human
// would describe them as "a different file" anyway.
const maxLineEditDistance = 2000

// editKind classifies one step of a Myers edit script.
type editKind int

const (
	// opEqual is a token or line present, unmoved, on both sides.
	opEqual editKind = iota
	// opDelete is present only on the "old" side.
	opDelete
	// opInsert is present only on the "new" side.
	opInsert
)

// editOp is one element of the edit script myersDiff returns: which side it
// came from, and the index into that side's slice. aIdx is meaningful for
// opEqual and opDelete; bIdx is meaningful for opEqual and opInsert.
type editOp struct {
	kind editKind
	aIdx int
	bIdx int
}

// myersDiff computes a shortest edit script turning a into b, using the
// classic O(ND) algorithm (Myers, 1986): a forward search over the edit
// graph's diagonals, one round per increasing edit distance D, backtracked
// once the search reaches the far corner.
//
// maxD caps how many rounds the search runs before giving up; ok is false
// when the true edit distance exceeds it, and callers must not use the
// (nil) result in that case. Passing maxD <= 0 removes the cap. Each round's
// diagonal state is snapshotted at only the width that round could possibly
// need (2*d+1 entries) rather than the full working array, which is what
// keeps the memory cost O(D^2) instead of O(D*(len(a)+len(b))) — the
// difference between "fine even near the cap" and "a 5,000-line file walks
// this into hundreds of megabytes".
func myersDiff[T comparable](a, b []T, maxD int) ([]editOp, bool) {
	n, m := len(a), len(b)
	total := n + m
	if total == 0 {
		return nil, true
	}
	if maxD <= 0 || maxD > total {
		maxD = total
	}

	// offset shifts a diagonal index k (which ranges over negative and
	// positive values) into a valid slice index. It is fixed for the whole
	// search so the same v buffer can be reused across every round.
	offset := maxD + 1
	v := make([]int, 2*maxD+2)

	trace := make([][]int, 0, maxD+1)
	found := -1
	for d := 0; d <= maxD && found < 0; d++ {
		// Snapshot the state as it stood BEFORE this round runs (i.e. the
		// result of round d-1), trimmed to the width round d can read from.
		// See backtrackOps for why this narrower slice is always enough.
		snap := make([]int, 2*d+1)
		copy(snap, v[offset-d:offset+d+1])
		trace = append(trace, snap)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+offset] < v[k+1+offset]) {
				x = v[k+1+offset]
			} else {
				x = v[k-1+offset] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+offset] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
	}
	if found < 0 {
		return nil, false
	}
	return backtrackOps(a, b, trace, found), true
}

// backtrackOps walks the round-by-round snapshots myersDiff recorded, from
// the endpoint (len(a), len(b)) back to the origin, and turns the path into
// an edit script in forward order.
//
// Each trace[d] holds only entries for diagonals k with |k| <= d, indexed
// locally as k+d. That is always enough EXCEPT at d == 0: the forward
// search's own k==-d / k==d special casing means a middle k's two
// neighbours (k-1, k+1) always have magnitude at most d-1 — the state
// round d-1 last wrote — with one deliberate exception. Round 0's only
// diagonal, k=0, is simultaneously "k==-d" and "k==d", and the formula
// resolves that by reading the algorithm's seed value at the imaginary
// diagonal k=1 (pre-seeded to x=0 so the very first snake can start at the
// origin) rather than anything round -1 could have written. That seed is
// handled directly below instead of through trace[0], which only ever
// covers k=0.
func backtrackOps[T comparable](a, b []T, trace [][]int, found int) []editOp {
	x, y := len(a), len(b)
	var ops []editOp
	for d := found; d >= 0; d-- {
		var prevK, prevX int
		if d == 0 {
			prevK, prevX = 1, 0
		} else {
			v := trace[d]
			loffset := d
			k := x - y
			if k == -d || (k != d && v[k-1+loffset] < v[k+1+loffset]) {
				prevK = k + 1
			} else {
				prevK = k - 1
			}
			prevX = v[prevK+loffset]
		}
		prevY := prevX - prevK

		for x > prevX && y > prevY {
			ops = append(ops, editOp{kind: opEqual, aIdx: x - 1, bIdx: y - 1})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				ops = append(ops, editOp{kind: opInsert, bIdx: y - 1})
			} else {
				ops = append(ops, editOp{kind: opDelete, aIdx: x - 1})
			}
		}
		x, y = prevX, prevY
	}
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}

// opGroup is a run of consecutive same-kind edit ops, expressed as
// half-open ranges into the old and new sequences. It plays the role
// Python's difflib calls an "opcode": every group, including the
// delete-only and insert-only ones, carries a position on BOTH sides (a
// zero-width range on the side it doesn't touch), which is what lets the
// hunk-grouping math below treat all three kinds uniformly.
type opGroup struct {
	kind       editKind
	aFrom, aTo int
	bFrom, bTo int
}

// groupOps collapses a flat, one-token/line-at-a-time edit script into runs,
// tracking running old/new cursors so even a delete-only or insert-only run
// gets a well-defined position on the side it doesn't consume.
func groupOps(ops []editOp) []opGroup {
	var groups []opGroup
	oldPos, newPos := 0, 0
	for _, op := range ops {
		switch op.kind {
		case opEqual:
			if n := len(groups); n > 0 && groups[n-1].kind == opEqual {
				groups[n-1].aTo++
				groups[n-1].bTo++
			} else {
				groups = append(groups, opGroup{kind: opEqual, aFrom: oldPos, aTo: oldPos + 1, bFrom: newPos, bTo: newPos + 1})
			}
			oldPos++
			newPos++
		case opDelete:
			if n := len(groups); n > 0 && groups[n-1].kind == opDelete {
				groups[n-1].aTo++
			} else {
				groups = append(groups, opGroup{kind: opDelete, aFrom: oldPos, aTo: oldPos + 1, bFrom: newPos, bTo: newPos})
			}
			oldPos++
		case opInsert:
			if n := len(groups); n > 0 && groups[n-1].kind == opInsert {
				groups[n-1].bTo++
			} else {
				groups = append(groups, opGroup{kind: opInsert, aFrom: oldPos, aTo: oldPos, bFrom: newPos, bTo: newPos + 1})
			}
			newPos++
		}
	}
	return groups
}

// groupedHunks buckets opGroups into hunks the way GNU diff and Python's
// difflib.get_grouped_opcodes do: each hunk keeps at most n lines of
// context before and after its changes, and two change regions merge into
// one hunk whenever the untouched gap between them is 2n lines or smaller.
// A diff with no changes at all yields no hunks.
func groupedHunks(groups []opGroup, n int) [][]opGroup {
	if len(groups) == 0 {
		return nil
	}
	codes := make([]opGroup, len(groups))
	copy(codes, groups)

	if codes[0].kind == opEqual {
		g := codes[0]
		g.aFrom = max(g.aFrom, g.aTo-n)
		g.bFrom = max(g.bFrom, g.bTo-n)
		codes[0] = g
	}
	if last := len(codes) - 1; codes[last].kind == opEqual {
		g := codes[last]
		g.aTo = min(g.aTo, g.aFrom+n)
		g.bTo = min(g.bTo, g.bFrom+n)
		codes[last] = g
	}

	nn := n + n
	var hunks [][]opGroup
	var cur []opGroup
	for _, g := range codes {
		if g.kind == opEqual && g.aTo-g.aFrom > nn {
			trimmed := g
			trimmed.aTo = min(g.aTo, g.aFrom+n)
			trimmed.bTo = min(g.bTo, g.bFrom+n)
			cur = append(cur, trimmed)
			hunks = append(hunks, cur)
			cur = nil
			g.aFrom = max(g.aFrom, g.aTo-n)
			g.bFrom = max(g.bFrom, g.bTo-n)
		}
		cur = append(cur, g)
	}
	if len(cur) > 0 && !(len(cur) == 1 && cur[0].kind == opEqual) {
		hunks = append(hunks, cur)
	}
	return hunks
}

// splitLines splits file content into lines with their newlines removed,
// and separately reports whether the content ended in a newline. That
// second value is what makes it possible to reproduce git's
// "\ No newline at end of file" marker: strings.Split alone can't tell
// "ends in \n" from "doesn't", and the two are visibly different on disk.
//
// An empty file is reported as zero lines with endsWithNewline=false: there
// is no trailing-newline question to ask about a file with no content.
func splitLines(text []byte) (lines []string, endsWithNewline bool) {
	if len(text) == 0 {
		return nil, false
	}
	s := string(text)
	if endsWithNewline = strings.HasSuffix(s, "\n"); endsWithNewline {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n"), endsWithNewline
}

// headerName formats one side of a Unified diff's --- / +++ header line,
// prefixing it the way git does (a/ for the old side, b/ for the new)
// unless the caller passed the literal "/dev/null" for a side that doesn't
// exist.
func headerName(prefix, name string) string {
	if name == "/dev/null" {
		return name
	}
	return prefix + "/" + name
}

// formatRange renders a half-open line range [start, stop) in the unified
// diff hunk-header convention: a single number when the range is exactly
// one line, "start,length" otherwise, and — for a zero-length range, which
// is how a pure insertion or pure deletion is expressed on the side it
// doesn't touch — a start one less than usual, per the unified diff spec.
func formatRange(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return strconv.Itoa(beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}

// writeNoNewlineMarker appends git's "\ No newline at end of file" line
// when the row just written was the last line of a side that doesn't end in
// a newline. i (old-file index) and j (new-file index) are -1 when the row
// just written doesn't exist on that side; at most one marker is ever
// written per row, since a context row is one physical line even when both
// of its indices happen to be the final line of a newline-less file.
func writeNoNewlineMarker(b *strings.Builder, i, j int, oldLines, newLines []string, oldNL, newNL bool) {
	if i >= 0 && i == len(oldLines)-1 && !oldNL {
		b.WriteString("\\ No newline at end of file\n")
		return
	}
	if j >= 0 && j == len(newLines)-1 && !newNL {
		b.WriteString("\\ No newline at end of file\n")
	}
}

// writeHunk renders one hunk's header and body onto b.
func writeHunk(b *strings.Builder, groups []opGroup, oldLines, newLines []string, oldNL, newNL bool) {
	first, last := groups[0], groups[len(groups)-1]
	fmt.Fprintf(b, "@@ -%s +%s @@\n", formatRange(first.aFrom, last.aTo), formatRange(first.bFrom, last.bTo))

	for _, g := range groups {
		switch g.kind {
		case opEqual:
			for i := g.aFrom; i < g.aTo; i++ {
				j := g.bFrom + (i - g.aFrom)
				b.WriteString(" ")
				b.WriteString(oldLines[i])
				b.WriteString("\n")
				writeNoNewlineMarker(b, i, j, oldLines, newLines, oldNL, newNL)
			}
		case opDelete:
			for i := g.aFrom; i < g.aTo; i++ {
				b.WriteString("-")
				b.WriteString(oldLines[i])
				b.WriteString("\n")
				writeNoNewlineMarker(b, i, -1, oldLines, newLines, oldNL, newNL)
			}
		case opInsert:
			for j := g.bFrom; j < g.bTo; j++ {
				b.WriteString("+")
				b.WriteString(newLines[j])
				b.WriteString("\n")
				writeNoNewlineMarker(b, -1, j, oldLines, newLines, oldNL, newNL)
			}
		}
	}
}

// Unified computes an in-process diff between oldText and newText and
// renders it as unified-diff text — the same shape `git diff` produces, and
// byte-for-byte parseable by Parse in this package. oldName and newName
// name the two sides in the --- a/ / +++ b/ header; pass "/dev/null" for a
// side that doesn't exist.
//
// context is how many unchanged lines to keep around each change, same
// meaning as `git diff -U<n>`. When the two texts are identical, Unified
// returns "" — there is nothing to show, the same as a real `git diff`
// between two unchanged files.
//
// The edit script comes from myersDiff. When the true edit distance would
// exceed maxLineEditDistance, Unified gives up on finding the minimal
// script and reports the whole file as one deletion followed by one
// insertion — see that constant's comment for why.
func Unified(oldName, newName string, oldText, newText []byte, context int) string {
	if context < 0 {
		context = 0
	}
	oldLines, oldNL := splitLines(oldText)
	newLines, newNL := splitLines(newText)

	var groups []opGroup
	if ops, ok := myersDiff(withTerminator(oldLines, oldNL), withTerminator(newLines, newNL), maxLineEditDistance); ok {
		groups = groupOps(ops)
	} else {
		if len(oldLines) > 0 {
			groups = append(groups, opGroup{kind: opDelete, aFrom: 0, aTo: len(oldLines)})
		}
		if len(newLines) > 0 {
			groups = append(groups, opGroup{kind: opInsert, bFrom: 0, bTo: len(newLines)})
		}
	}

	hunks := groupedHunks(groups, context)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("--- " + headerName("a", oldName) + "\n")
	b.WriteString("+++ " + headerName("b", newName) + "\n")
	for _, h := range hunks {
		writeHunk(&b, h, oldLines, newLines, oldNL, newNL)
	}
	return b.String()
}

// withTerminator returns lines with a newline appended to the last element
// when the text ended in one, for the line comparison only. Without it two
// texts that differ solely in a trailing newline split into identical line
// slices, the diff comes back empty, and the conflict prompt's Show diff
// would report "no difference" for a change the byte comparison just
// flagged. The output side still prints the original lines, so the marker
// logic in writeHunk is unaffected.
func withTerminator(lines []string, endsWithNewline bool) []string {
	if !endsWithNewline || len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	out[len(out)-1] += "\n"
	return out
}

// --- Token-level diff, used by assignWordRanges in diff.go ---

// maxTintLineRunes and maxTintTokens are the guards assignWordRanges uses
// to decide whether a paired deletion/addition is cheap enough to run the
// token-level diff on. A minified line or a generated one-liner can be
// thousands of runes long; tokenising and diffing it on every frame the
// diff view scrolls past it would be a real cost for a cosmetic tint, so
// past either threshold the row falls back to the plain prefix/suffix
// heuristic instead.
const (
	maxTintLineRunes = 400
	maxTintTokens    = 200
)

// tokenize splits a line into the units the word-level tint diffs against:
// a maximal run of letters/digits/underscore, a maximal run of whitespace,
// or a single punctuation rune. Grouping identifier characters and
// whitespace into runs (rather than one token per rune) is what makes a
// renamed variable or a re-indented line diff as one changed token instead
// of a scatter of single-character edits.
func tokenize(s string) []string {
	runes := []rune(s)
	var tokens []string
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch {
		case isWordRune(r):
			j := i + 1
			for j < len(runes) && isWordRune(runes[j]) {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		case unicode.IsSpace(r):
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		default:
			tokens = append(tokens, string(r))
			i++
		}
	}
	return tokens
}

// isWordRune reports whether r belongs in an identifier-like token: a
// letter, a digit, or underscore.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// runeOffsets returns, for a slice of tokens, the rune offset at which
// each token starts, plus one trailing entry for the end of the last
// token — so offsets[i] and offsets[i+1] bound token i in rune indices
// without a second pass over the string.
func runeOffsets(tokens []string) []int {
	offsets := make([]int, len(tokens)+1)
	for i, t := range tokens {
		offsets[i+1] = offsets[i] + utf8.RuneCountInString(t)
	}
	return offsets
}

// tokenWordRange finds, for one paired deletion/addition line, the
// tightest rune range on each side that spans every changed token, using a
// token-level Myers diff. ok is false — meaning the caller should fall back
// to the prefix/suffix heuristic — when either line is too long, either
// side has too many tokens, or (same as the old heuristic's rule) the two
// lines share no token at all: a pair with nothing in common isn't "an
// edit", and claiming the whole line as the changed part says nothing the
// row-level colour hasn't already said.
func tokenWordRange(oldText, newText string) (delStart, delEnd, addStart, addEnd int, ok bool) {
	if utf8.RuneCountInString(oldText) > maxTintLineRunes || utf8.RuneCountInString(newText) > maxTintLineRunes {
		return 0, 0, 0, 0, false
	}
	oldTokens := tokenize(oldText)
	newTokens := tokenize(newText)
	if len(oldTokens) > maxTintTokens || len(newTokens) > maxTintTokens {
		return 0, 0, 0, 0, false
	}

	// Bounded by maxTintTokens*2 above, so this is always cheap and always
	// finds the true edit distance — no fallback path needed here.
	ops, computed := myersDiff(oldTokens, newTokens, len(oldTokens)+len(newTokens))
	if !computed {
		return 0, 0, 0, 0, false
	}

	delFirst, delLast := -1, -1
	addFirst, addLast := -1, -1
	sharedToken := false
	for _, op := range ops {
		switch op.kind {
		case opEqual:
			sharedToken = true
		case opDelete:
			if delFirst == -1 {
				delFirst = op.aIdx
			}
			delLast = op.aIdx
		case opInsert:
			if addFirst == -1 {
				addFirst = op.bIdx
			}
			addLast = op.bIdx
		}
	}
	if !sharedToken {
		return 0, 0, 0, 0, false
	}

	oldOffsets := runeOffsets(oldTokens)
	newOffsets := runeOffsets(newTokens)
	if delFirst >= 0 {
		delStart, delEnd = oldOffsets[delFirst], oldOffsets[delLast+1]
	}
	if addFirst >= 0 {
		addStart, addEnd = newOffsets[addFirst], newOffsets[addLast+1]
	}
	return delStart, delEnd, addStart, addEnd, true
}

// assignPrefixSuffixRange is the original word-tint heuristic: compare a
// paired deletion and addition by common prefix and common suffix, and
// tint whatever's left in the middle. It is now only the fallback for rows
// the token diff opts out of (see tokenWordRange), but it is unchanged from
// when it was the only algorithm, including its "nothing shared -> no
// tint" rule.
func assignPrefixSuffixRange(del, add *Row) {
	oldRunes := []rune(del.Text)
	newRunes := []rune(add.Text)

	prefix := 0
	for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldRunes)-prefix && suffix < len(newRunes)-prefix &&
		oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
		suffix++
	}
	if prefix+suffix == 0 {
		return
	}
	del.WordStart, del.WordEnd = prefix, len(oldRunes)-suffix
	add.WordStart, add.WordEnd = prefix, len(newRunes)-suffix
}
