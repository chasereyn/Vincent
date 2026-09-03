// =============================================================================
// File: internal/editor/highlight.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-29
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

package editor

import (
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/theme"
)

// Highlight tokenises src using a Chroma lexer chosen by filename (falling
// back to content-based detection, then to a plain-text lexer) and returns a
// per-line slice of styles parallel to the buffer's lines: styles[i][j] is
// the style for rune j on line i.
//
// Returning a per-rune style grid keeps the renderer simple — it just looks
// up the style for each cell it draws — at the cost of some memory.
// For files small enough to comfortably review, that's a fine trade.
func Highlight(filename, src string, t theme.Theme) [][]tcell.Style {
	return highlightSource(filename, src, t)
}

// highlightLeadLines is how many rows above and below the viewport we
// tokenise but discard, so the lexer can re-enter state it couldn't see
// otherwise, like a multi-line comment or quoted string that spans the
// viewport. Without this lead, scrolling into the middle of a long comment
// colours the body as plain code because the lexer starts fresh at the
// viewport. The lead runs both above and below: chroma needs both
// delimiters of a multi-line block in range to colour the body, so a
// comment that opens above and closes below the viewport needs the opener
// reached by the upper lead and the closer reached by the lower lead.
// Bounded so keystroke cost still follows terminal height, not file size:
// a block longer than the combined lead is the rare case that still
// mis-colours.
const highlightLeadLines = 256

// HighlightVisible returns a style grid for the current viewport. Only
// visible rows are kept in the output so keystroke cost follows terminal
// height, not file size. To stay correct inside multi-line comments and
// strings that span the viewport, tokenisation starts a bounded lead above
// the top and ends a bounded lead below the bottom; those lead rows are
// styled then thrown away.
func HighlightVisible(filename string, lines []string, startLine, height int, t theme.Theme) [][]tcell.Style {
	styles := make([][]tcell.Style, len(lines))
	if height <= 0 || startLine >= len(lines) {
		return styles
	}
	if startLine < 0 {
		startLine = 0
	}
	endLine := startLine + height
	if endLine > len(lines) {
		endLine = len(lines)
	}
	// Tokenise a bounded lead above and below the viewport. The upper lead
	// lets the lexer re-enter comment / string state opened earlier; the
	// lower lead closes blocks that end past the viewport. We keep only the
	// visible rows; the lead rows are styled then dropped.
	leadStart := startLine - highlightLeadLines
	if leadStart < 0 {
		leadStart = 0
	}
	leadEnd := endLine + highlightLeadLines
	if leadEnd > len(lines) {
		leadEnd = len(lines)
	}
	src := strings.Join(lines[leadStart:leadEnd], "\n")
	leadStyles := highlightSource(filename, src, t)
	for i := startLine; i < endLine; i++ {
		idx := i - leadStart
		if idx >= len(leadStyles) {
			break
		}
		styles[i] = leadStyles[idx]
	}
	return styles
}

// highlightSource tokenises src and returns one style row per source line,
// choosing a lexer from filename the way any file on disk would.
func highlightSource(filename, src string, t theme.Theme) [][]tcell.Style {
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Analyse(src)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	return tokenizeWithLexer(lexer, src, t)
}

// HighlightLang tokenises src using the Chroma lexer registered under lang
// — a fenced markdown code block's info string ("go", "python", "bash",
// ...) — falling back to content sniffing, then plain text.
//
// This is deliberately a separate entry point from Highlight rather than
// Highlight("code."+lang, ...): lexers.Match resolves a lexer from a
// filename's extension, and a fence's language tag is often not a real
// extension at all ("python" vs. the actual ".py", "javascript" vs.
// ".js") — lexers.Get looks a lexer up by its registered name and
// aliases instead, which is what a language tag actually is.
func HighlightLang(lang, src string, t theme.Theme) [][]tcell.Style {
	var lexer chroma.Lexer
	if lang != "" {
		lexer = lexers.Get(lang)
	}
	if lexer == nil {
		lexer = lexers.Analyse(src)
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}
	return tokenizeWithLexer(lexer, src, t)
}

// tokenizeWithLexer runs lexer over src and returns one style row per
// source line. Shared by Highlight (lexer chosen from a filename) and
// HighlightLang (lexer chosen from a fence's language tag) so the two
// entry points can't drift on how a token maps to a style.
func tokenizeWithLexer(lexer chroma.Lexer, src string, t theme.Theme) [][]tcell.Style {
	// Coalesce merges adjacent same-type tokens; cheaper to scan in render.
	lexer = chroma.Coalesce(lexer)

	base := tcell.StyleDefault.Background(t.BG).Foreground(t.Text)

	// Pre-allocate a styles grid sized to the source. We seed every cell
	// with the base style so untokenised runes still render readably.
	lines := strings.Split(src, "\n")
	styles := baseStyleGrid(lines, base)

	iter, err := lexer.Tokenise(nil, src)
	if err != nil {
		return styles
	}

	line, col := 0, 0
	for tok := iter(); tok != chroma.EOF; tok = iter() {
		st := styleForToken(tok.Type, tok.Value, t, base)
		for _, r := range tok.Value {
			if r == '\n' {
				line++
				col = 0
				continue
			}
			if line < len(styles) && col < len(styles[line]) {
				styles[line][col] = st
			}
			col++
		}
	}
	return styles
}

// baseStyleGrid returns a correctly shaped grid pre-filled with base.
func baseStyleGrid(lines []string, base tcell.Style) [][]tcell.Style {
	styles := make([][]tcell.Style, len(lines))
	for i, ln := range lines {
		runes := []rune(ln)
		row := make([]tcell.Style, len(runes))
		for j := range row {
			row[j] = base
		}
		styles[i] = row
	}
	return styles
}

// styleForToken maps a Chroma token type to a tcell.Style using the active
// theme. We match by category first (Keyword, LiteralString, etc.) so the
// mapping stays tight across the dozens of language-specific subtypes.
//
// value is the token's source text — needed only for the Name category's
// catch-all case, where chroma's regex lexer has no semantic model of
// "this identifier was declared as a type" and styleForPlainIdent falls
// back to a capitalization heuristic instead.
func styleForToken(tt chroma.TokenType, value string, t theme.Theme, base tcell.Style) tcell.Style {
	switch tt.Category() {
	case chroma.Keyword:
		return base.Foreground(t.SynKeyword)
	case chroma.LiteralString:
		return base.Foreground(t.SynString)
	case chroma.LiteralNumber:
		return base.Foreground(t.SynNumber)
	case chroma.Comment:
		return base.Foreground(t.SynComment).Italic(true)
	case chroma.Operator:
		return base.Foreground(t.SynOperator)
	case chroma.Punctuation:
		return base.Foreground(t.SynPunct)
	case chroma.Literal:
		return base.Foreground(t.SynConstant)
	case chroma.Name:
		switch tt {
		case chroma.NameFunction, chroma.NameFunctionMagic:
			return base.Foreground(t.SynFunction)
		case chroma.NameClass, chroma.NameNamespace:
			return base.Foreground(t.SynType)
		case chroma.NameBuiltin, chroma.NameBuiltinPseudo:
			return base.Foreground(t.SynBuiltin)
		case chroma.NameConstant:
			return base.Foreground(t.SynConstant)
		case chroma.NameVariable, chroma.NameVariableInstance,
			chroma.NameVariableClass, chroma.NameVariableGlobal,
			chroma.NameVariableAnonymous:
			return base.Foreground(t.SynVariable)
		case chroma.NameTag, chroma.NameAttribute, chroma.NameProperty:
			return base.Foreground(t.SynProperty)
		case chroma.NameOther, chroma.Name:
			return styleForPlainIdent(value, t, base)
		}
		// Any other Name subtype chroma might add: same heuristic rather
		// than the old flat base — see styleForPlainIdent.
		return styleForPlainIdent(value, t, base)
	}
	return base
}

// styleForPlainIdent colors a Name-category token chroma didn't classify
// any more specifically than "an identifier" — a declared type name, a
// struct field, a package qualifier like fmt in fmt.Println, or a plain
// local variable. Chroma is a single-pass regex lexer, not a parser, so it
// has no notion of "this name was declared as a type"; this is a
// heuristic standing in for that, using the Go/Rust/TypeScript convention
// that exported and type identifiers start uppercase. It won't be exactly
// right — a capitalized package-level const reads as a type too — but it
// beats every plain identifier rendering in flat, uncolored text.
func styleForPlainIdent(value string, t theme.Theme, base tcell.Style) tcell.Style {
	runes := []rune(value)
	if len(runes) > 0 && unicode.IsUpper(runes[0]) {
		return base.Foreground(t.SynType)
	}
	return base.Foreground(t.SynVariable)
}
