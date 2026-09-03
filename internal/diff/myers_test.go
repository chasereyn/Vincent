// =============================================================================
// File: internal/diff/myers_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package diff

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// reconstructSide walks Parse's rows and rebuilds one side of the original
// text, using hasTrailingNL to decide whether to put the final newline
// back. It only produces the true original text when every line of that
// side actually appears in the diff — true whenever the context passed to
// Unified is generous enough to cover the whole (small) test fixture, which
// is exactly how every case below is built.
func reconstructSide(rows []Row, old bool, hasTrailingNL bool) string {
	var lines []string
	for _, r := range rows {
		switch {
		case r.Kind == KindContext:
			lines = append(lines, r.Text)
		case old && r.Kind == KindDeleted:
			lines = append(lines, r.Text)
		case !old && r.Kind == KindAdded:
			lines = append(lines, r.Text)
		}
	}
	s := strings.Join(lines, "\n")
	if hasTrailingNL {
		s += "\n"
	}
	return s
}

// TestUnified_RoundTrip checks that Unified produces text Parse can read
// back into rows whose old and new sides reconstruct the original inputs
// exactly. context is generous (10) relative to every fixture here (at
// most a handful of lines), so nothing falls outside the one hunk each case
// produces.
func TestUnified_RoundTrip(t *testing.T) {
	cases := []struct {
		name         string
		old, new     string
		oldNL, newNL bool
	}{
		{"insert at start", "b\nc\n", "a\nb\nc\n", true, true},
		{"insert in middle", "a\nc\n", "a\nb\nc\n", true, true},
		{"insert at end", "a\nb\n", "a\nb\nc\n", true, true},
		{"delete", "a\nb\nc\n", "a\nc\n", true, true},
		{"replace", "a\nb\nc\n", "a\nX\nc\n", true, true},
		{"no trailing newline", "a\nb\n", "a\nb\nc", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Unified("old.txt", "new.txt", []byte(c.old), []byte(c.new), 10)
			if out == "" {
				t.Fatalf("Unified produced no diff for a real change")
			}
			rows := Parse(out)

			gotOld := reconstructSide(rows, true, c.oldNL)
			if gotOld != c.old {
				t.Errorf("old side = %q, want %q\ndiff:\n%s", gotOld, c.old, out)
			}
			gotNew := reconstructSide(rows, false, c.newNL)
			if gotNew != c.new {
				t.Errorf("new side = %q, want %q\ndiff:\n%s", gotNew, c.new, out)
			}
		})
	}
}

// TestUnified_EmptyInputs is the "empty" table case: two empty texts have
// nothing to diff, so Unified must produce no output at all, the same as a
// real `git diff` between two files with no changes.
func TestUnified_EmptyInputs(t *testing.T) {
	if out := Unified("a", "b", nil, nil, 3); out != "" {
		t.Fatalf("Unified(empty, empty) = %q, want \"\"", out)
	}
}

// TestUnified_IdenticalInputs is the "identical" table case. Byte-identical
// text produces no hunks, matching real `git diff`'s "nothing to show" for
// an unchanged file — there is no round-trip to check because there is
// nothing in the diff to reconstruct FROM.
func TestUnified_IdenticalInputs(t *testing.T) {
	text := []byte("a\nb\nc\n")
	if out := Unified("a", "b", text, text, 3); out != "" {
		t.Fatalf("Unified(identical, identical) = %q, want \"\"", out)
	}
}

// TestUnified_NoNewlineMarker pins git's "\ No newline at end of file"
// convention: appending an unterminated line must produce the marker
// directly after that line, and ONLY after that line.
func TestUnified_NoNewlineMarker(t *testing.T) {
	out := Unified("old.txt", "new.txt", []byte("a\nb\n"), []byte("a\nb\nc"), 10)
	lines := strings.Split(out, "\n")
	found := -1
	for i, l := range lines {
		if l == "\\ No newline at end of file" {
			found = i
		}
	}
	if found <= 0 {
		t.Fatalf("no marker found in output:\n%s", out)
	}
	if lines[found-1] != "+c" {
		t.Errorf("marker followed %q, want it right after \"+c\"\noutput:\n%s", lines[found-1], out)
	}
	// The old side ends in a real newline, so there must be exactly one
	// marker — not one per side.
	count := strings.Count(out, "\\ No newline at end of file")
	if count != 1 {
		t.Errorf("marker appeared %d times, want 1\noutput:\n%s", count, out)
	}
}

// TestUnified_CRLFPreserved proves Unified doesn't strip \r itself — that
// stripping is Parse's job, for display, and is already pinned by
// TestParse_StripsCarriageReturns. Unified's own output must carry the
// carriage returns verbatim, the same as a real `git diff` of a CRLF file,
// or Parse would have nothing to strip.
func TestUnified_CRLFPreserved(t *testing.T) {
	out := Unified("old.txt", "new.txt", []byte("a\r\nb\r\n"), []byte("a\r\nX\r\n"), 10)
	if !strings.Contains(out, "-b\r") {
		t.Errorf("deletion lost its \\r: %q", out)
	}
	if !strings.Contains(out, "+X\r") {
		t.Errorf("addition lost its \\r: %q", out)
	}
	// Parse still does its documented stripping on the way out.
	rows := Parse(out)
	for _, r := range rows {
		if strings.ContainsRune(r.Text, '\r') {
			t.Errorf("Parse should have stripped \\r from %q", r.Text)
		}
	}
}

// TestUnified_HeaderNames pins the --- a/ / +++ b/ header shape the task
// asks for, and the /dev/null escape hatch for a side that doesn't exist.
func TestUnified_HeaderNames(t *testing.T) {
	out := Unified("note.txt", "note.txt", []byte("a\n"), []byte("b\n"), 3)
	if !strings.HasPrefix(out, "--- a/note.txt\n+++ b/note.txt\n") {
		t.Fatalf("header = %q", firstTwoLines(out))
	}

	out = Unified("/dev/null", "new.txt", nil, []byte("a\n"), 3)
	if !strings.HasPrefix(out, "--- /dev/null\n+++ b/new.txt\n") {
		t.Fatalf("header = %q", firstTwoLines(out))
	}
}

func firstTwoLines(s string) string {
	lines := strings.SplitN(s, "\n", 3)
	if len(lines) > 2 {
		lines = lines[:2]
	}
	return strings.Join(lines, "\n")
}

// TestUnified_FallsBackPastEditDistanceCap proves the cap documented on
// maxLineEditDistance actually fires: two files sharing no lines at all,
// with more than maxLineEditDistance/2 lines each, exceed the cap (the
// edit distance for wholly-disjoint content is old+new deletions/insertions,
// i.e. 2*n), and Unified must still return a correct, if non-minimal,
// diff — one hunk deleting everything old and inserting everything new —
// rather than hang or blow up memory finding the true shortest script.
func TestUnified_FallsBackPastEditDistanceCap(t *testing.T) {
	n := maxLineEditDistance/2 + 100
	oldLines := make([]string, n)
	newLines := make([]string, n)
	for i := range oldLines {
		oldLines[i] = "old-" + strconv.Itoa(i)
		newLines[i] = "new-" + strconv.Itoa(i)
	}
	oldText := strings.Join(oldLines, "\n") + "\n"
	newText := strings.Join(newLines, "\n") + "\n"

	out := Unified("old.txt", "new.txt", []byte(oldText), []byte(newText), 3)
	if out == "" {
		t.Fatal("expected a diff, got none")
	}
	rows := Parse(out)
	added, deleted := Stats(rows)
	if added != n || deleted != n {
		t.Fatalf("Stats = +%d -%d, want +%d -%d", added, deleted, n, n)
	}
}

// TestTokenWordRange_OneWordChangeTintsExactlyThatWord is the case the task
// calls out by name: an identifier changed in the middle of an otherwise
// identical line should tint that identifier and nothing either side of
// it, on both the deletion and the addition.
func TestTokenWordRange_OneWordChangeTintsExactlyThatWord(t *testing.T) {
	rows := Parse("@@ -1,1 +1,1 @@\n-result := computeTotal(items)\n+result := computeSum(items)\n")
	del, add := rows[0], rows[1]

	if !del.HasWordTint() {
		t.Fatal("deletion should carry a word tint")
	}
	if got := string([]rune(del.Text)[del.WordStart:del.WordEnd]); got != "computeTotal" {
		t.Errorf("deletion tinted %q, want %q", got, "computeTotal")
	}
	if got := string([]rune(add.Text)[add.WordStart:add.WordEnd]); got != "computeSum" {
		t.Errorf("addition tinted %q, want %q", got, "computeSum")
	}
}

// TestTokenWordRange_TwoSeparateEditsTintsBoth covers a line with two
// unrelated changes. The tint is still a single contiguous span (Row has no
// way to record two disjoint ranges), so the assertion is that both edited
// words fall inside it — the span runs from the first changed token to the
// last, which for two edits necessarily covers both, at the cost of also
// covering the untouched text between them.
func TestTokenWordRange_TwoSeparateEditsTintsBoth(t *testing.T) {
	rows := Parse("@@ -1,1 +1,1 @@\n-alpha := one + beta\n+alpha := two + gamma\n")
	del, add := rows[0], rows[1]

	if !del.HasWordTint() || !add.HasWordTint() {
		t.Fatalf("both sides should carry a tint: del=%+v add=%+v", del, add)
	}
	delTint := string([]rune(del.Text)[del.WordStart:del.WordEnd])
	if !strings.Contains(delTint, "one") || !strings.Contains(delTint, "beta") {
		t.Errorf("deletion tint %q should span both changed words (one, beta)", delTint)
	}
	addTint := string([]rune(add.Text)[add.WordStart:add.WordEnd])
	if !strings.Contains(addTint, "two") || !strings.Contains(addTint, "gamma") {
		t.Errorf("addition tint %q should span both changed words (two, gamma)", addTint)
	}
}

// TestAssignWordRanges_FallsBackOnLongLines proves the 400-rune guard
// actually routes to the prefix/suffix heuristic instead of silently
// skipping the tint altogether — a long line with a short, clearly bounded
// edit should still get a (heuristic) tint, just not from the token diff.
func TestAssignWordRanges_FallsBackOnLongLines(t *testing.T) {
	pad := strings.Repeat("x", 450)
	oldLine := pad + "AAA"
	newLine := pad + "BBB"
	rows := Parse(fmt.Sprintf("@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine))
	del, add := rows[0], rows[1]

	if !del.HasWordTint() {
		t.Fatal("long line should still fall back to a tint")
	}
	if got := string([]rune(del.Text)[del.WordStart:del.WordEnd]); got != "AAA" {
		t.Errorf("deletion tinted %q, want %q", got, "AAA")
	}
	if got := string([]rune(add.Text)[add.WordStart:add.WordEnd]); got != "BBB" {
		t.Errorf("addition tinted %q, want %q", got, "BBB")
	}
}

// TestAssignWordRanges_FallsBackOnManyTokens is the companion guard: a pair
// with more than 200 tokens on either side must not run the token diff,
// even when each individual line is short.
func TestAssignWordRanges_FallsBackOnManyTokens(t *testing.T) {
	var oldWords, newWords []string
	for i := 0; i < 210; i++ {
		oldWords = append(oldWords, "w")
		newWords = append(newWords, "w")
	}
	oldWords = append(oldWords, "AAA")
	newWords = append(newWords, "BBB")
	oldLine := strings.Join(oldWords, " ")
	newLine := strings.Join(newWords, " ")

	rows := Parse(fmt.Sprintf("@@ -1,1 +1,1 @@\n-%s\n+%s\n", oldLine, newLine))
	del, add := rows[0], rows[1]
	if !del.HasWordTint() || !add.HasWordTint() {
		t.Fatal("a too-wide pair should still fall back to the prefix/suffix tint")
	}
	if got := string([]rune(del.Text)[del.WordStart:del.WordEnd]); got != "AAA" {
		t.Errorf("deletion tinted %q, want %q", got, "AAA")
	}
}

// TestTokenize pins the token boundaries the word-level diff relies on:
// identifier runs, whitespace runs, and lone punctuation.
func TestTokenize(t *testing.T) {
	got := tokenize(`foo_bar(1, "x")`)
	want := []string{"foo_bar", "(", "1", ",", " ", `"`, "x", `"`, ")"}
	if len(got) != len(want) {
		t.Fatalf("tokenize(...) = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// BenchmarkUnified measures Unified on two 5,000-line inputs differing in
// 50 scattered single-line replacements — the shape a buffer-vs-disk diff
// takes on a large source file with a handful of real edits.
func BenchmarkUnified(b *testing.B) {
	const total = 5000
	const changed = 50
	oldLines := make([]string, total)
	newLines := make([]string, total)
	for i := 0; i < total; i++ {
		oldLines[i] = fmt.Sprintf("line %d unchanged content here", i)
		newLines[i] = oldLines[i]
	}
	step := total / changed
	for i := 0; i < changed; i++ {
		idx := i * step
		newLines[idx] = fmt.Sprintf("line %d WAS CHANGED by the benchmark", idx)
	}
	oldText := []byte(strings.Join(oldLines, "\n") + "\n")
	newText := []byte(strings.Join(newLines, "\n") + "\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Unified("old.go", "new.go", oldText, newText, 3)
	}
}

// BenchmarkAssignWordRanges measures the token-level tint over 1,000 paired
// deletion/addition rows, each a short line with a single changed word —
// the common case a diff view's word tint runs on every scroll.
func BenchmarkAssignWordRanges(b *testing.B) {
	const pairs = 1000
	rows := make([]Row, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		rows = append(rows,
			Row{Kind: KindDeleted, Text: fmt.Sprintf("value := computeTotal(items, %d)", i)},
			Row{Kind: KindAdded, Text: fmt.Sprintf("value := computeSum(items, %d)", i)},
		)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cp := make([]Row, len(rows))
		copy(cp, rows)
		assignWordRanges(cp)
	}
}
