// =============================================================================
// File: internal/editor/markdownview_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package editor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/theme"
)

// writeMarkdownFile writes content to a temp .md file and returns its
// path. t.TempDir() per CLAUDE.md's convention — nothing here ever
// touches the repo.
func writeMarkdownFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// newMarkdownScreen opens path as a markdown tab and paints it into a
// simulation screen of the given size, returning both so a test can
// assert on rendered cells.
func newMarkdownScreen(t *testing.T, path string, w, h int) (*Tab, tcell.SimulationScreen) {
	t.Helper()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(w, h)

	tab, err := NewMarkdownTab(path)
	if err != nil {
		t.Fatalf("NewMarkdownTab: %v", err)
	}
	tab.Render(scr, theme.Default(), 0, 0, w, h)
	scr.Show()
	return tab, scr
}

// TestIsMarkdownExt pins the extension check the app uses to decide
// whether a newly opened file should default to rendered mode.
func TestIsMarkdownExt(t *testing.T) {
	cases := map[string]bool{
		"README.md":       true,
		"readme.MD":       true,
		"notes.markdown":  true,
		"NOTES.MARKDOWN":  true,
		"main.go":         false,
		"README":          false,
		"README.md.bak":   false,
		"a/b/c/README.md": true,
	}
	for path, want := range cases {
		if got := IsMarkdownExt(path); got != want {
			t.Errorf("IsMarkdownExt(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestNewMarkdownTab_IsReadOnlyAndParallelToRows pins the two structural
// invariants everything else leans on, mirroring the diff tab's own test
// of the same name: the tab reports itself read-only, and once rendered
// its buffer runs line-for-line parallel to MarkdownRows.
func TestNewMarkdownTab_IsReadOnlyAndParallelToRows(t *testing.T) {
	path := writeMarkdownFile(t, "# Title\n\nSome text.\n")
	tab, _ := newMarkdownScreen(t, path, 40, 10)

	if !tab.IsMarkdown() {
		t.Fatal("expected IsMarkdown() to be true")
	}
	if !tab.ReadOnly() {
		t.Fatal("a markdown tab must be read-only")
	}
	if tab.Buffer.LineCount() != len(tab.MarkdownRows) {
		t.Fatalf("buffer has %d lines, MarkdownRows has %d", tab.Buffer.LineCount(), len(tab.MarkdownRows))
	}
	for i, row := range tab.MarkdownRows {
		if tab.Buffer.Lines[i] != row.Text() {
			t.Fatalf("buffer line %d = %q, MarkdownRows[%d].Text() = %q", i, tab.Buffer.Lines[i], i, row.Text())
		}
	}
}

// TestRenderMarkdown_HeadingIsBold proves a heading row actually reaches
// the screen in a bold style — not just that the row model says Heading1,
// but that the painter applied it.
func TestRenderMarkdown_HeadingIsBold(t *testing.T) {
	path := writeMarkdownFile(t, "# Title\n")
	_, scr := newMarkdownScreen(t, path, 40, 5)

	if got := rowText(t, scr, 0); got != "  Title" {
		t.Fatalf("row 0 = %q, want %q (two-cell margin, then the heading)", got, "  Title")
	}
	cells, w, _ := scr.GetContents()
	st := cells[0*w+markdownGutter].Style
	gotFG, _, attr := st.Decompose()
	if attr&tcell.AttrBold == 0 {
		t.Fatal("heading cell is not bold")
	}
	th := theme.Default()
	if gotFG != th.Accent {
		t.Fatalf("heading foreground = %v, want theme.Accent %v", gotFG, th.Accent)
	}
}

// TestRenderMarkdown_CodeBlockIsBoxedAndPadded proves a fenced code
// block's row is painted with theme.ReviewBoxBG behind it and one cell of
// left padding, per the phase spec.
func TestRenderMarkdown_CodeBlockIsBoxedAndPadded(t *testing.T) {
	path := writeMarkdownFile(t, "```go\nfmt.Println(1)\n```\n")
	_, scr := newMarkdownScreen(t, path, 40, 5)

	th := theme.Default()
	if bg := cellBG(t, scr, 0, 0); bg != th.ReviewBoxBG {
		t.Fatalf("code row background = %v, want ReviewBoxBG %v", bg, th.ReviewBoxBG)
	}
	// One cell of left padding: column 0 is blank background, the code
	// text starts at column 1.
	got := rowText(t, scr, 0)
	if strings.TrimLeft(got, " ") == got {
		t.Fatalf("code row %q has no leading padding", got)
	}
	if !strings.Contains(got, "fmt.Println(1)") {
		t.Fatalf("code row %q is missing the code text", got)
	}
}

// TestRenderMarkdown_LinkIsUnderlinedInSynProperty pins the link style
// the spec asks for by name.
func TestRenderMarkdown_LinkIsUnderlinedInSynProperty(t *testing.T) {
	path := writeMarkdownFile(t, "[Vincent](https://example.com)\n")
	_, scr := newMarkdownScreen(t, path, 60, 5)

	cells, w, _ := scr.GetContents()
	th := theme.Default()
	found := false
	for x := 0; x < w; x++ {
		c := cells[x]
		if len(c.Runes) == 0 || c.Runes[0] != 'V' {
			continue
		}
		fg, _, attr := c.Style.Decompose()
		if fg == th.SynProperty && attr&tcell.AttrUnderline != 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("did not find the link's leading rune underlined in SynProperty")
	}
}

// TestRenderMarkdown_NoGutter proves gutterCells is 0 for a markdown tab
// — there is no line-number column the way text and diff tabs have — and
// that the content sits exactly markdownGutter cells in: a margin, not a
// gutter, so the hit-tester and the painter agree on the same constant.
func TestRenderMarkdown_NoGutter(t *testing.T) {
	path := writeMarkdownFile(t, "hello\n")
	tab, scr := newMarkdownScreen(t, path, 40, 5)
	if got := tab.gutterCells(); got != 0 {
		t.Fatalf("gutterCells() = %d, want 0", got)
	}
	want := strings.Repeat(" ", markdownGutter) + "hello"
	if got := rowText(t, scr, 0); got != want {
		t.Fatalf("row 0 = %q, want %q", got, want)
	}
}

// TestMarkdownInnerWidth_MarginsGiveWayOnANarrowPane pins the fallback:
// the two margins come off the wrap width until the pane is too narrow
// for them to be worth having, at which point the text gets the full
// width back rather than wrapping into a sliver.
func TestMarkdownInnerWidth_MarginsGiveWayOnANarrowPane(t *testing.T) {
	if got := markdownInnerWidth(80); got != 80-2*markdownGutter {
		t.Fatalf("inner(80) = %d, want %d", got, 80-2*markdownGutter)
	}
	if got := markdownInnerWidth(18); got != 18 {
		t.Fatalf("inner(18) = %d, want the full 18", got)
	}
}

// TestRewrapMarkdown_ReflowsOnWidthChange proves a resize actually
// re-wraps the document rather than leaving stale line breaks from the
// previous width.
func TestRewrapMarkdown_ReflowsOnWidthChange(t *testing.T) {
	path := writeMarkdownFile(t, "one two three four five six seven eight\n")
	tab, err := NewMarkdownTab(path)
	if err != nil {
		t.Fatalf("NewMarkdownTab: %v", err)
	}
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(80, 24)

	tab.Render(scr, theme.Default(), 0, 0, 12, 10)
	wideRows := len(tab.MarkdownRows)
	if wideRows < 2 {
		t.Fatalf("expected wrapping at width 12, got %d rows", wideRows)
	}

	tab.Render(scr, theme.Default(), 0, 0, 80, 10)
	if len(tab.MarkdownRows) != 1 {
		t.Fatalf("at width 80 expected the line to fit on 1 row, got %d", len(tab.MarkdownRows))
	}
}

// TestSetMarkdownSource_ForcesRewrapOnNextRender proves the disk-reconcile
// / raw-toggle entry point actually takes effect on the next paint, even
// though the pane width hasn't changed.
func TestSetMarkdownSource_ForcesRewrapOnNextRender(t *testing.T) {
	path := writeMarkdownFile(t, "# One\n")
	tab, scr := newMarkdownScreen(t, path, 40, 5)
	if got := rowText(t, scr, 0); got != "  One" {
		t.Fatalf("row 0 = %q, want %q", got, "  One")
	}

	tab.SetMarkdownSource("# Two\n")
	tab.Render(scr, theme.Default(), 0, 0, 40, 5)
	scr.Show()
	if got := rowText(t, scr, 0); got != "  Two" {
		t.Fatalf("after SetMarkdownSource, row 0 = %q, want %q", got, "  Two")
	}
}

// TestMarkdownHitTest_MapsClickToRowAndColumn proves a click lands on the
// row/column it visually points at, with no gutter offset to account for.
func TestMarkdownHitTest_MapsClickToRowAndColumn(t *testing.T) {
	path := writeMarkdownFile(t, "abcde\n\nfghij\n")
	tab, _ := newMarkdownScreen(t, path, 40, 10)

	pos, ok := tab.HitTest(2+markdownGutter, 0, 40, 10)
	if !ok {
		t.Fatal("expected a hit on row 0")
	}
	if pos != (Position{Line: 0, Col: 2}) {
		t.Fatalf("hit = %+v, want {0 2} (click column less the margin)", pos)
	}
	// A click inside the left margin lands on column 0, never negative.
	if pos, ok := tab.HitTest(0, 0, 40, 10); !ok || pos.Col != 0 {
		t.Fatalf("margin click = %+v ok=%v, want column 0", pos, ok)
	}

	pos, ok = tab.HitTest(1+markdownGutter, 2, 40, 10)
	if !ok {
		t.Fatal("expected a hit on row 2")
	}
	if pos != (Position{Line: 2, Col: 1}) {
		t.Fatalf("hit = %+v, want {2 1}", pos)
	}

	if _, ok := tab.HitTest(0, 999, 40, 10); ok {
		t.Fatal("a click below the document should miss")
	}
}

// TestRenderMarkdown_FindMatchIsHighlighted proves the find bar's
// highlight machinery — generic over any Tab's Buffer — actually reaches
// the screen for a markdown tab, the same way it does for plain text.
func TestRenderMarkdown_FindMatchIsHighlighted(t *testing.T) {
	path := writeMarkdownFile(t, "find the needle here\n")
	tab, scr := newMarkdownScreen(t, path, 40, 5)

	tab.SetFindQuery("needle")
	tab.Render(scr, theme.Default(), 0, 0, 40, 5)
	scr.Show()

	th := theme.Default()
	idx := strings.Index("find the needle here", "needle") + markdownGutter
	if bg := cellBG(t, scr, idx, 0); bg != th.FindCurrent {
		t.Fatalf("match background = %v, want FindCurrent %v", bg, th.FindCurrent)
	}
}

// TestToggleMarkdownView_RoundTripPreservesPathAndSlot proves the
// invariant the whole feature rests on: toggling never allocates a new
// tab or changes Path — it mutates the same *Tab in place.
func TestToggleMarkdownView_RoundTripPreservesPathAndSlot(t *testing.T) {
	path := writeMarkdownFile(t, "# Title\n\nbody text\n")
	tab, err := NewMarkdownTab(path)
	if err != nil {
		t.Fatalf("NewMarkdownTab: %v", err)
	}
	if !tab.IsMarkdown() || tab.ReadOnly() != true {
		t.Fatal("expected a fresh markdown tab to be rendered and read-only")
	}

	tab.ToggleMarkdownView()
	if tab.IsMarkdown() {
		t.Fatal("expected the tab to be raw after one toggle")
	}
	if tab.ReadOnly() {
		t.Fatal("a raw tab must be editable")
	}
	if tab.Path != path {
		t.Fatalf("Path changed across the toggle: got %q, want %q", tab.Path, path)
	}
	if got := tab.Buffer.String(); got != "# Title\n\nbody text\n" && got != "# Title\n\nbody text" {
		t.Fatalf("raw buffer = %q, want the original markdown source", got)
	}

	tab.ToggleMarkdownView()
	if !tab.IsMarkdown() {
		t.Fatal("expected the tab to be rendered after a second toggle")
	}
	if !tab.ReadOnly() {
		t.Fatal("a rendered tab must be read-only")
	}
	if tab.Path != path {
		t.Fatalf("Path changed across the round trip: got %q, want %q", tab.Path, path)
	}
}

// TestToggleMarkdownView_PreservesEditsAcrossRoundTrip proves an edit made
// in raw mode survives a toggle to rendered and back — the render must
// read the CURRENT buffer, not re-read the file from disk.
func TestToggleMarkdownView_PreservesEditsAcrossRoundTrip(t *testing.T) {
	path := writeMarkdownFile(t, "# Original\n")
	tab, err := NewMarkdownTab(path)
	if err != nil {
		t.Fatalf("NewMarkdownTab: %v", err)
	}

	tab.ToggleMarkdownView() // -> raw
	tab.SelectAll()
	tab.DeleteSelection()
	tab.InsertString("# Edited\n")
	if !tab.Dirty {
		t.Fatal("expected the raw edit to mark the tab dirty")
	}

	tab.ToggleMarkdownView() // -> rendered, previewing the edit
	if !tab.Dirty {
		t.Fatal("toggling to rendered must not clear Dirty — nothing was saved")
	}
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(40, 5)
	tab.Render(scr, theme.Default(), 0, 0, 40, 5)
	scr.Show()
	if got := rowText(t, scr, 0); got != "  Edited" {
		t.Fatalf("rendered row 0 = %q, want the edited heading %q", got, "  Edited")
	}

	tab.ToggleMarkdownView() // -> raw again
	if got := tab.Buffer.String(); !strings.Contains(got, "Edited") {
		t.Fatalf("raw buffer after the round trip = %q, lost the edit", got)
	}
	if !tab.Dirty {
		t.Fatal("Dirty must survive the full round trip until an actual save")
	}
}

// TestToggleMarkdownView_NonMarkdownTabIsNoOp proves the guard: a plain
// text tab, or a non-.md path, does nothing when toggled.
func TestToggleMarkdownView_NonMarkdownTabIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	tab, err := NewTab(path)
	if err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	before := tab.Buffer.String()
	tab.ToggleMarkdownView()
	if tab.IsMarkdown() {
		t.Fatal("a .go tab must never become a markdown view")
	}
	if tab.Buffer.String() != before {
		t.Fatal("toggling a non-markdown tab must not touch its buffer")
	}
}

// TestToggleMarkdownView_PreservesScrollFraction proves the scroll
// position survives a round trip as roughly the same FRACTION of the
// document, since raw and rendered have different row counts for the
// same content.
func TestToggleMarkdownView_PreservesScrollFraction(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line "+string(rune('a'+i%26)))
	}
	path := writeMarkdownFile(t, strings.Join(lines, "\n\n")+"\n")
	tab, err := NewMarkdownTab(path)
	if err != nil {
		t.Fatalf("NewMarkdownTab: %v", err)
	}
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("screen init: %v", err)
	}
	t.Cleanup(scr.Fini)
	scr.SetSize(40, 10)
	tab.Render(scr, theme.Default(), 0, 0, 40, 10) // lay out MarkdownRows/Buffer.

	total := tab.Buffer.LineCount()
	tab.ScrollY = total / 2
	wantFrac := float64(tab.ScrollY) / float64(total)

	tab.ToggleMarkdownView() // -> raw
	rawTotal := tab.Buffer.LineCount()
	gotFrac := float64(tab.ScrollY) / float64(rawTotal)
	if diff := gotFrac - wantFrac; diff < -0.05 || diff > 0.05 {
		t.Fatalf("raw scroll fraction = %.3f, want close to %.3f", gotFrac, wantFrac)
	}
}

// TestReload_MarkdownTabReRendersFromDisk proves Reload (the conflict
// modal's "Reload" button, defensively) re-reads the file and re-lays it
// out rather than falling into the plain-text NewBuffer path that would
// show the raw markdown source as if it were the rendered view.
func TestReload_MarkdownTabReRendersFromDisk(t *testing.T) {
	path := writeMarkdownFile(t, "# One\n")
	tab, err := NewMarkdownTab(path)
	if err != nil {
		t.Fatalf("NewMarkdownTab: %v", err)
	}
	if err := os.WriteFile(path, []byte("# Two\n"), 0644); err != nil {
		t.Fatalf("rewrite fixture: %v", err)
	}
	if err := tab.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if tab.MarkdownSource != "# Two\n" {
		t.Fatalf("MarkdownSource = %q, want the reloaded content", tab.MarkdownSource)
	}
	if !tab.IsMarkdown() {
		t.Fatal("Reload must not change Mode")
	}
}
