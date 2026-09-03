// =============================================================================
// File: internal/markdown/wrap.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// wrap.go breaks a styled paragraph into rows no wider than a target
// width, without ever splitting a word — even one that continues a
// styled run (a bold phrase, a link's visible text). It works on tokens,
// not on the original Spans, because a single Span can span several
// words ("this whole sentence is **bold**" is one Emphasis span) and
// wrapping has to be able to break between them.

package markdown

import "strings"

// token is one word, or one run of inter-word whitespace collapsed to a
// single space. style is the style of the SPAN the token came from —
// including a space token, so a run of words inside one styled span (an
// inline code span with an embedded space, a multi-word link) stays that
// style straight through when it doesn't happen to fall on a wrap point.
// Only a space that sits at a genuine boundary between two differently
// styled spans ends up looking like plain text, which is exactly right:
// there is no styled content there to carry.
type token struct {
	text  string
	style SpanStyle
	space bool
}

// tokenize splits spans into words and single-space separators. Runs of
// whitespace inside a span's text collapse to one space token, matching
// how every other markdown renderer reflows prose: the source's exact
// whitespace (single space vs. accidental double space, a wrapped line
// in the source file) carries no meaning once we're doing our own wrap.
func tokenize(spans []Span) []token {
	var toks []token
	for _, sp := range spans {
		start := 0
		runes := []rune(sp.Text)
		inSpace := false
		flush := func(end int) {
			if end > start {
				toks = append(toks, token{text: string(runes[start:end]), style: sp.Style})
			}
		}
		for i, r := range runes {
			isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
			if isSpace && !inSpace {
				flush(i)
				start = i
				inSpace = true
			} else if !isSpace && inSpace {
				toks = append(toks, token{text: " ", style: sp.Style, space: true})
				start = i
				inSpace = false
			}
		}
		if inSpace {
			toks = append(toks, token{text: " ", style: sp.Style, space: true})
		} else {
			flush(len(runes))
		}
	}
	return toks
}

// visibleWidth is the rune-count width of spans concatenated. See the
// package doc comment for why this is runes, not display cells.
func visibleWidth(spans []Span) int {
	n := 0
	for _, s := range spans {
		n += len([]rune(s.Text))
	}
	return n
}

// wrapSpans breaks content into rows of at most width cells (after
// prefix), prepending prefix — unmodified, always occupying exactly
// len(prefix) leading spans — to every row. Prefix spans are never
// merged with content spans in the returned Row, even when adjacent
// spans share a Style: callers (list's marker-swap, in particular) index
// into a row's Spans by prefix length and need that boundary to hold.
//
// A token wider than the whole budget is placed on its own row rather
// than dropped or split — an overrunning URL is a rendering nuisance, not
// a reason to lose content or loop forever.
func wrapSpans(content []Span, width int, prefix []Span) []Row {
	if len(content) == 0 {
		return nil
	}
	budget := width - visibleWidth(prefix)
	if budget < 1 {
		budget = 1
	}
	toks := tokenize(content)
	// Trim leading/trailing whitespace tokens: a paragraph that starts or
	// ends with a soft-break-turned-space would otherwise show a stray
	// leading or trailing blank cell.
	for len(toks) > 0 && toks[0].space {
		toks = toks[1:]
	}
	for len(toks) > 0 && toks[len(toks)-1].space {
		toks = toks[:len(toks)-1]
	}
	if len(toks) == 0 {
		return nil
	}

	var rows []Row
	line := []token{}
	lineW := 0
	flushLine := func() {
		// Drop a trailing space token — wrapping breaks AT a space, and
		// keeping it would leave a visible gap before the row's own right
		// edge with nothing after it.
		for len(line) > 0 && line[len(line)-1].space {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			return
		}
		spans := append(append([]Span{}, prefix...), coalesce(line)...)
		rows = append(rows, Row{Spans: spans})
		line = nil
		lineW = 0
	}
	for _, t := range toks {
		tw := len([]rune(t.text))
		if lineW == 0 && t.space {
			// A space would only appear here right after a forced break
			// (the word before it was exactly budget-wide); dropping it
			// keeps every wrapped line starting on real content.
			continue
		}
		if lineW+tw > budget && lineW > 0 {
			flushLine()
			if t.space {
				continue
			}
		}
		line = append(line, t)
		lineW += tw
	}
	flushLine()
	return rows
}

// coalesce merges adjacent tokens that share a Style into one Span, so a
// run of several plain words becomes one Text span instead of one Span
// per token. Purely a size/tidiness optimization for the painter and for
// tests that assert on span text — wrapSpans's correctness doesn't depend
// on it.
func coalesce(toks []token) []Span {
	var spans []Span
	var b strings.Builder
	var cur SpanStyle
	flush := func() {
		if b.Len() > 0 {
			spans = append(spans, Span{Style: cur, Text: b.String()})
			b.Reset()
		}
	}
	for i, t := range toks {
		if i == 0 {
			cur = t.style
		} else if t.style != cur {
			flush()
			cur = t.style
		}
		b.WriteString(t.text)
	}
	flush()
	return spans
}
