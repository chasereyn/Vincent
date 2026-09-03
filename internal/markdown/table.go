// =============================================================================
// File: internal/markdown/table.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// table.go renders a GFM table with box-drawing borders and column widths
// taken from content, per CLAUDE.md's spec for this phase. Cells don't
// wrap — see the package doc comment in markdown.go for why that's an
// accepted limitation rather than a TODO.

package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

// minColWidth is the floor a column is shrunk to before the shrink loop
// gives up on making an over-wide table fit. Narrower than this and a
// header word starts truncating into illegibility, which teaches the
// reader nothing a too-wide table didn't already.
const minColWidth = 3

// tableCellText extracts one cell's plain text — no nested styling, same
// simplification a heading gets, and for the same reason: a cell is
// sized in whole characters, and a bold word mid-cell would have to be
// tracked separately from the width math that sizing depends on.
func tableCellText(cell *extast.TableCell, source []byte) string {
	return plainText(cell, source)
}

// tableRowCells walks the TableCell children of a table row (or header)
// node and returns their plain text.
func tableRowCells(row ast.Node, source []byte) []string {
	var cells []string
	for c := row.FirstChild(); c != nil; c = c.NextSibling() {
		if cell, ok := c.(*extast.TableCell); ok {
			cells = append(cells, tableCellText(cell, source))
		}
	}
	return cells
}

// columnWidths returns the content width of every column: the widest
// cell in that column across the header and every body row, floored at
// minColWidth so a column of empty or one-character cells still reads as
// a column and not a seam.
func columnWidths(header []string, body [][]string) []int {
	n := len(header)
	for _, row := range body {
		if len(row) > n {
			n = len(row)
		}
	}
	widths := make([]int, n)
	grow := func(cells []string) {
		for i, c := range cells {
			if w := len([]rune(c)); w > widths[i] {
				widths[i] = w
			}
		}
	}
	grow(header)
	for _, row := range body {
		grow(row)
	}
	for i := range widths {
		if widths[i] < minColWidth {
			widths[i] = minColWidth
		}
	}
	return widths
}

// shrinkToFit reduces the widest columns, one cell at a time, until the
// table's total width (borders included) fits budget or every column has
// hit minColWidth. Bounded by the sum of all the width it could possibly
// remove, so a pathological input degrades to "as narrow as it gets"
// rather than looping.
func shrinkToFit(widths []int, budget int) []int {
	total := func() int {
		n := 1
		for _, w := range widths {
			n += w + 3 // " x " plus the trailing "│"
		}
		return n
	}
	for total() > budget {
		widest := -1
		for i, w := range widths {
			if w > minColWidth && (widest == -1 || w > widths[widest]) {
				widest = i
			}
		}
		if widest == -1 {
			break // every column is already at the floor.
		}
		widths[widest]--
	}
	return widths
}

// tableBorder builds one border row ("┌─┬─┐" style) from the column
// widths and the three junction/end runes to use.
func tableBorder(widths []int, left, mid, right rune) string {
	var b strings.Builder
	b.WriteRune(left)
	for i, w := range widths {
		if i > 0 {
			b.WriteRune(mid)
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteRune(right)
	return b.String()
}

// tableDataRow builds one "│ cell │ cell │" row. Cells are left-aligned
// and padded/truncated to their column's width; cellStyle picks Text for
// a body row and TableHeader for the header row.
func tableDataRow(cells []string, widths []int, cellStyle SpanStyle) []Span {
	spans := []Span{{Style: TableBorder, Text: "│ "}}
	for i, w := range widths {
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		runes := []rune(text)
		if len(runes) > w {
			runes = runes[:w]
		}
		padded := string(runes) + strings.Repeat(" ", w-len(runes))
		spans = append(spans, Span{Style: cellStyle, Text: padded})
		if i < len(widths)-1 {
			spans = append(spans, Span{Style: TableBorder, Text: " │ "})
		}
	}
	spans = append(spans, Span{Style: TableBorder, Text: " │"})
	return spans
}

// table renders a GFM table: a top border, the header row, a header
// separator, every body row, and a bottom border — each prefixed with
// prefix like every other block, so a table nested in a quote or list
// still carries its bar or indent.
func (b *builder) table(n *extast.Table, prefix []Span) {
	var header []string
	var body [][]string
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *extast.TableHeader:
			header = tableRowCells(v, b.source)
		case *extast.TableRow:
			body = append(body, tableRowCells(v, b.source))
		}
	}
	if header == nil && len(body) == 0 {
		return
	}
	widths := columnWidths(header, body)
	avail := b.width - visibleWidth(prefix)
	if avail > 0 {
		widths = shrinkToFit(widths, avail)
	}

	row := func(spans []Span) {
		b.rows = append(b.rows, Row{Spans: append(append([]Span{}, prefix...), spans...)})
	}
	borderSpan := func(s string) []Span { return []Span{{Style: TableBorder, Text: s}} }

	row(borderSpan(tableBorder(widths, '┌', '┬', '┐')))
	if header != nil {
		row(tableDataRow(header, widths, TableHeader))
		row(borderSpan(tableBorder(widths, '├', '┼', '┤')))
	}
	for _, r := range body {
		row(tableDataRow(r, widths, Text))
	}
	row(borderSpan(tableBorder(widths, '└', '┴', '┘')))
}
