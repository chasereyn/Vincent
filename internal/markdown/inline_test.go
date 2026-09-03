// =============================================================================
// File: internal/markdown/inline_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// Tests for inline span extraction: bold, italic, inline code, links, and
// the restyle/plainText helpers they're built on.

package markdown

import "testing"

// spanStylesAndText flattens a row's spans to parallel (style, text)
// slices, which is what most of these tests want to assert against.
func spanStylesAndText(spans []Span) (styles []SpanStyle, texts []string) {
	for _, s := range spans {
		styles = append(styles, s.Style)
		texts = append(texts, s.Text)
	}
	return
}

// TestRender_BoldAndItalic pins that ** and _ map to distinct styles and
// that plain text around them stays Style Text.
func TestRender_BoldAndItalic(t *testing.T) {
	rows := Render([]byte("plain **bold** and _italic_ done"), 80)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	styles, texts := spanStylesAndText(rows[0].Spans)
	foundBold, foundItalic := false, false
	for i, st := range styles {
		switch st {
		case Bold:
			foundBold = true
			if texts[i] != "bold" {
				t.Fatalf("bold span text = %q, want %q", texts[i], "bold")
			}
		case Italic:
			foundItalic = true
			if texts[i] != "italic" {
				t.Fatalf("italic span text = %q, want %q", texts[i], "italic")
			}
		}
	}
	if !foundBold {
		t.Fatalf("no Bold span in %#v", rows[0].Spans)
	}
	if !foundItalic {
		t.Fatalf("no Italic span in %#v", rows[0].Spans)
	}
}

// TestRender_InlineCode pins that a backtick span gets Style Code and
// keeps its exact contents, unescaped.
func TestRender_InlineCode(t *testing.T) {
	rows := Render([]byte("run `go test ./...` now"), 80)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	found := false
	for _, s := range rows[0].Spans {
		if s.Style == Code {
			found = true
			if s.Text != "go test ./..." {
				t.Fatalf("code span = %q, want %q", s.Text, "go test ./...")
			}
		}
	}
	if !found {
		t.Fatal("no Code span found")
	}
}

// TestRender_LinkShowsTextThenDimmedURL pins the exact shape the spec
// asks for: the link's visible text as a Link-styled span (or spans, once
// wrapping tokenizes it), followed by the URL in brackets as plain text.
func TestRender_LinkShowsTextThenDimmedURL(t *testing.T) {
	rows := Render([]byte("[Vincent](https://example.com/vincent)"), 80)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	text := rows[0].Text()
	if text != "Vincent [https://example.com/vincent]" {
		t.Fatalf("row text = %q, want %q", text, "Vincent [https://example.com/vincent]")
	}
	// The visible text must be Link-styled; the bracketed URL must not be.
	styles, texts := spanStylesAndText(rows[0].Spans)
	sawLinkStyledVincent := false
	sawURLAsPlain := false
	for i, st := range styles {
		if st == Link && texts[i] == "Vincent" {
			sawLinkStyledVincent = true
		}
		if st == Text && texts[i] == " [https://example.com/vincent]" {
			sawURLAsPlain = true
		}
	}
	if !sawLinkStyledVincent {
		t.Fatalf("no Link-styled %q span in %#v", "Vincent", rows[0].Spans)
	}
	if !sawURLAsPlain {
		t.Fatalf("URL suffix is not a plain Text span in %#v", rows[0].Spans)
	}
}

// TestRender_AutoLink pins that a bare <https://...> autolink still
// becomes a Link span even with no separate label.
func TestRender_AutoLink(t *testing.T) {
	rows := Render([]byte("see <https://example.com>"), 80)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	found := false
	for _, s := range rows[0].Spans {
		if s.Style == Link && s.Text == "https://example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no Link span for the autolink in %#v", rows[0].Spans)
	}
}

// TestRestyle_OverwritesStyleKeepsText pins the Emphasis building block:
// restyle changes every span's Style and nothing about its Text.
func TestRestyle_OverwritesStyleKeepsText(t *testing.T) {
	in := []Span{{Style: Text, Text: "a"}, {Style: Code, Text: "b"}}
	out := restyle(in, Bold)
	if len(out) != 2 {
		t.Fatalf("got %d spans, want 2", len(out))
	}
	for i, s := range out {
		if s.Style != Bold {
			t.Fatalf("span %d style = %v, want Bold", i, s.Style)
		}
		if s.Text != in[i].Text {
			t.Fatalf("span %d text = %q, want %q", i, s.Text, in[i].Text)
		}
	}
	// restyle must not mutate its input.
	if in[0].Style != Text || in[1].Style != Code {
		t.Fatal("restyle mutated its input slice")
	}
}

// TestRender_BoldContainingCode is the documented simplification check:
// nesting doesn't get a combined style, but the text itself is never
// lost — restyle overwrites Code to Bold rather than dropping the span.
func TestRender_BoldContainingCode(t *testing.T) {
	rows := Render([]byte("**bold `code` here**"), 80)
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want 1", rowTexts(rows))
	}
	if got := rows[0].Text(); got != "bold code here" {
		t.Fatalf("row text = %q, want %q", got, "bold code here")
	}
	for _, s := range rows[0].Spans {
		if s.Style == Code {
			t.Fatalf("expected no surviving Code style inside bold, got span %#v", s)
		}
	}
}
