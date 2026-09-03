// =============================================================================
// File: internal/editor/highlight_test.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Tests for the syntax-highlight grid generator. We don't pin specific
// chroma token assignments (those are an upstream concern), only the shape
// invariants the renderer relies on: one row per source line, each row long
// enough to cover its line's runes, and a graceful fallback for unknown or
// missing lexers.

package editor

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/theme"
)

// TestHighlight_GoSourceShapesGrid verifies that highlighting a snippet of
// Go produces a grid whose row count and per-row length match the source
// lines and rune counts — that's what the renderer indexes into.
func TestHighlight_GoSourceShapesGrid(t *testing.T) {
	src := "package main\n\nfunc main() {}\n"
	th := theme.Default()

	got := Highlight("main.go", src, th)
	lines := strings.Split(src, "\n")
	if len(got) != len(lines) {
		t.Fatalf("rows = %d, want %d", len(got), len(lines))
	}
	for i, ln := range lines {
		if len(got[i]) != len([]rune(ln)) {
			t.Errorf("row %d len = %d, want %d", i, len(got[i]), len([]rune(ln)))
		}
	}
}

// TestHighlight_GoKeywordIsColored confirms at least one rune in a Go source
// gets a non-default foreground — proves the lexer ran and produced styles.
func TestHighlight_GoKeywordIsColored(t *testing.T) {
	src := "package main\nfunc f() {}\n"
	th := theme.Default()

	got := Highlight("main.go", src, th)
	base := tcell.StyleDefault.Background(th.BG).Foreground(th.Text)

	differs := false
	for _, row := range got {
		for _, st := range row {
			if st != base {
				differs = true
				break
			}
		}
		if differs {
			break
		}
	}
	if !differs {
		t.Fatal("expected at least one non-base styled rune in highlighted Go source")
	}
}

// TestHighlight_UnknownExtension gracefully falls back to the plain-text
// (or analyzed) lexer instead of panicking. The grid must still match the
// source shape.
func TestHighlight_UnknownExtension(t *testing.T) {
	src := "anything goes here\nsecond line"
	th := theme.Default()

	got := Highlight("file.totallymadeup", src, th)
	lines := strings.Split(src, "\n")
	if len(got) != len(lines) {
		t.Fatalf("rows = %d, want %d", len(got), len(lines))
	}
	for i, ln := range lines {
		if len(got[i]) != len([]rune(ln)) {
			t.Errorf("row %d len = %d, want %d", i, len(got[i]), len([]rune(ln)))
		}
	}
}

// TestHighlight_EmptyInput returns a single empty row, mirroring NewBuffer's
// behaviour — strings.Split("", "\n") yields [""], so the grid has one row
// of zero runes.
func TestHighlight_EmptyInput(t *testing.T) {
	th := theme.Default()
	got := Highlight("file.go", "", th)
	if len(got) != 1 {
		t.Fatalf("expected 1 row, got %d", len(got))
	}
	if len(got[0]) != 0 {
		t.Fatalf("expected empty row, got len=%d", len(got[0]))
	}
}

// TestHighlight_MultibyteRunes makes sure rune-indexed columns are handled
// when the source contains multi-byte characters — each row must have one
// style entry per rune (not per byte).
func TestHighlight_MultibyteRunes(t *testing.T) {
	src := "// héllo\nx := 1\n"
	th := theme.Default()

	got := Highlight("main.go", src, th)
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if len(got[i]) != len([]rune(ln)) {
			t.Errorf("row %d: style len = %d, rune len = %d", i, len(got[i]), len([]rune(ln)))
		}
	}
}

// TestHighlight_NoFilenameAnalyses lets Chroma analyse the content when no
// filename is provided. It should still return a properly shaped grid.
func TestHighlight_NoFilenameAnalyses(t *testing.T) {
	src := "package main\nfunc main() {}\n"
	th := theme.Default()

	got := Highlight("", src, th)
	lines := strings.Split(src, "\n")
	if len(got) != len(lines) {
		t.Fatalf("rows = %d, want %d", len(got), len(lines))
	}
}

// TestHighlight_DiverseTokens exercises the styleForToken switch by feeding
// it source containing keywords, strings, numbers, comments, and names, then
// confirms the grid is well-formed. We don't assert on specific colors —
// only that the function ran across the diverse token types without panic
// and produced a parallel grid.
func TestHighlight_DiverseTokens(t *testing.T) {
	src := `// comment line
package main

import "fmt"

const Answer = 42

type Foo struct{}

func (f *Foo) Bar() string {
	return "hello"
}
`
	th := theme.Default()
	got := Highlight("main.go", src, th)
	lines := strings.Split(src, "\n")
	if len(got) != len(lines) {
		t.Fatalf("rows = %d, want %d", len(got), len(lines))
	}
	for i, ln := range lines {
		if len(got[i]) != len([]rune(ln)) {
			t.Errorf("row %d len = %d, want %d", i, len(got[i]), len([]rune(ln)))
		}
	}
}

// TestHighlightVisible_LimitsTokenisingToViewport pins the fast path:
// off-screen rows stay empty in the output while visible rows are tokenised.
// (A bounded lead above the viewport is also tokenised but discarded, so the
// output grid still only carries the visible rows.)
func TestHighlightVisible_LimitsTokenisingToViewport(t *testing.T) {
	th := theme.Default()
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "package main"
	}

	got := HighlightVisible("main.go", lines, 10, 2, th)
	if len(got) != len(lines) {
		t.Fatalf("rows = %d, want %d", len(got), len(lines))
	}
	if got[0] != nil {
		t.Fatalf("off-screen row was highlighted, got %v", got[0])
	}
	base := tcell.StyleDefault.Background(th.BG).Foreground(th.Text)
	if got[10][0] == base {
		t.Fatal("visible row was not highlighted")
	}
}

// TestHighlightVisible_LeadRecoversCommentState proves the bounded lead
// fixes the regression where scrolling into the middle of a multi-line
// comment coloured the body as plain code. The comment opens above the
// viewport; without the lead the lexer would start fresh at the visible
// rows and never know it was inside a comment.
func TestHighlightVisible_LeadRecoversCommentState(t *testing.T) {
	th := theme.Default()
	// A multi-line comment opens on line 1 and closes on line 6. The
	// viewport starts on line 4, inside the comment, with the opening /*
	// above the visible rows.
	lines := []string{
		"package main",   // 0
		"/*",             // 1  <- comment opens
		" * body one",    // 2
		" * body two",    // 3
		" * body three",  // 4  <- viewport start
		" * body four",   // 5
		" */",            // 6
		"func main() {}", // 7
	}
	got := HighlightVisible("main.go", lines, 4, 2, th)
	comment := tcell.StyleDefault.Background(th.BG).Foreground(th.SynComment).Italic(true)
	for _, i := range []int{4, 5} {
		row := got[i]
		if row == nil {
			t.Fatalf("line %d was not highlighted", i)
		}
		// At least one rune on each visible comment body line must carry
		// the comment style, proving the lead let the lexer re-enter the
		// block instead of tokenising the body as plain text.
		hasComment := false
		for _, st := range row {
			if st == comment {
				hasComment = true
				break
			}
		}
		if !hasComment {
			t.Errorf("line %d has no comment-styled rune; lead did not recover comment state", i)
		}
	}
}

// TestHighlight_TypeNameVsPlainVariableDiffer pins the styleForPlainIdent
// heuristic: a declared type name and an ordinary lowercase variable both
// fall through chroma's Go lexer to the unclassified NameOther token (chroma
// is a regex lexer, not a parser — it has no notion of "this was declared
// as a type"), and before this fix both rendered in the same flat base
// color. They must now diverge by capitalization: SynType vs SynVariable.
func TestHighlight_TypeNameVsPlainVariableDiffer(t *testing.T) {
	src := "package main\n\nfunc run(cfg Config) {\n\tresult := cfg.Name\n\t_ = result\n}\n"
	th := theme.Default()

	got := Highlight("main.go", src, th)
	lines := strings.Split(src, "\n")

	typeStyle := tcell.StyleDefault.Background(th.BG).Foreground(th.SynType)
	varStyle := tcell.StyleDefault.Background(th.BG).Foreground(th.SynVariable)

	findRune := func(row int, needle rune) tcell.Style {
		t.Helper()
		for i, r := range []rune(lines[row]) {
			if r == needle {
				return got[row][i]
			}
		}
		t.Fatalf("could not find %q on line %d (%q)", needle, row, lines[row])
		return tcell.Style{}
	}

	// Line 3 is "\tresult := cfg.Name" — "result" is a lowercase local
	// variable bound by :=.
	resultStyle := findRune(3, 'r')
	if resultStyle != varStyle {
		t.Errorf("lowercase variable %q: got %v, want SynVariable %v", "result", resultStyle, varStyle)
	}

	// Line 2 is "func run(cfg Config) {" — "Config" is a declared,
	// capitalized type name used as a parameter type.
	configStyle := findRune(2, 'C')
	if configStyle != typeStyle {
		t.Errorf("type name %q: got %v, want SynType %v", "Config", configStyle, typeStyle)
	}

	if resultStyle == configStyle {
		t.Error("type name and lowercase variable must not share a style")
	}
}

// TestHighlightLang_ResolvesByRegisteredName proves HighlightLang picks a
// lexer from a fenced code block's language tag directly — "python", not
// the ".py" extension lexers.Match would need — which is the whole reason
// this entry point exists apart from Highlight.
func TestHighlightLang_ResolvesByRegisteredName(t *testing.T) {
	src := "def f():\n    return 1\n"
	th := theme.Default()

	got := HighlightLang("python", src, th)
	lines := strings.Split(src, "\n")
	if len(got) != len(lines) {
		t.Fatalf("rows = %d, want %d", len(got), len(lines))
	}
	// "def" is a Python keyword; some rune on line 0 must differ from the
	// plain base style, or the lexer never resolved and everything fell
	// through to Fallback.
	base := tcell.StyleDefault.Background(th.BG).Foreground(th.Text)
	colored := false
	for _, st := range got[0] {
		if st != base {
			colored = true
		}
	}
	if !colored {
		t.Fatal("no rune on line 0 got a non-base style; the python lexer likely didn't resolve")
	}
}

// TestHighlightLang_UnknownLanguageFallsBackToAnalyse proves an
// unrecognised or empty language tag degrades to content sniffing rather
// than panicking or returning nothing — the same fallback chain Highlight
// gives an unrecognised filename.
func TestHighlightLang_UnknownLanguageFallsBackToAnalyse(t *testing.T) {
	src := "package main\nfunc main() {}\n"
	th := theme.Default()

	for _, lang := range []string{"", "totallymadeuplang"} {
		got := HighlightLang(lang, src, th)
		lines := strings.Split(src, "\n")
		if len(got) != len(lines) {
			t.Fatalf("lang %q: rows = %d, want %d", lang, len(got), len(lines))
		}
		for i, ln := range lines {
			if len(got[i]) != len([]rune(ln)) {
				t.Errorf("lang %q: row %d len = %d, want %d", lang, i, len(got[i]), len([]rune(ln)))
			}
		}
	}
}

// TestHighlightLang_EmptyInput mirrors TestHighlight_EmptyInput: a single
// empty row, never a panic on zero-length source.
func TestHighlightLang_EmptyInput(t *testing.T) {
	th := theme.Default()
	got := HighlightLang("go", "", th)
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("got %#v, want one empty row", got)
	}
}
