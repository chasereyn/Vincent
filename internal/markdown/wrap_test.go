// =============================================================================
// File: internal/markdown/wrap_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for the word-wrap engine itself: tokenize, coalesce, and
// wrapSpans, exercised directly rather than through a whole document —
// markdown_test.go and inline_test.go already cover those from Render.

package markdown

import "testing"

// TestTokenize_CollapsesInternalWhitespace pins that a run of spaces or a
// tab inside one span's text becomes a single space token.
func TestTokenize_CollapsesInternalWhitespace(t *testing.T) {
	toks := tokenize([]Span{{Style: Text, Text: "a   b\tc"}})
	var got []string
	for _, tk := range toks {
		got = append(got, tk.text)
	}
	want := []string{"a", " ", "b", " ", "c"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTokenize_SpaceKeepsOriginatingStyle is the fix for the bug an
// earlier version of this file had: a space INSIDE one styled span (an
// inline code span with an embedded space, e.g.) must carry that span's
// style, or a multi-word Code/Link/Bold run silently loses its
// background/underline on every space it contains.
func TestTokenize_SpaceKeepsOriginatingStyle(t *testing.T) {
	toks := tokenize([]Span{{Style: Code, Text: "go test"}})
	if len(toks) != 3 {
		t.Fatalf("tokens = %#v, want 3 (go, space, test)", toks)
	}
	for i, tk := range toks {
		if tk.style != Code {
			t.Fatalf("token %d (%q) style = %v, want Code", i, tk.text, tk.style)
		}
	}
	if !toks[1].space {
		t.Fatalf("token 1 (%q) should be the space token", toks[1].text)
	}
}

// TestTokenize_SpaceAtSpanBoundaryUsesThatSpansStyle proves a space that
// belongs to the FIRST of two adjacent spans (its source text ends in a
// literal space) takes that span's style, not the next span's — the
// boundary is a property of the source text, not something wrap.go should
// blend.
func TestTokenize_SpaceAtSpanBoundaryUsesThatSpansStyle(t *testing.T) {
	toks := tokenize([]Span{{Style: Text, Text: "plain "}, {Style: Bold, Text: "bold"}})
	if len(toks) != 3 {
		t.Fatalf("tokens = %#v, want 3", toks)
	}
	if toks[0].text != "plain" || toks[0].style != Text {
		t.Fatalf("token 0 = %#v, want {plain, Text}", toks[0])
	}
	if !toks[1].space || toks[1].style != Text {
		t.Fatalf("token 1 = %#v, want a Text-styled space", toks[1])
	}
	if toks[2].text != "bold" || toks[2].style != Bold {
		t.Fatalf("token 2 = %#v, want {bold, Bold}", toks[2])
	}
}

// TestWrapSpans_NoWrapNeededProducesOneCoalescedSpan proves that when
// content fits on one row untouched, wrapSpans hands back exactly the
// same span it was given — the fix above matters precisely because a
// multi-word Code span must round-trip whole when it isn't wrapped.
func TestWrapSpans_NoWrapNeededProducesOneCoalescedSpan(t *testing.T) {
	rows := wrapSpans([]Span{{Style: Code, Text: "go test ./..."}}, 80, nil)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rows)
	}
	if len(rows[0].Spans) != 1 {
		t.Fatalf("spans = %#v, want a single coalesced span", rows[0].Spans)
	}
	if got := rows[0].Spans[0]; got.Style != Code || got.Text != "go test ./..." {
		t.Fatalf("span = %#v, want {Code, \"go test ./...\"}", got)
	}
}

// TestWrapSpans_PrefixOccupiesEveryRow proves the prefix is prepended,
// unmodified, to every row wrapSpans produces — the invariant list's
// marker-swap and blockquote's repeating bar both depend on.
func TestWrapSpans_PrefixOccupiesEveryRow(t *testing.T) {
	prefix := []Span{{Style: Quote, Text: "▍ "}}
	content := []Span{{Style: Text, Text: "one two three four five six"}}
	rows := wrapSpans(content, 10, prefix)
	if len(rows) < 2 {
		t.Fatalf("expected wrapping across multiple rows, got %#v", rows)
	}
	for i, r := range rows {
		if len(r.Spans) < 1 || r.Spans[0] != prefix[0] {
			t.Fatalf("row %d spans = %#v, want to start with the prefix span", i, r.Spans)
		}
	}
}

// TestWrapSpans_OversizedWordGetsItsOwnRow proves a single token wider
// than the budget is never dropped or split, just placed alone.
func TestWrapSpans_OversizedWordGetsItsOwnRow(t *testing.T) {
	rows := wrapSpans([]Span{{Style: Text, Text: "short reallylongwordthatoverflows ok"}}, 6, nil)
	found := false
	for _, r := range rows {
		if r.Text() == "reallylongwordthatoverflows" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rows = %#v, did not find the oversized word on its own row", rowTexts(rows))
	}
}

// TestWrapSpans_EmptyContentProducesNoRows keeps an empty inline run (an
// empty paragraph, a heading with no text) from emitting a row.
func TestWrapSpans_EmptyContentProducesNoRows(t *testing.T) {
	if rows := wrapSpans(nil, 80, nil); rows != nil {
		t.Fatalf("rows = %#v, want nil", rows)
	}
	if rows := wrapSpans([]Span{{Style: Text, Text: "   "}}, 80, nil); rows != nil {
		t.Fatalf("whitespace-only content: rows = %#v, want nil", rows)
	}
}

// TestVisibleWidth sums rune counts across spans.
func TestVisibleWidth(t *testing.T) {
	got := visibleWidth([]Span{{Text: "abc"}, {Text: "de"}})
	if got != 5 {
		t.Fatalf("visibleWidth = %d, want 5", got)
	}
}
