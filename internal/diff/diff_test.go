// =============================================================================
// File: internal/diff/diff_test.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package diff

import (
	"strings"
	"testing"
)

// sampleDiff is a realistic one-file, one-hunk `git diff`. Written as
// explicit concatenation rather than a raw string literal because the
// leading space on a context line is load-bearing and any editor or
// formatter that trims trailing whitespace would silently break the fixture.
var sampleDiff = strings.Join([]string{
	"diff --git a/src/app.go b/src/app.go",
	"index 1111111..2222222 100644",
	"--- a/src/app.go",
	"+++ b/src/app.go",
	"@@ -10,4 +10,5 @@ func main() {",
	` 	setup()`,
	`-	log.Print("starting up")`,
	`+	log.Print("starting")`,
	`+	// TODO: flag parsing`,
	` 	run()`,
	"",
}, "\n")

// TestParse_DropsHeadersAndNumbersRows pins the core of the parse: the git
// plumbing header is dropped entirely, and every surviving row carries the
// old and new line numbers it should, with zero standing for "not on this
// side of the diff".
func TestParse_DropsHeadersAndNumbersRows(t *testing.T) {
	rows := Parse(sampleDiff)

	want := []struct {
		kind Kind
		old  int
		new  int
		text string
	}{
		{KindContext, 10, 10, "\tsetup()"},
		{KindDeleted, 11, 0, "\tlog.Print(\"starting up\")"},
		{KindAdded, 0, 11, "\tlog.Print(\"starting\")"},
		{KindAdded, 0, 12, "\t// TODO: flag parsing"},
		{KindContext, 12, 13, "\trun()"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		got := rows[i]
		if got.Kind != w.kind || got.Old != w.old || got.New != w.new || got.Text != w.text {
			t.Errorf("row %d = {%v old=%d new=%d %q}, want {%v old=%d new=%d %q}",
				i, got.Kind, got.Old, got.New, got.Text, w.kind, w.old, w.new, w.text)
		}
	}
}

// TestParse_WordRangeCoversOnlyTheChangedMiddle is the test that matters for
// the look of the viewer: a line edited in place should tint just the part
// that differs, not the whole line. Here the two lines share the prefix
// `\tlog.Print("starting` and the suffix `")`, so only ` up` is tinted on
// the deletion and nothing at all on the addition.
func TestParse_WordRangeCoversOnlyTheChangedMiddle(t *testing.T) {
	rows := Parse(sampleDiff)
	del, add := rows[1], rows[2]

	if !del.HasWordTint() {
		t.Fatalf("deletion should carry a word tint: %+v", del)
	}
	if got := string([]rune(del.Text)[del.WordStart:del.WordEnd]); got != " up" {
		t.Errorf("deletion tinted %q, want %q", got, " up")
	}
	// The addition is the deletion minus " up", so prefix+suffix consume the
	// whole line and there is nothing left in the middle to tint.
	if add.HasWordTint() {
		t.Errorf("addition tinted %q, want no tint",
			string([]rune(add.Text)[add.WordStart:add.WordEnd]))
	}

	// The unpaired second addition must never pick up a range from the
	// pairing pass — it replaced nothing.
	if rows[3].HasWordTint() {
		t.Error("unpaired addition should have no word tint")
	}
}

// TestParse_UnrelatedPairIsNotTinted keeps the tint honest. Two lines with
// no common prefix or suffix were not an in-place edit, and painting a
// "changed middle" spanning the entire line would claim a precision the
// pairing heuristic does not have.
func TestParse_UnrelatedPairIsNotTinted(t *testing.T) {
	rows := Parse("@@ -1,1 +1,1 @@\n-alpha\n+zulu\n")
	for i, r := range rows {
		if r.HasWordTint() {
			t.Errorf("row %d (%q) should not be tinted", i, r.Text)
		}
	}
}

// TestParse_GapOnlyBetweenHunks checks the elision marker: two hunks get one
// separator between them, and a single hunk gets none. A leading separator
// would claim content was elided above the first hunk when none was.
func TestParse_GapOnlyBetweenHunks(t *testing.T) {
	one := Parse("@@ -1,1 +1,1 @@\n ctx\n")
	for _, r := range one {
		if r.Kind == KindGap {
			t.Fatal("single-hunk diff should have no gap row")
		}
	}

	two := Parse("@@ -1,1 +1,1 @@\n ctx\n@@ -9,1 +9,1 @@\n ctx2\n")
	gaps := 0
	for _, r := range two {
		if r.Kind == KindGap {
			gaps++
		}
	}
	if gaps != 1 {
		t.Fatalf("two-hunk diff produced %d gaps, want 1", gaps)
	}
	if two[1].Kind != KindGap {
		t.Errorf("gap should sit between the hunks, rows = %+v", two)
	}
}

// TestParse_KeepsBinaryNotice proves an unparseable-but-meaningful line
// survives as KindMeta. Dropping it would leave the viewer showing an empty
// pane for a file git clearly says has changed.
func TestParse_KeepsBinaryNotice(t *testing.T) {
	rows := Parse("diff --git a/x.png b/x.png\nBinary files a/x.png and b/x.png differ\n")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Kind != KindMeta {
		t.Errorf("kind = %v, want KindMeta", rows[0].Kind)
	}
	if !strings.Contains(rows[0].Text, "Binary files") {
		t.Errorf("text = %q, want the binary notice", rows[0].Text)
	}
}

// TestParse_BlankContextLineCountsAsContext covers the case that made the
// leading-space rule unreliable: a blank line inside a hunk arrives with its
// marker space stripped by anything that trims trailing whitespace. Treating
// it as context is what keeps the line numbers after it correct.
func TestParse_BlankContextLineCountsAsContext(t *testing.T) {
	rows := Parse("@@ -1,3 +1,3 @@\n a\n\n b\n")
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[1].Kind != KindContext || rows[1].Text != "" {
		t.Errorf("row 1 = %+v, want an empty context row", rows[1])
	}
	if rows[2].New != 3 {
		t.Errorf("row 2 new line = %d, want 3 — the blank line must advance the counter", rows[2].New)
	}
}

// TestParse_MalformedHunkHeaderKeepsGoing pins the tolerance contract: a
// header we can't read costs the numbers on its own hunk and nothing more.
// Returning an error instead would mean the user gets no diff at all, with
// no action available to fix it.
func TestParse_MalformedHunkHeaderKeepsGoing(t *testing.T) {
	rows := Parse("@@ garbage @@\n+added\n")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Kind != KindAdded || rows[0].Text != "added" {
		t.Errorf("row = %+v, want the addition to survive", rows[0])
	}
}

// TestParse_EmptyInput confirms the zero case is an empty slice rather than
// a panic or a phantom row.
func TestParse_EmptyInput(t *testing.T) {
	if rows := Parse(""); len(rows) != 0 {
		t.Fatalf("empty diff produced %d rows: %+v", len(rows), rows)
	}
}

// TestStatsAndMaxLineNo covers the two summary helpers the renderer and the
// status bar depend on.
func TestStatsAndMaxLineNo(t *testing.T) {
	rows := Parse(sampleDiff)

	added, deleted := Stats(rows)
	if added != 2 || deleted != 1 {
		t.Errorf("Stats = +%d −%d, want +2 −1", added, deleted)
	}
	// The largest number on either side is the new file's line 13.
	if got := MaxLineNo(rows); got != 13 {
		t.Errorf("MaxLineNo = %d, want 13", got)
	}
}

// TestRowForNewLine maps a file line back to its diff row, which is what
// makes a click on the editor's git gutter land in the right place.
func TestRowForNewLine(t *testing.T) {
	rows := Parse(sampleDiff)

	if got := RowForNewLine(rows, 11); got != 2 {
		t.Errorf("RowForNewLine(11) = %d, want 2 (the first addition)", got)
	}
	if got := RowForNewLine(rows, 10); got != 0 {
		t.Errorf("RowForNewLine(10) = %d, want 0 (a context row still matches)", got)
	}
	// Line 500 isn't in the diff at all.
	if got := RowForNewLine(rows, 500); got != -1 {
		t.Errorf("RowForNewLine(500) = %d, want -1", got)
	}
}

// TestFirstChangedRow proves a diff opens on its first real change rather
// than on the leading context git includes for orientation.
func TestFirstChangedRow(t *testing.T) {
	if got := FirstChangedRow(Parse(sampleDiff)); got != 1 {
		t.Errorf("FirstChangedRow = %d, want 1", got)
	}
	// All-context diffs have nothing to jump to; row 0 is the honest answer.
	if got := FirstChangedRow(Parse("@@ -1,1 +1,1 @@\n ctx\n")); got != 0 {
		t.Errorf("FirstChangedRow on an unchanged diff = %d, want 0", got)
	}
}

// TestTexts confirms the backing-buffer projection stays parallel to the
// rows — the invariant the renderer's style lookup relies on.
func TestTexts(t *testing.T) {
	rows := Parse(sampleDiff)
	texts := Texts(rows)
	if len(texts) != len(rows) {
		t.Fatalf("Texts returned %d entries for %d rows", len(texts), len(rows))
	}
	for i, r := range rows {
		if texts[i] != r.Text {
			t.Errorf("Texts[%d] = %q, want %q", i, texts[i], r.Text)
		}
	}
}
