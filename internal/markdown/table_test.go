// =============================================================================
// File: internal/markdown/table_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for GFM table rendering: box-drawing borders, column widths from
// content, and the shrink-to-fit that keeps a wide table inside the pane.

package markdown

import (
	"strings"
	"testing"
)

// tableSrc is a small three-column table reused by several tests.
const tableSrc = "| Name | Age | City |\n|---|---|---|\n| Al | 30 | NYC |\n| Bob | 9 | Springfield |\n"

// TestRender_TableHasBordersAndHeader pins the overall shape: a top
// border, a styled header row, a separator, one row per data row, and a
// bottom border — five structural rows plus two data rows.
func TestRender_TableHasBordersAndHeader(t *testing.T) {
	rows := Render([]byte(tableSrc), 80)
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6 (top, header, sep, 2 data, bottom): %#v", len(rows), rowTexts(rows))
	}
	borderStyle := func(r Row) bool {
		for _, s := range r.Spans {
			if s.Style != TableBorder {
				return false
			}
		}
		return len(r.Spans) > 0
	}
	if !borderStyle(rows[0]) {
		t.Fatalf("row 0 (%q) is not a pure border row", rows[0].Text())
	}
	if !borderStyle(rows[2]) {
		t.Fatalf("row 2 (%q) is not the header separator", rows[2].Text())
	}
	if !borderStyle(rows[5]) {
		t.Fatalf("row 5 (%q) is not the bottom border", rows[5].Text())
	}
	if !strings.Contains(rows[1].Text(), "Name") || !strings.Contains(rows[1].Text(), "Age") {
		t.Fatalf("header row = %q, missing column names", rows[1].Text())
	}
	foundHeaderStyle := false
	for _, s := range rows[1].Spans {
		if s.Style == TableHeader {
			foundHeaderStyle = true
		}
	}
	if !foundHeaderStyle {
		t.Fatalf("header row spans = %#v, want a TableHeader-styled span", rows[1].Spans)
	}
}

// TestRender_TableColumnWidthFromContent pins that a column sizes to its
// widest cell (here, "Springfield" in the City column), not to a fixed
// width — every row's border must therefore be the same total length.
func TestRender_TableColumnWidthFromContent(t *testing.T) {
	rows := Render([]byte(tableSrc), 80)
	widths := map[int]bool{}
	for _, r := range rows {
		widths[len([]rune(r.Text()))] = true
	}
	if len(widths) != 1 {
		t.Fatalf("row widths = %v, want all rows the same total width", widths)
	}
	if !strings.Contains(rows[4].Text(), "Springfield") {
		t.Fatalf("expected a data row to contain the widest cell, got %q", rows[4].Text())
	}
}

// TestColumnWidths_FloorsAtMinColWidth proves a column of short cells
// still gets at least minColWidth, so a one-letter column doesn't render
// as a seam.
func TestColumnWidths_FloorsAtMinColWidth(t *testing.T) {
	widths := columnWidths([]string{"x"}, [][]string{{"y"}})
	if len(widths) != 1 || widths[0] != minColWidth {
		t.Fatalf("widths = %v, want [%d]", widths, minColWidth)
	}
}

// TestColumnWidths_UsesWidestCellPerColumn proves each column is sized
// independently off its own widest cell, not the table's widest cell
// overall.
func TestColumnWidths_UsesWidestCellPerColumn(t *testing.T) {
	widths := columnWidths([]string{"a", "bb"}, [][]string{{"aaaaa", "b"}})
	if widths[0] != 5 {
		t.Fatalf("column 0 width = %d, want 5", widths[0])
	}
	if widths[1] != minColWidth {
		t.Fatalf("column 1 width = %d, want the floor %d", widths[1], minColWidth)
	}
}

// TestShrinkToFit_NarrowsWidestColumnFirst proves the shrink loop takes
// width from the widest column before touching a narrower one, as long as
// the wide column alone can absorb the needed reduction without dropping
// below the narrow one.
func TestShrinkToFit_NarrowsWidestColumnFirst(t *testing.T) {
	widths := []int{20, 5}
	got := shrinkToFit(append([]int{}, widths...), 22)
	if got[1] != 5 {
		t.Fatalf("narrow column shrank to %d, want untouched at 5", got[1])
	}
	if got[0] >= widths[0] {
		t.Fatalf("wide column did not shrink: got %d, started at %d", got[0], widths[0])
	}
	if got[0] < minColWidth {
		t.Fatalf("wide column shrank past the floor: got %d, floor %d", got[0], minColWidth)
	}
}

// TestShrinkToFit_ConvergesOnceColumnsAreClose proves that once the widest
// column has shrunk down to meet a narrower one, the loop starts
// alternating between them rather than favoring one forever — the two
// end up within one cell of each other rather than one column bottoming
// out at the floor while the other stays untouched.
func TestShrinkToFit_ConvergesOnceColumnsAreClose(t *testing.T) {
	got := shrinkToFit([]int{20, 5}, 15)
	if d := got[0] - got[1]; d < -1 || d > 1 {
		t.Fatalf("widths converged to %v, want the two within 1 cell of each other", got)
	}
	for i, w := range got {
		if w < minColWidth {
			t.Fatalf("column %d = %d, below the floor %d", i, w, minColWidth)
		}
	}
}

// TestShrinkToFit_StopsAtFloorEvenIfStillTooWide proves the loop
// terminates (rather than looping forever or going negative) when every
// column is already at minColWidth and the budget still can't be met.
func TestShrinkToFit_StopsAtFloorEvenIfStillTooWide(t *testing.T) {
	widths := []int{minColWidth, minColWidth, minColWidth}
	got := shrinkToFit(append([]int{}, widths...), 1)
	for i, w := range got {
		if w != minColWidth {
			t.Fatalf("column %d = %d, want it to stay at the floor %d", i, w, minColWidth)
		}
	}
}

// TestRender_WideTableShrinksToFitWidth proves an over-wide table's total
// row width never exceeds the requested render width, even though a
// table's cells themselves never wrap.
func TestRender_WideTableShrinksToFitWidth(t *testing.T) {
	src := "| " + strings.Repeat("a", 60) + " | " + strings.Repeat("b", 60) + " |\n|---|---|\n| x | y |\n"
	rows := Render([]byte(src), 40)
	for i, r := range rows {
		if n := len([]rune(r.Text())); n > 40 {
			t.Fatalf("row %d (%q) is %d runes wide, want <= 40", i, r.Text(), n)
		}
	}
}

// TestTableDataRow_PadsAndTruncatesToColumnWidth pins the exact cell
// framing: "│ " + left-aligned, padded/truncated content + " │ " between
// columns and " │" at the end.
func TestTableDataRow_PadsAndTruncatesToColumnWidth(t *testing.T) {
	spans := tableDataRow([]string{"ab", "toolong"}, []int{4, 3}, Text)
	got := (Row{Spans: spans}).Text()
	want := "│ ab   │ too │"
	if got != want {
		t.Fatalf("row text = %q, want %q", got, want)
	}
}
