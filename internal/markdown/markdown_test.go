// =============================================================================
// File: internal/markdown/markdown_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for the block-level row model: headings, code blocks, lists,
// block quotes, thematic breaks, and the blank-line spacing between
// top-level blocks. inline_test.go and wrap_test.go and table_test.go
// cover their own files.

package markdown

import (
	"strings"
	"testing"
)

// rowTexts is a small helper: the plain text of every row, for tests that
// only care what a document reads as, not how it's styled.
func rowTexts(rows []Row) []string {
	return Texts(rows)
}

// TestRender_HeadingLevelsMapToStyle pins that H1/H2 get their own style
// and H4-6 share Heading3 — the enum has no more room, so levels past 3
// must degrade rather than panic or silently vanish.
func TestRender_HeadingLevelsMapToStyle(t *testing.T) {
	src := "# one\n\n## two\n\n### three\n\n#### four\n"
	rows := Render([]byte(src), 80)
	want := []SpanStyle{Heading1, Heading2, Heading3, Heading3}
	var got []SpanStyle
	for _, r := range rows {
		if len(r.Spans) == 0 {
			continue // blank separator row
		}
		got = append(got, r.Spans[0].Style)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d heading rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("heading %d style = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestRender_HeadingHashesAreDropped proves the literal "#" markers never
// reach the row text — the Style carries the weight instead.
func TestRender_HeadingHashesAreDropped(t *testing.T) {
	rows := Render([]byte("### Section Title\n"), 80)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if strings.Contains(rows[0].Text(), "#") {
		t.Fatalf("heading row %q still contains a hash", rows[0].Text())
	}
	if rows[0].Text() != "Section Title" {
		t.Fatalf("heading row = %q, want %q", rows[0].Text(), "Section Title")
	}
}

// TestRender_BlankRowBetweenTopLevelBlocks pins the paragraph spacing a
// reader expects: two consecutive top-level blocks get one blank row
// between them, not zero and not two.
func TestRender_BlankRowBetweenTopLevelBlocks(t *testing.T) {
	rows := Render([]byte("first paragraph\n\nsecond paragraph\n"), 80)
	texts := rowTexts(rows)
	if len(texts) != 3 {
		t.Fatalf("rows = %v, want 3 (para, blank, para)", texts)
	}
	if texts[0] != "first paragraph" || texts[1] != "" || texts[2] != "second paragraph" {
		t.Fatalf("rows = %#v, want [first paragraph, \"\", second paragraph]", texts)
	}
}

// TestRender_ParagraphWraps proves a long paragraph is actually reflowed
// to the requested width rather than emitted as one long row.
func TestRender_ParagraphWraps(t *testing.T) {
	src := "one two three four five six seven eight nine ten"
	rows := Render([]byte(src), 12)
	if len(rows) < 3 {
		t.Fatalf("got %d rows wrapping to width 12, want at least 3: %v", len(rows), rowTexts(rows))
	}
	for i, r := range rows {
		if n := len([]rune(r.Text())); n > 12 {
			t.Fatalf("row %d (%q) is %d runes wide, want <= 12", i, r.Text(), n)
		}
	}
	// Rejoining the wrapped words must reproduce the original words in
	// order — wrapping must never drop or reorder content.
	var joined []string
	for _, r := range rows {
		joined = append(joined, strings.Fields(r.Text())...)
	}
	if got := strings.Join(joined, " "); got != src {
		t.Fatalf("rewrapped text = %q, want %q", got, src)
	}
}

// TestRender_FencedCodeBlockDoesNotWrapAndKeepsLanguage pins the two
// properties the painter depends on: every row of a fenced block carries
// its language, and a line longer than the render width is NOT broken —
// code's line breaks are meaningful and wrapping them would corrupt it.
func TestRender_FencedCodeBlockDoesNotWrapAndKeepsLanguage(t *testing.T) {
	longLine := strings.Repeat("x", 40)
	src := "```go\n" + longLine + "\nshort\n```\n"
	rows := Render([]byte(src), 10)
	var codeRows []Row
	for _, r := range rows {
		if r.CodeBlock {
			codeRows = append(codeRows, r)
		}
	}
	if len(codeRows) != 2 {
		t.Fatalf("got %d code-block rows, want 2: %#v", len(codeRows), rowTexts(rows))
	}
	if codeRows[0].Text() != longLine {
		t.Fatalf("first code row = %q, want the unwrapped %d-rune line", codeRows[0].Text(), len(longLine))
	}
	if codeRows[0].Lang != "go" || codeRows[1].Lang != "go" {
		t.Fatalf("code rows lost their language: %q, %q", codeRows[0].Lang, codeRows[1].Lang)
	}
	for _, r := range codeRows {
		if len(r.Spans) != 1 || r.Spans[0].Style != CodeBlock {
			t.Fatalf("code row spans = %#v, want a single CodeBlock span", r.Spans)
		}
	}
}

// TestRender_FencedCodeBlockNoLanguage proves an unlabeled fence still
// renders as a code block, just with an empty Lang the painter can treat
// as "no highlighter, flat style".
func TestRender_FencedCodeBlockNoLanguage(t *testing.T) {
	rows := Render([]byte("```\nplain\n```\n"), 80)
	if len(rows) != 1 || !rows[0].CodeBlock || rows[0].Lang != "" {
		t.Fatalf("got %#v, want one CodeBlock row with empty Lang", rows)
	}
}

// TestRender_UnorderedListMarker pins the bullet-list shape: every item
// gets "• " on its own first row and nothing else does.
func TestRender_UnorderedListMarker(t *testing.T) {
	rows := Render([]byte("- one\n- two\n- three\n"), 80)
	texts := rowTexts(rows)
	want := []string{"• one", "• two", "• three"}
	if len(texts) != len(want) {
		t.Fatalf("rows = %#v, want %#v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, texts[i], want[i])
		}
	}
}

// TestRender_OrderedListNumbering proves ordinals increment and respect a
// non-default start ("3. foo" starting an ordered list starts counting at
// 3, per CommonMark).
func TestRender_OrderedListNumbering(t *testing.T) {
	rows := Render([]byte("3. foo\n4. bar\n5. baz\n"), 80)
	texts := rowTexts(rows)
	want := []string{"3. foo", "4. bar", "5. baz"}
	if len(texts) != len(want) {
		t.Fatalf("rows = %#v, want %#v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, texts[i], want[i])
		}
	}
}

// TestRender_ListItemWrapKeepsMarkerOnFirstRowOnly is the one property
// the whole list/listItem prefix-swap machinery exists for: a wrapped
// item shows its marker exactly once, and every continuation row lines
// up under the text instead of repeating (or losing) the marker.
func TestRender_ListItemWrapKeepsMarkerOnFirstRowOnly(t *testing.T) {
	src := "- one two three four five six seven eight nine ten eleven twelve\n"
	rows := Render([]byte(src), 14)
	if len(rows) < 2 {
		t.Fatalf("expected the item to wrap across multiple rows, got %#v", rowTexts(rows))
	}
	if !strings.HasPrefix(rows[0].Text(), "• ") {
		t.Fatalf("first row %q does not start with the marker", rows[0].Text())
	}
	for i, r := range rows[1:] {
		if strings.Contains(r.Text(), "•") {
			t.Fatalf("continuation row %d (%q) repeats the marker", i+1, r.Text())
		}
		if !strings.HasPrefix(r.Text(), "  ") {
			t.Fatalf("continuation row %d (%q) is not indented to the marker's width", i+1, r.Text())
		}
	}
}

// TestRender_NestedListIndents pins that a nested list item gets more
// leading indentation than its parent's items.
func TestRender_NestedListIndents(t *testing.T) {
	rows := Render([]byte("- parent\n  - child\n"), 80)
	texts := rowTexts(rows)
	if len(texts) != 2 {
		t.Fatalf("rows = %#v, want 2", texts)
	}
	parentIndent := len(texts[0]) - len(strings.TrimLeft(texts[0], " "))
	childIndent := len(texts[1]) - len(strings.TrimLeft(texts[1], " "))
	if childIndent <= parentIndent {
		t.Fatalf("child indent %d is not deeper than parent indent %d (%q vs %q)",
			childIndent, parentIndent, texts[1], texts[0])
	}
}

// TestRender_BlockquoteBar pins that every line of a quote — including
// ones the wrapper produces from a single long source line — carries the
// left bar, and that a non-quoted paragraph does not.
func TestRender_BlockquoteBar(t *testing.T) {
	src := "> one two three four five six seven eight nine ten\n"
	rows := Render([]byte(src), 12)
	if len(rows) < 2 {
		t.Fatalf("expected the quote to wrap, got %#v", rowTexts(rows))
	}
	for i, r := range rows {
		if !strings.HasPrefix(r.Text(), "▍") {
			t.Fatalf("quote row %d (%q) is missing its bar", i, r.Text())
		}
		if len(r.Spans) == 0 || r.Spans[0].Style != Quote {
			t.Fatalf("quote row %d's first span is %#v, want Style Quote", i, r.Spans)
		}
	}
}

// TestRender_NestedBlockquoteDoublesBar proves a quote inside a quote
// gets two bars, falling out of blockquote's recursion rather than
// needing its own special case.
func TestRender_NestedBlockquoteDoublesBar(t *testing.T) {
	rows := Render([]byte("> > nested\n"), 80)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	if got := strings.Count(rows[0].Text(), "▍"); got != 2 {
		t.Fatalf("bar count = %d in %q, want 2", got, rows[0].Text())
	}
}

// TestRender_ThematicBreakFillsWidth pins the rule: one row, Style Rule,
// exactly as wide as requested.
func TestRender_ThematicBreakFillsWidth(t *testing.T) {
	rows := Render([]byte("---\n"), 20)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	if len(rows[0].Spans) != 1 || rows[0].Spans[0].Style != Rule {
		t.Fatalf("rule row spans = %#v, want a single Rule span", rows[0].Spans)
	}
	if n := len([]rune(rows[0].Text())); n != 20 {
		t.Fatalf("rule width = %d, want 20", n)
	}
}

// TestRender_EmptyDocument proves an empty or whitespace-only document
// renders to no rows rather than a slice of one empty Row — a caller
// building a Buffer from Texts still needs at least one line, but that's
// the editor layer's job (mirroring how diff tabs handle an empty diff),
// not this package's.
func TestRender_EmptyDocument(t *testing.T) {
	if rows := Render([]byte(""), 80); len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
	if rows := Render([]byte("\n\n\n"), 80); len(rows) != 0 {
		t.Fatalf("rows = %#v, want none", rows)
	}
}

// TestRender_WidthClampedToOne proves a non-positive width degrades to
// the narrowest legal wrap — one word per row, since a single word can't
// be split without hyphenation — instead of panicking or looping. A
// resize event landing between frames could plausibly hand Render a zero.
func TestRender_WidthClampedToOne(t *testing.T) {
	for _, w := range []int{0, -5} {
		rows := Render([]byte("hello world"), w)
		want := []string{"hello", "world"}
		texts := rowTexts(rows)
		if len(texts) != len(want) {
			t.Fatalf("width %d: rows = %#v, want %#v", w, texts, want)
		}
		for i := range want {
			if texts[i] != want[i] {
				t.Fatalf("width %d: row %d = %q, want %q", w, i, texts[i], want[i])
			}
		}
	}
}

// TestTexts_MatchesRowText proves the Texts helper (what backs a
// markdown tab's Buffer.Lines) agrees with each Row's own Text method.
func TestTexts_MatchesRowText(t *testing.T) {
	rows := Render([]byte("# h\n\npara\n"), 80)
	texts := Texts(rows)
	if len(texts) != len(rows) {
		t.Fatalf("Texts returned %d entries for %d rows", len(texts), len(rows))
	}
	for i, r := range rows {
		if texts[i] != r.Text() {
			t.Fatalf("Texts[%d] = %q, want %q", i, texts[i], r.Text())
		}
	}
}

// TestExpandTabs pins the tab-stop math a code-block line depends on.
func TestExpandTabs(t *testing.T) {
	cases := []struct {
		in   string
		stop int
		want string
	}{
		{"a\tb", 4, "a   b"},
		{"ab\tc", 4, "ab  c"},
		{"no tabs", 4, "no tabs"},
		{"", 4, ""},
	}
	for _, c := range cases {
		if got := expandTabs(c.in, c.stop); got != c.want {
			t.Fatalf("expandTabs(%q, %d) = %q, want %q", c.in, c.stop, got, c.want)
		}
	}
}

// TestItoa pins the hand-rolled integer formatter list's ordinal numbers
// depend on.
func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 123: "123"}
	for n, want := range cases {
		if got := itoa(n); got != want {
			t.Fatalf("itoa(%d) = %q, want %q", n, got, want)
		}
	}
}
