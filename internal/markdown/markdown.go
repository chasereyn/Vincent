// =============================================================================
// File: internal/markdown/markdown.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// New package, phase 7. Mirrors internal/diff's split: this package parses
// and lays out, internal/editor/markdownview.go paints. Nothing here
// imports tcell.
// =============================================================================

// Package markdown turns a markdown source document into a flat list of
// display rows a renderer can walk without knowing anything about the
// markdown grammar — the same shape internal/diff gives the diff viewer.
//
// Parsing is github.com/yuin/goldmark's AST (pure Go, no cgo), walked here
// into Rows. A Row is a slice of styled Spans that concatenate to one
// on-screen line; Style is a small closed enum so the tcell painter can
// switch on it without knowing anything about markdown.
//
// What is deliberately simplified, because a terminal cell has one style
// and markdown nests arbitrarily:
//
//   - A heading's inline content (bold, code, links inside "## foo `bar`")
//     collapses to plain text under the heading's own style. Nested
//     emphasis inside a heading is rare enough in reviewed markdown that
//     losing it beats the bookkeeping needed to keep it.
//   - Combining bold and italic on the same run keeps whichever style
//     wraps it last (Emphasis of level 2 replaces level 1's Italic with
//     Bold) rather than tracking both at once — the enum has no
//     "BoldItalic" member.
//   - Table cells do not wrap. A cell wider than its column's computed
//     width is left as-is; only the whole table's total width is capped
//     to the page width by shrinking its widest columns.
//   - Wrapping measures width in runes, not display cells, so a
//     wide-glyph-heavy document (CJK prose) will wrap a little short.
//     internal/editor's RuneVisualWidth solves this for code, but pulling
//     it into a tcell-free package isn't worth it for a markdown viewer.
package markdown

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// SpanStyle names the display treatment for one run of text within a Row.
// It is a small, closed set on purpose: the tcell painter switches on it,
// so growing the enum means growing the painter too, never just this file.
type SpanStyle int

// The full set of styles Render ever emits. Text is the zero value so a
// Span left unset paints as plain text rather than something alarming.
const (
	Text SpanStyle = iota
	Heading1
	Heading2
	Heading3
	Bold
	Italic
	Code
	CodeBlock
	Link
	ListMarker
	Quote
	Rule
	TableBorder
	TableHeader
)

// Span is one contiguous run of text sharing a single Style. A Row's
// Spans concatenate, in order, to that row's full displayed text.
type Span struct {
	Style SpanStyle
	Text  string
}

// Row is one displayed line of rendered markdown.
//
// CodeBlock and Lang exist only so the painter can special-case a fenced
// code block: box it, and run its language through the existing Chroma
// highlighter instead of the flat Code style a plain inline code span
// gets. Lang is empty for a fence with no info string and for every row
// that isn't part of a code block.
type Row struct {
	Spans     []Span
	CodeBlock bool
	Lang      string
}

// Text concatenates a Row's spans, for callers (the find bar, tests) that
// want the plain text of a rendered line without caring how it is styled.
func (r Row) Text() string {
	var b strings.Builder
	for _, s := range r.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Texts returns the plain text of every row, in order. Mirrors
// diff.Texts: it is what backs a markdown tab's Buffer.Lines, which is
// what lets scrolling, clamping, and the find bar operate on a rendered
// tab without a single special case in the app or editor layer.
func Texts(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text()
	}
	return out
}

// Render parses source as markdown and lays it out into rows wrapped to
// width. width is clamped to at least 1 so a pathologically narrow pane
// degrades to one word per row instead of dividing by zero or looping
// forever.
//
// GFM (tables, strikethrough, autolinks, task-list checkboxes) is enabled
// unconditionally rather than gated behind a flag: an agent's markdown is
// as likely to be a GitHub-flavoured README as plain CommonMark, and
// parsing the wider grammar costs nothing when a document doesn't use it.
func Render(source []byte, width int) []Row {
	if width < 1 {
		width = 1
	}
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader(source))
	b := &builder{source: source, width: width}
	b.walkChildren(doc, nil, true)
	return b.rows
}

// builder accumulates rows while walking the AST. It holds the two things
// every block-render method needs — the source bytes (spans are sliced out
// of it, not copied ahead of time) and the target width — plus the output
// slice itself.
type builder struct {
	source []byte
	width  int
	rows   []Row
}

// emit appends one row built from content, wrapped to the builder's width
// and prefixed with prefix on every wrapped line. Blockquote bars and list
// indentation are exactly this: a prefix that repeats on every line the
// content wraps to.
func (b *builder) emit(content []Span, prefix []Span) {
	b.rows = append(b.rows, wrapSpans(content, b.width, prefix)...)
}

// walkChildren renders every block-level child of parent in document
// order. topLevel gates the blank separator row inserted between
// siblings: real prose wants a blank line between top-level paragraphs
// and headings, but a tight list item's own children (a paragraph
// followed by a nested list, say) should stay packed together the way
// the source markdown intended.
func (b *builder) walkChildren(parent ast.Node, prefix []Span, topLevel bool) {
	first := true
	for c := parent.FirstChild(); c != nil; c = c.NextSibling() {
		if topLevel && !first {
			b.rows = append(b.rows, Row{})
		}
		first = false
		b.block(c, prefix)
	}
}

// block renders a single block-level node, dispatching on its concrete
// type. Anything not recognised falls through to walking its children —
// which for an unhandled *container* still surfaces whatever text is
// inside it, rather than silently dropping a whole section.
func (b *builder) block(n ast.Node, prefix []Span) {
	switch v := n.(type) {
	case *ast.Heading:
		b.heading(v, prefix)
	case *ast.Paragraph:
		b.emit(b.inline(v), prefix)
	case *ast.TextBlock:
		// A tight list item's content parses as a TextBlock rather than a
		// Paragraph — same inline rendering, no surrounding blank line.
		b.emit(b.inline(v), prefix)
	case *ast.CodeBlock:
		b.codeBlock(v.Text(b.source), "", prefix)
	case *ast.FencedCodeBlock:
		b.codeBlock(v.Text(b.source), string(v.Language(b.source)), prefix)
	case *ast.Blockquote:
		b.blockquote(v, prefix)
	case *ast.List:
		b.list(v, prefix)
	case *ast.ThematicBreak:
		b.rule(prefix)
	case *ast.HTMLBlock:
		b.htmlBlock(v, prefix)
	case *extast.Table:
		b.table(v, prefix)
	default:
		b.walkChildren(n, prefix, false)
	}
}

// headingStyle maps a heading's 1-6 level onto the three heading styles
// the enum has. Levels 4-6 share Heading3 — a terminal has no font-size
// axis to spend on six distinct treatments, and Vincent's own headers
// (see any doc in this repo) rarely go past ###.
func headingStyle(level int) SpanStyle {
	switch level {
	case 1:
		return Heading1
	case 2:
		return Heading2
	default:
		return Heading3
	}
}

// heading renders a heading as one styled row (wrapped if it overflows
// width). The "#" markers are dropped — the Style carries the weight a
// literal hash mark would otherwise have to fake — and nested inline
// styling collapses to plain text; see the package doc comment.
func (b *builder) heading(n *ast.Heading, prefix []Span) {
	txt := plainText(n, b.source)
	if txt == "" {
		return
	}
	b.emit([]Span{{Style: headingStyle(n.Level), Text: txt}}, prefix)
}

// rule renders a thematic break (`---`) as one full-width dashed row.
func (b *builder) rule(prefix []Span) {
	w := b.width - visibleWidth(prefix)
	if w < 1 {
		w = 1
	}
	row := Row{Spans: append(append([]Span{}, prefix...), Span{Style: Rule, Text: strings.Repeat("─", w)})}
	b.rows = append(b.rows, row)
}

// codeBlock renders a fenced or indented code block one line per row,
// unwrapped — the whole point of a code block is that its line breaks
// are meaningful, so reflowing it would corrupt the code it shows. Tabs
// are expanded so the painter never has to reason about tab stops on a
// row model with no cursor.
func (b *builder) codeBlock(raw []byte, lang string, prefix []Span) {
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		line = expandTabs(line, 4)
		spans := append(append([]Span{}, prefix...), Span{Style: CodeBlock, Text: line})
		b.rows = append(b.rows, Row{Spans: spans, CodeBlock: true, Lang: lang})
	}
}

// htmlBlock renders a raw HTML block verbatim, styled like code. Vincent
// is a review tool, not a browser: showing the tag literally beats
// silently dropping content an agent put there on purpose (a <details>
// wrapper, an embedded <img>).
func (b *builder) htmlBlock(n *ast.HTMLBlock, prefix []Span) {
	var sb strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if line, ok := c.(*ast.Text); ok {
			sb.Write(line.Value(b.source))
		}
	}
	text := strings.TrimRight(sb.String(), "\n")
	if text == "" {
		return
	}
	for _, line := range strings.Split(text, "\n") {
		spans := append(append([]Span{}, prefix...), Span{Style: Code, Text: line})
		b.rows = append(b.rows, Row{Spans: spans})
	}
}

// blockquote renders a `>` block by prepending a bar to the prefix every
// descendant row already carries. Nesting quotes just means nesting this
// call, which is why a doubly-quoted line ends up with two bars — that
// falls out of the recursion rather than needing its own case.
func (b *builder) blockquote(n *ast.Blockquote, prefix []Span) {
	bar := append(append([]Span{}, prefix...), Span{Style: Quote, Text: "▍ "})
	b.walkChildren(n, bar, false)
}

// list renders every item of an ordered or unordered list. The marker
// ("• " or "1. ") only ever occupies the FIRST row an item produces;
// listItem does the prefix-swap that makes that true regardless of how
// many blocks (or how much wrapping) the item's content turns into.
func (b *builder) list(n *ast.List, prefix []Span) {
	ordinal := n.Start
	if ordinal == 0 {
		ordinal = 1
	}
	ordered := n.IsOrdered()
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		li, ok := item.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "• "
		if ordered {
			marker = itoa(ordinal) + ". "
			ordinal++
		}
		cont := append(append([]Span{}, prefix...), Span{Style: Text, Text: strings.Repeat(" ", visibleWidth([]Span{{Text: marker}}))})
		first := append(append([]Span{}, prefix...), Span{Style: ListMarker, Text: marker})
		b.listItem(li, first, cont)
	}
}

// listItem renders one list item's block children using cont as every
// row's prefix, then patches the very first row emitted to use first
// instead — the marker-vs-indent swap list relies on. first and cont are
// required to carry exactly the same visible width, which list's caller
// guarantees by building cont as first's marker text replaced with
// spaces of the same length.
func (b *builder) listItem(li *ast.ListItem, first, cont []Span) {
	start := len(b.rows)
	b.walkChildren(li, cont, false)
	if len(b.rows) == start {
		return
	}
	row := b.rows[start]
	if len(row.Spans) >= len(cont) {
		row.Spans = append(append([]Span{}, first...), row.Spans[len(cont):]...)
		b.rows[start] = row
	}
}

// itoa is strconv.Itoa without the import — list is the only caller and
// ordinal is always small and non-negative, so a hand-rolled version
// avoids pulling in strconv for one call site. Kept here rather than
// wrap.go since it is purely a list-numbering concern.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// expandTabs replaces every tab with enough spaces to reach the next
// stop-width column boundary, matching how most terminals render an
// untouched tab. Used only for code-block lines: everywhere else in this
// package renders prose, which markdown itself never lets contain a
// literal tab.
func expandTabs(s string, stop int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := stop - col%stop
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}
