// =============================================================================
// File: internal/editor/markdownview.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Phase 7. Mirrors diffview.go's shape exactly: internal/markdown parses
// and lays out (no tcell), this file paints. A markdown tab is a Tab in
// markdownMode, read-only through ReadOnly() for free, the same way a
// diff or image tab is.
// =============================================================================

// markdownview.go gives Tab a rendered-markdown mode: headings, bold,
// italic, inline code, fenced code blocks (Chroma-highlighted, boxed),
// lists, block quotes, tables, links, and rules — laid out by
// internal/markdown and painted here.
//
// A markdown tab's Buffer.Lines holds the RENDERED plain text (one line
// per internal/markdown.Row, via markdown.Texts) — exactly the trick
// DiffRows/Buffer.Lines plays for a diff tab. That's what lets scrolling,
// clamping, hit-testing, and the find bar all work on a markdown tab with
// no special case anywhere else in the app. MarkdownRows carries the
// styling that plain-text Buffer can't.
//
// Unlike a diff, a markdown document must be WRAPPED to the pane's width,
// and the pane resizes. rewrapMarkdown re-renders from MarkdownSource
// whenever the render width changes, which is also how the app's Esc-m
// raw/rendered toggle and the disk-reconcile loop feed it new content —
// see app/markdownview.go.
package editor

import (
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/markdown"
	"github.com/chasereyn/vincent/internal/theme"
)

// markdownMode is the value Tab.Mode takes for a rendered markdown view,
// mirroring diffMode and imageMode.
const markdownMode = "markdown"

// IsMarkdownExt reports whether path's extension marks it as markdown.
// Case-insensitive so "README.MD" opens rendered the same as "readme.md".
func IsMarkdownExt(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// defaultMarkdownWidth is the wrap width NewMarkdownTab lays out at
// before the tab has ever been rendered — the real pane width isn't
// knowable at open time (the tab doesn't have a rect yet). It only has to
// hold until the first Render, which sees the actual width and re-wraps;
// it exists at all so t.Buffer is never nil in the window between "tab
// constructed" and "tab painted", the way every other Tab constructor's
// buffer already isn't.
const defaultMarkdownWidth = 80

// NewMarkdownTab opens path and returns a Tab rendered in markdown mode.
// A missing file is tolerated, exactly like NewTab: it opens as an empty
// rendered document rather than an error, so pointing Vincent at a .md
// path that doesn't exist yet behaves the same as it would for any other
// extension.
func NewMarkdownTab(path string) (*Tab, error) {
	var data []byte
	var mtime time.Time
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		data = b
		if info, statErr := os.Stat(path); statErr == nil {
			mtime = info.ModTime()
		}
	}
	t := &Tab{Path: path, Mode: markdownMode, Mtime: mtime}
	t.MarkdownSource = string(data)
	t.rewrapMarkdown(defaultMarkdownWidth)
	t.initUndo()
	return t, nil
}

// IsMarkdown reports whether the tab is a rendered markdown view rather
// than the file's raw text (or anything else). A .md file opened in raw
// mode has Mode == "" and reports false here — "is this tab currently
// showing the rendered view", not "is the underlying file markdown".
func (t *Tab) IsMarkdown() bool {
	return t.Mode == markdownMode
}

// SetMarkdownSource replaces the tab's markdown source — the disk-reconcile
// loop re-reading a file an agent rewrote, or the app's raw/rendered
// toggle handing back the (possibly edited) raw buffer — and forces the
// next render to re-lay it out. markdownWidth resets to -1 rather than 0
// so a render at a genuinely zero-width pane still re-wraps once the pane
// has real width again, instead of comparing 0 == 0 and skipping it.
func (t *Tab) SetMarkdownSource(source string) {
	t.MarkdownSource = source
	t.markdownWidth = -1
}

// ToggleMarkdownView is Esc m: swap the tab between its rendered markdown
// view and the raw editable text view, in place — same *Tab, same Path,
// so this never touches the app's tab list or the active-tab index. The
// raw view is an ordinary text tab: Mode becomes "", ReadOnly() flips
// false, and every edit, undo, and save path a plain file already has
// just works with no further changes.
//
// Calling this on a tab that isn't showing markdown and isn't a .md/.markdown
// path is a silent no-op — the app's leader binding doesn't have to know
// what counts as "markdown" itself, just that the tab does.
//
// Content survives the round trip: raw -> rendered renders whatever is
// CURRENTLY in the buffer, edits included, so previewing an in-progress
// edit works before it's saved; rendered -> raw seeds the editable buffer
// from that same source, so an edit made, previewed, and toggled back is
// still there. Dirty is untouched by this method entirely — it already
// reflects "does the buffer disagree with disk", independent of which
// view is currently showing it.
//
// Scroll position is preserved as a FRACTION of the document rather than
// an absolute line, because the two views have different row counts
// (wrapped rendered rows vs. raw source lines) — an absolute ScrollY
// would land on an unrelated line after either direction.
func (t *Tab) ToggleMarkdownView() {
	if t.IsMarkdown() {
		t.switchMarkdownToRaw()
		return
	}
	if IsMarkdownExt(t.Path) {
		t.switchMarkdownToRendered()
	}
}

// switchMarkdownToRaw is the rendered -> raw half of ToggleMarkdownView.
// The new buffer is built immediately (unlike the other direction, which
// defers to the next Render), so the scroll fraction is applied here and
// now rather than left for rewrapMarkdown to pick up.
func (t *Tab) switchMarkdownToRaw() {
	oldCount := t.Buffer.LineCount()
	var frac float64
	if oldCount > 0 {
		frac = float64(t.ScrollY) / float64(oldCount)
	}
	t.Buffer = NewBuffer(t.MarkdownSource)
	t.Mode = ""
	t.IndentUnit = DetectIndent(t.Buffer.Lines, t.Path)
	t.StyleStale = true
	t.ScrollY = int(frac * float64(t.Buffer.LineCount()))
	t.Cursor = t.Buffer.Clamp(t.Cursor)
	t.Anchor = t.Cursor
}

// switchMarkdownToRendered is the raw -> rendered half of
// ToggleMarkdownView. It deliberately leaves Buffer, ScrollY, Cursor, and
// Anchor untouched: the next Render sees markdownWidth reset to -1 by
// SetMarkdownSource, calls rewrapMarkdown, and rewrapMarkdown preserves
// the scroll fraction by reading THIS SAME (still-raw) Buffer before
// replacing it — duplicating that math here would just be a second,
// driftable copy of it.
func (t *Tab) switchMarkdownToRendered() {
	t.Mode = markdownMode
	t.SetMarkdownSource(t.Buffer.String())
}

// rewrapMarkdown re-renders MarkdownSource at width and rebuilds Buffer
// from the result, preserving the scroll position as a fraction of the
// document rather than an absolute line — a resize or a content change
// both change the row count, and an absolute ScrollY would land on an
// unrelated line after either.
func (t *Tab) rewrapMarkdown(width int) {
	if width < 1 {
		width = 1
	}
	var oldCount int
	var frac float64
	if t.Buffer != nil {
		if oldCount = t.Buffer.LineCount(); oldCount > 0 {
			frac = float64(t.ScrollY) / float64(oldCount)
		}
	}
	t.MarkdownRows = markdown.Render([]byte(t.MarkdownSource), width)
	lines := markdown.Texts(t.MarkdownRows)
	if len(lines) == 0 {
		lines = []string{""}
	}
	t.Buffer = &Buffer{Lines: lines}
	t.markdownWidth = width
	t.StyleStale = true
	if oldCount > 0 {
		t.ScrollY = int(frac * float64(t.Buffer.LineCount()))
	}
	t.Cursor = t.Buffer.Clamp(t.Cursor)
	t.Anchor = t.Cursor
	t.markdownCodeStyles = nil
}

// markdownCodeBlockGroup returns the run of contiguous rows starting at i
// that share CodeBlock == true and the same Lang, and the index just past
// it. Grouping contiguous same-language rows lets Chroma tokenise a fenced
// block as one source (correct for multi-line strings and comments)
// instead of one doomed-to-be-wrong call per line.
func markdownCodeBlockGroup(rows []markdown.Row, i int) (lang string, end int) {
	lang = rows[i].Lang
	end = i
	for end < len(rows) && rows[end].CodeBlock && rows[end].Lang == lang {
		end++
	}
	return lang, end
}

// highlightMarkdownCode runs Chroma over every fenced code block in rows
// and returns a per-row style grid indexed the same way rows is — nil for
// a row that isn't part of a code block. Computed eagerly over the whole
// document (not viewport-bounded the way highlight.go's HighlightVisible
// is for a text tab) because a reviewed markdown document is realistically
// README-sized, not the multi-thousand-line agent diff that pattern
// exists for.
func highlightMarkdownCode(rows []markdown.Row, th theme.Theme) [][]tcell.Style {
	styles := make([][]tcell.Style, len(rows))
	for i := 0; i < len(rows); {
		if !rows[i].CodeBlock {
			i++
			continue
		}
		lang, end := markdownCodeBlockGroup(rows, i)
		lines := make([]string, 0, end-i)
		for k := i; k < end; k++ {
			lines = append(lines, rows[k].Text())
		}
		block := HighlightLang(lang, strings.Join(lines, "\n"), th)
		for k := 0; k < len(block) && i+k < end; k++ {
			styles[i+k] = block[k]
		}
		i = end
	}
	return styles
}

// markdownSpanStyle maps one markdown.SpanStyle to the tcell.Style it
// paints as, layered on base (which already carries the row's background
// and the plain foreground/background pair every span starts from).
//
// CodeBlock is deliberately absent from this switch: a code-block row is
// painted from the pre-tokenised Chroma grid instead of this per-Style
// mapping — see renderMarkdown.
func markdownSpanStyle(style markdown.SpanStyle, th theme.Theme, base tcell.Style) tcell.Style {
	switch style {
	case markdown.Heading1:
		return base.Foreground(th.Accent).Bold(true).Underline(true)
	case markdown.Heading2, markdown.Heading3:
		return base.Foreground(th.Accent).Bold(true)
	case markdown.Bold:
		return base.Bold(true)
	case markdown.Italic:
		return base.Italic(true)
	case markdown.Code:
		// Inline code gets the same boxed background a fenced block gets,
		// just without its own Chroma pass — a bare snippet ("go test
		// ./...") rarely carries enough context for a lexer to earn its
		// keep, and the tint alone already sets it apart from prose.
		return base.Background(th.ReviewBoxBG)
	case markdown.ListMarker:
		return base.Foreground(th.Accent)
	case markdown.Quote:
		return base.Foreground(th.Subtle)
	case markdown.Rule:
		return base.Foreground(th.Subtle)
	case markdown.TableBorder:
		return base.Foreground(th.Subtle)
	case markdown.TableHeader:
		return base.Bold(true)
	case markdown.Link:
		return base.Foreground(th.SynProperty).Underline(true)
	default:
		return base
	}
}

// codeBlockPad is the left padding, in cells, a fenced code block's box
// gets — "boxed in one cell of left padding" per the phase spec. There is
// no matching right pad: the row's background already fills the pane's
// full width, so a right pad would just be more of the same background.
const codeBlockPad = 1

// renderMarkdown paints the markdown tab. Called from Tab.Render when the
// tab is in markdownMode.
//
// There is no gutter — gutterCells() returns 0 for a markdown tab — so
// content starts at column x. Every cell is written exactly once via the
// scratch-row/flushRow idiom diffview.go and the text path both use; see
// flushRow's doc comment for why that isn't just tidiness.
func (t *Tab) renderMarkdown(scr tcell.Screen, th theme.Theme, x, y, w, h int) {
	if w != t.markdownWidth {
		t.rewrapMarkdown(w)
	}
	t.clampScroll(h)
	if t.StyleStale {
		t.markdownCodeStyles = highlightMarkdownCode(t.MarkdownRows, th)
		t.StyleStale = false
	}

	base := tcell.StyleDefault.Background(th.BG).Foreground(th.Text)
	selStart, selEnd := PosOrdered(t.Anchor, t.Cursor)
	hasSel := t.HasSelection()
	rowBuf := t.rowBuf(w)

	for row := 0; row < h; row++ {
		lineIdx := t.ScrollY + row
		cy := y + row
		if lineIdx < 0 || lineIdx >= len(t.MarkdownRows) {
			fillRow(rowBuf, ' ', base)
			flushRow(scr, x, cy, rowBuf)
			continue
		}
		mdRow := t.MarkdownRows[lineIdx]
		rowBG := th.BG
		if mdRow.CodeBlock {
			rowBG = th.ReviewBoxBG
		}
		rowStyle := tcell.StyleDefault.Background(rowBG).Foreground(th.Text)
		fillRow(rowBuf, ' ', rowStyle)
		put := func(cx int, ch rune, st tcell.Style) {
			if idx := cx - x; idx >= 0 && idx < len(rowBuf) {
				rowBuf[idx] = renderCell{ch: ch, st: st}
			}
		}
		overlay := func(col int, st tcell.Style) tcell.Style {
			if hasSel {
				p := Position{Line: lineIdx, Col: col}
				if !PosLess(p, selStart) && PosLess(p, selEnd) {
					st = st.Background(th.Selection)
				}
			}
			if mIdx := t.matchAtRune(lineIdx, col); mIdx >= 0 {
				if mIdx == t.FindIndex {
					st = st.Background(th.FindCurrent).Foreground(th.BG)
				} else {
					st = st.Background(th.FindMatch)
				}
			}
			return st
		}

		contentX := x
		if mdRow.CodeBlock {
			contentX = x + codeBlockPad
		}

		if mdRow.CodeBlock {
			runes := []rune(mdRow.Text())
			var styles []tcell.Style
			if lineIdx < len(t.markdownCodeStyles) {
				styles = t.markdownCodeStyles[lineIdx]
			}
			for i, r := range runes {
				st := rowStyle
				if i < len(styles) {
					st = styles[i].Background(rowBG)
				}
				put(contentX+i, r, overlay(i, st))
			}
		} else {
			col := 0
			for _, span := range mdRow.Spans {
				st := markdownSpanStyle(span.Style, th, rowStyle)
				for _, r := range span.Text {
					put(contentX+col, r, overlay(col, st))
					col++
				}
			}
		}

		flushRow(scr, x, cy, rowBuf)
	}

	// Read-only: a selected range, never a blinking insertion point.
	scr.HideCursor()
}

// markdownHitTest maps a click inside a markdown tab to a buffer
// position. There is no gutter and no horizontal scroll to account for —
// content always starts at column 0 and wraps rather than running off
// the pane — so this is simpler than the text and diff hit-testers it
// sits beside.
func (t *Tab) markdownHitTest(localX, localY, w, h int) (Position, bool) {
	if localY < 0 || localY >= h {
		return Position{}, false
	}
	line := t.ScrollY + localY
	if line < 0 || line >= t.Buffer.LineCount() {
		return Position{}, false
	}
	runes := t.Buffer.LineRunes(line)
	col := localX
	if col > len(runes) {
		col = len(runes)
	}
	if col < 0 {
		col = 0
	}
	return Position{Line: line, Col: col}, true
}
