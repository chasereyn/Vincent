// =============================================================================
// File: internal/markdown/inline.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// inline.go turns an inline-content AST subtree (the children of a
// paragraph, heading, or table cell) into a flat []Span, and plainText
// gives the same content as one un-styled string for callers — headings
// and table cells — that don't want nested styling at all.

package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

// inline renders every inline child of n into a flat, ordered []Span.
func (b *builder) inline(n ast.Node) []Span {
	var spans []Span
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		spans = append(spans, b.inlineNode(c)...)
	}
	return spans
}

// inlineNode renders one inline node. Container nodes (Emphasis, Link)
// recurse into inline on themselves; leaf nodes (Text, CodeSpan) produce
// one Span directly.
func (b *builder) inlineNode(n ast.Node) []Span {
	switch v := n.(type) {
	case *ast.Text:
		s := string(v.Value(b.source))
		if v.SoftLineBreak() || v.HardLineBreak() {
			// Reflowing to our own width makes the source's line breaks
			// meaningless; a soft or hard break becomes the word-space it
			// would render as in any other markdown viewer.
			s += " "
		}
		if s == "" {
			return nil
		}
		return []Span{{Style: Text, Text: s}}
	case *ast.String:
		s := string(v.Value)
		if s == "" {
			return nil
		}
		return []Span{{Style: Text, Text: s}}
	case *ast.Emphasis:
		style := Italic
		if v.Level >= 2 {
			style = Bold
		}
		return restyle(b.inline(v), style)
	case *ast.CodeSpan:
		txt := plainText(v, b.source)
		if txt == "" {
			return nil
		}
		return []Span{{Style: Code, Text: txt}}
	case *ast.Link:
		txt := plainText(v, b.source)
		if txt == "" {
			txt = string(v.Destination)
		}
		spans := []Span{{Style: Link, Text: txt}}
		if len(v.Destination) > 0 {
			spans = append(spans, Span{Style: Text, Text: " [" + string(v.Destination) + "]"})
		}
		return spans
	case *ast.AutoLink:
		url := string(v.URL(b.source))
		if url == "" {
			return nil
		}
		return []Span{{Style: Link, Text: url}}
	case *ast.Image:
		alt := plainText(v, b.source)
		return []Span{{Style: Text, Text: "[image: " + alt + "]"}}
	case *ast.RawHTML:
		return nil // rendered as tags in a browser; nothing sensible to show inline here.
	case *extast.Strikethrough:
		// No dedicated style in the enum (see the package doc comment) —
		// the content still reads, just without the strike.
		return b.inline(v)
	case *extast.TaskCheckBox:
		if v.IsChecked {
			return []Span{{Style: Text, Text: "[x] "}}
		}
		return []Span{{Style: Text, Text: "[ ] "}}
	default:
		return b.inline(n)
	}
}

// restyle overwrites the Style of every span in spans and returns it.
// Used by Emphasis: bold and italic each wrap a run of otherwise-plain
// text, and the enum has no combined style to preserve a nested Code or
// Link span's own style through — see the package doc comment for why
// that's an accepted simplification rather than a bug to fix later.
func restyle(spans []Span, style SpanStyle) []Span {
	out := make([]Span, len(spans))
	for i, s := range spans {
		out[i] = Span{Style: style, Text: s.Text}
	}
	return out
}

// plainText concatenates the raw text of every Text/String descendant of
// n, ignoring any styling in between. Used where nested styling isn't
// worth carrying: heading text, link labels, table cell content.
func plainText(n ast.Node, source []byte) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(x ast.Node) {
		switch v := x.(type) {
		case *ast.Text:
			b.Write(v.Value(source))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte(' ')
			}
			return
		case *ast.String:
			b.Write(v.Value)
			return
		}
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			walk(c)
		}
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		walk(c)
	}
	return strings.TrimSpace(b.String())
}
