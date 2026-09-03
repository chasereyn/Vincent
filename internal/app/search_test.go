// =============================================================================
// File: internal/app/search_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/finder"
	"github.com/chasereyn/vincent/internal/search"
)

// withSearchApp wires up an App with a warm, ready Finder rooted at a
// tempdir seeded with a couple of files — the file list runSearchIfCurrent
// hands to the engine comes from a.finder.Paths(), so any test that drives
// an actual search run needs one. Mirrors finder_test.go's withFinder.
func withSearchApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	a := newTestApp(t, dir)
	a.finder = finder.New(a.rootDir)
	done := make(chan struct{})
	a.finder.Rebuild(func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("finder did not become ready")
	}
	return a, dir
}

// fakeEngineCall records one invocation of a fake search engine, so a test
// can assert what the modal actually asked it to search for.
type fakeEngineCall struct {
	files []string
	root  string
	query string
}

// fakeSearchEngine returns a searchEngineFunc that ignores the real
// filesystem: it hands back the canned matches on a buffered channel,
// already closed, and calls onDone with outcome — synchronously, before
// returning, matching the real engine's "channel closes, then onDone"
// order closely enough for the event-flow tests below. calls, when
// non-nil, gets one entry appended per invocation.
func fakeSearchEngine(calls *[]fakeEngineCall, matches []search.Match, outcome search.Outcome) searchEngineFunc {
	return func(_ context.Context, files []string, root, query string, _ search.Options, onDone func(search.Outcome)) <-chan search.Match {
		if calls != nil {
			*calls = append(*calls, fakeEngineCall{files: files, root: root, query: query})
		}
		ch := make(chan search.Match, len(matches))
		for _, m := range matches {
			ch <- m
		}
		close(ch)
		if onDone != nil {
			onDone(outcome)
		}
		return ch
	}
}

// fireDebounceNow stops the real debounce timer (if one is armed) and
// directly invokes runSearchIfCurrent for the current generation — the
// deterministic stand-in tests use instead of sleeping past the real
// 150ms window. Returns the generation it fired for.
func fireDebounceNow(a *App) int {
	if a.search.debounce != nil {
		a.search.debounce.Stop()
	}
	gen := a.search.generation
	a.runSearchIfCurrent(gen)
	return gen
}

// awaitSearchDone pumps the screen's event queue — applying every event
// via handleEvent, exactly as the real loop would — until a
// searchResultsEvent with done=true for generation gen is applied, or the
// timeout elapses. Mirrors gitpoll_test.go's awaitGitPoll.
func awaitSearchDone(t *testing.T, a *App, gen int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ev := a.screen.PollEvent()
		if ev == nil {
			t.Fatal("screen closed before the search finished")
		}
		a.handleEvent(ev)
		if sr, ok := ev.(*searchResultsEvent); ok && sr.done && sr.generation == gen {
			return
		}
	}
	t.Fatal("timed out waiting for the search to finish")
}

// -----------------------------------------------------------------------------
// Open / guard
// -----------------------------------------------------------------------------

// TestOpenSearch_SingleFileModeGuard pins that the modal refuses to open
// when there's no project tree to index — the same guard openFinder has,
// for the same reason: there is no file list to search.
func TestOpenSearch_SingleFileModeGuard(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.tree = nil
	a.openSearch()
	if a.search.open {
		t.Fatal("search should not open in single-file mode")
	}
}

// TestOpenSearch_ResetsStateButKeepsInjectedEngine pins that reopening the
// modal starts from a clean slate (no leftover query or results from a
// previous session) while preserving a test-injected engine rather than
// losing it to the fresh zero-value searchState.
func TestOpenSearch_ResetsStateButKeepsInjectedEngine(t *testing.T) {
	a, _ := withSearchApp(t)
	called := false
	a.search.engine = func(ctx context.Context, files []string, root, query string, opts search.Options, onDone func(search.Outcome)) <-chan search.Match {
		called = true
		ch := make(chan search.Match)
		close(ch)
		if onDone != nil {
			onDone(search.Outcome{})
		}
		return ch
	}
	a.search.query = []rune("leftover")
	a.search.results = []search.Match{{Path: "x.go"}}

	a.openSearch()

	if !a.search.open {
		t.Fatal("expected the modal to be open")
	}
	if len(a.search.query) != 0 || a.search.results != nil {
		t.Fatalf("expected a clean slate, got query=%q results=%v", string(a.search.query), a.search.results)
	}
	a.handleSearchKey(keyEv(tcell.KeyRune, 'x'))
	gen := fireDebounceNow(a)
	// The engine runs on a goroutine; wait for its done event (posted
	// only after it flushes, per runSearchIfCurrent's ordering guarantee)
	// before reading `called`, rather than racing a bare bool against a
	// goroutine that may not have been scheduled yet.
	awaitSearchDone(t, a, gen)
	if !called {
		t.Fatal("expected the injected engine to survive openSearch")
	}
}

// -----------------------------------------------------------------------------
// Debounce + generation
// -----------------------------------------------------------------------------

// TestSearchQueryChanged_StaleDebounceDropped pins the core of the
// debounce/generation contract: a debounce event delivered for a
// generation the query has since moved past must not start a search, and
// the CURRENT generation's debounce must still work.
func TestSearchQueryChanged_StaleDebounceDropped(t *testing.T) {
	a, _ := withSearchApp(t)
	var calls []fakeEngineCall
	a.search.engine = fakeSearchEngine(&calls, nil, search.Outcome{})
	a.openSearch()

	a.handleSearchKey(keyEv(tcell.KeyRune, 'f'))
	staleGen := a.search.generation
	if a.search.debounce == nil {
		t.Fatal("expected a debounce timer to be armed after typing")
	}

	// A second keystroke supersedes the first before its debounce fires.
	// searchQueryChanged's own cancellation stops the first timer, so
	// there's no real timer left to race with the manual delivery below.
	a.handleSearchKey(keyEv(tcell.KeyRune, 'o'))
	if a.search.generation == staleGen {
		t.Fatal("expected generation to advance on the second keystroke")
	}

	// Deliver the stale generation's debounce directly, as if its timer
	// had (impossibly) still fired: must be a no-op.
	a.runSearchIfCurrent(staleGen)
	if len(calls) != 0 {
		t.Fatalf("stale debounce event started a search: %d calls", len(calls))
	}

	// The current generation's debounce firing DOES start a search.
	gen := fireDebounceNow(a)
	awaitSearchDone(t, a, gen)
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 engine call, got %d", len(calls))
	}
	if calls[0].query != "fo" {
		t.Fatalf("query: got %q, want %q", calls[0].query, "fo")
	}
	if gen != a.search.generation {
		t.Fatalf("fireDebounceNow generation mismatch: %d vs %d", gen, a.search.generation)
	}
}

// TestApplySearchResults_DropsStaleGeneration pins the other half of the
// same guard: a batch event for a superseded generation must not touch
// the results the reviewer is currently looking at.
func TestApplySearchResults_DropsStaleGeneration(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.generation = 5
	a.applySearchResults(&searchResultsEvent{
		generation: 4,
		batch:      []search.Match{{Path: "x.go", Line: 1}},
	})
	if len(a.search.results) != 0 {
		t.Fatal("stale batch should have been dropped")
	}
}

// TestSearchQueryChanged_ClearingQueryStopsSearching pins that emptying
// the field back out (e.g. holding Backspace) cancels any in-flight run
// and drops back to the idle "type to search" state rather than leaving
// "searching…" on screen for a query that no longer exists.
func TestSearchQueryChanged_ClearingQueryStopsSearching(t *testing.T) {
	a, _ := withSearchApp(t)
	a.search.engine = fakeSearchEngine(nil, nil, search.Outcome{})
	a.openSearch()

	a.handleSearchKey(keyEv(tcell.KeyRune, 'x'))
	if !a.search.searching {
		t.Fatal("expected searching=true right after typing")
	}
	a.handleSearchKey(keyEv(tcell.KeyBackspace, 0))
	if a.search.searching {
		t.Fatal("expected searching=false once the query is empty again")
	}
	if a.search.debounce != nil {
		t.Fatal("expected no debounce timer armed for an empty query")
	}
}

// -----------------------------------------------------------------------------
// Event flow (fake engine)
// -----------------------------------------------------------------------------

// TestSearchEventFlow_FakeEngineDeliversSortedResults is the round trip:
// type a query, let the (stubbed) debounce fire, drain the real event
// queue exactly as the main loop would, and check the modal ends up with
// the matches sorted by path then line and the outcome fields applied.
func TestSearchEventFlow_FakeEngineDeliversSortedResults(t *testing.T) {
	a, _ := withSearchApp(t)
	matches := []search.Match{
		{Path: "b.go", Line: 5, Col: 1, Text: "bbb"},
		{Path: "a.go", Line: 2, Col: 1, Text: "aaa"},
		{Path: "a.go", Line: 1, Col: 1, Text: "aaa"},
	}
	outcome := search.Outcome{Matches: 3, FilesScanned: 2, Capped: false}
	a.search.engine = fakeSearchEngine(nil, matches, outcome)

	a.openSearch()
	a.handleSearchKey(keyEv(tcell.KeyRune, 'x'))
	gen := fireDebounceNow(a)
	awaitSearchDone(t, a, gen)

	if len(a.search.results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(a.search.results))
	}
	want := []string{"a.go:1", "a.go:2", "b.go:5"}
	for i, w := range want {
		got := a.search.results[i].Path + ":" + itoa(a.search.results[i].Line)
		if got != w {
			t.Fatalf("result[%d]: got %s, want %s (results not sorted by path then line)", i, got, w)
		}
	}
	if a.search.matches != 3 || a.search.files != 2 || a.search.capped {
		t.Fatalf("outcome fields: matches=%d files=%d capped=%v", a.search.matches, a.search.files, a.search.capped)
	}
	if a.search.searching {
		t.Fatal("expected searching=false once the run is done")
	}
	if got := a.searchFooterText(); got != "3 matches in 2 files" {
		t.Fatalf("footer: got %q", got)
	}
}

// TestSearchEventFlow_UsesFinderIndexAsFileList pins that runSearchIfCurrent
// hands the engine the SAME file list the finder's index has — content
// search is Phase 8b's second consumer of that multi-root index, not a
// separate walk of the filesystem.
func TestSearchEventFlow_UsesFinderIndexAsFileList(t *testing.T) {
	a, _ := withSearchApp(t)
	var calls []fakeEngineCall
	a.search.engine = fakeSearchEngine(&calls, nil, search.Outcome{})

	a.openSearch()
	a.handleSearchKey(keyEv(tcell.KeyRune, 'x'))
	gen := fireDebounceNow(a)
	awaitSearchDone(t, a, gen)

	if len(calls) != 1 {
		t.Fatalf("expected 1 engine call, got %d", len(calls))
	}
	want := a.finder.Paths()
	if len(calls[0].files) != len(want) {
		t.Fatalf("files handed to engine: got %v, want %v", calls[0].files, want)
	}
	if calls[0].root != a.rootDir {
		t.Fatalf("root handed to engine: got %q, want %q", calls[0].root, a.rootDir)
	}
}

// -----------------------------------------------------------------------------
// Close / cancellation
// -----------------------------------------------------------------------------

// TestCloseSearch_CancelsInFlightRun pins that dismissing the modal
// mid-search cancels the engine run's context, rather than leaving a
// worker pool grepping the whole root for a query nobody's watching any
// more.
func TestCloseSearch_CancelsInFlightRun(t *testing.T) {
	a, _ := withSearchApp(t)
	ctxCh := make(chan context.Context, 1)
	a.search.engine = func(ctx context.Context, files []string, root, query string, opts search.Options, onDone func(search.Outcome)) <-chan search.Match {
		ctxCh <- ctx
		// Deliberately never closes on its own — only ctx cancellation
		// should end this run, which is exactly the behaviour this test
		// is pinning. runSearchIfCurrent's own ctx.Done() case (see
		// search.go) is what keeps this from leaking the reader
		// goroutine forever once closeSearch cancels ctx below.
		return make(chan search.Match)
	}
	a.openSearch()
	a.handleSearchKey(keyEv(tcell.KeyRune, 'x'))
	fireDebounceNow(a)

	var ctx context.Context
	select {
	case ctx = <-ctxCh:
	case <-time.After(2 * time.Second):
		t.Fatal("engine was never invoked")
	}
	if ctx.Err() != nil {
		t.Fatal("context should not be cancelled before closeSearch runs")
	}
	a.closeSearch()
	if ctx.Err() == nil {
		t.Fatal("expected closeSearch to cancel the in-flight run's context")
	}
}

// TestHandleSearchKey_EscClosesAndCancels pins the keyboard path to the
// same guarantee: Esc is how a reviewer actually dismisses the modal.
func TestHandleSearchKey_EscClosesAndCancels(t *testing.T) {
	a, _ := withSearchApp(t)
	ctxCh := make(chan context.Context, 1)
	a.search.engine = func(ctx context.Context, files []string, root, query string, opts search.Options, onDone func(search.Outcome)) <-chan search.Match {
		ctxCh <- ctx
		return make(chan search.Match)
	}
	a.openSearch()
	a.handleSearchKey(keyEv(tcell.KeyRune, 'x'))
	fireDebounceNow(a)

	var ctx context.Context
	select {
	case ctx = <-ctxCh:
	case <-time.After(2 * time.Second):
		t.Fatal("engine was never invoked")
	}

	a.handleSearchKey(keyEv(tcell.KeyEsc, 0))
	if a.search.open {
		t.Fatal("Esc should have closed the modal")
	}
	if ctx.Err() == nil {
		t.Fatal("Esc should have cancelled the in-flight run")
	}
}

// -----------------------------------------------------------------------------
// Opening a result
// -----------------------------------------------------------------------------

// TestOpenSelectedSearchResult_LandsCursorOnLine pins the whole point of
// the modal: Enter (or a click) opens the file and puts the cursor on the
// matched line and column, not just at the top of the file.
func TestOpenSelectedSearchResult_LandsCursorOnLine(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hit.go")
	if err := os.WriteFile(target, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := newTestApp(t, dir)
	a.search.open = true
	a.search.results = []search.Match{{Path: "hit.go", Line: 2, Col: 3}}
	a.search.selected = 0

	a.openSelectedSearchResult()

	if a.search.open {
		t.Fatal("expected the modal to close after opening a result")
	}
	tab := a.activeTabPtr()
	if tab == nil {
		t.Fatal("expected the file to open in a tab")
	}
	if tab.Cursor.Line != 1 || tab.Cursor.Col != 2 {
		t.Fatalf("cursor: got %+v, want Line=1 Col=2 (0-based for 1-based Line=2 Col=3)", tab.Cursor)
	}
}

// TestOpenSelectedSearchResult_EmptyResultsNoop pins that Enter on an
// empty result list (e.g. mashed before any match arrived) is a silent
// no-op rather than a panic on an out-of-range index.
func TestOpenSelectedSearchResult_EmptyResultsNoop(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	a.search.selected = 0
	a.openSelectedSearchResult() // must not panic
	if !a.search.open {
		t.Fatal("a no-op Enter should not have closed the modal")
	}
}

// TestHandleSearchKey_EnterOpensSelected wires Enter itself, not just the
// underlying method, so a routing regression in handleSearchKey would
// fail this even if openSelectedSearchResult itself were fine.
func TestHandleSearchKey_EnterOpensSelected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hit.go"), []byte("a\nb\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := newTestApp(t, dir)
	a.search.open = true
	a.search.results = []search.Match{{Path: "hit.go", Line: 1, Col: 1}}

	a.handleSearchKey(keyEv(tcell.KeyEnter, 0))

	if a.search.open {
		t.Fatal("Enter should have closed the modal")
	}
	if tab := a.activeTabPtr(); tab == nil || tab.Path != filepath.Join(dir, "hit.go") {
		t.Fatal("Enter should have opened the matched file")
	}
}

// -----------------------------------------------------------------------------
// Mouse
// -----------------------------------------------------------------------------

// TestHandleSearchMouse_WheelScrolls pins that the wheel moves listScroll
// without touching the selection — same contract as the root picker's
// wheel handling.
func TestHandleSearchMouse_WheelScrolls(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	for i := 0; i < searchResultsVisible+5; i++ {
		a.search.results = append(a.search.results, search.Match{Path: "f.go", Line: i + 1, Text: "x"})
	}
	a.drawSearch()

	a.handleSearchMouse(0, 0, tcell.WheelDown)
	if a.search.listScroll != 1 {
		t.Fatalf("listScroll after wheel down: got %d, want 1", a.search.listScroll)
	}
	a.handleSearchMouse(0, 0, tcell.WheelUp)
	if a.search.listScroll != 0 {
		t.Fatalf("listScroll after wheel up: got %d, want 0", a.search.listScroll)
	}
}

// TestHandleSearchMouse_HoverSelectsRowUnderPointer pins that motion over
// a result row (no button held) moves the selection to that row, the same
// "scrub without clicking" behaviour the finder modal has.
func TestHandleSearchMouse_HoverSelectsRowUnderPointer(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	a.search.results = []search.Match{
		{Path: "a.go", Line: 1, Text: "x"},
		{Path: "b.go", Line: 2, Text: "y"},
		{Path: "c.go", Line: 3, Text: "z"},
	}
	a.drawSearch()

	if len(a.search.rowRects) != 3 {
		t.Fatalf("expected 3 row rects recorded, got %d", len(a.search.rowRects))
	}
	target := a.search.rowRects[2]
	mx, _, _, _ := a.searchModalRect()
	a.handleSearchMouse(mx+2, target.y, tcell.ButtonNone)
	if a.search.selected != target.index {
		t.Fatalf("selected: got %d, want %d (row under pointer)", a.search.selected, target.index)
	}
}

// TestHandleSearchMouse_ClickOutsideCloses pins the dismiss gesture every
// other modal in Vincent shares: a click outside the box closes it.
func TestHandleSearchMouse_ClickOutsideCloses(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	a.drawSearch()
	a.handleSearchMouse(0, 0, tcell.Button1)
	if a.search.open {
		t.Fatal("click outside the modal should close it")
	}
}

// TestHandleSearchMouse_ClickOnRowOpensIt pins the click-to-open path,
// separate from hover: clicking (not just hovering over) a row should
// open it and close the modal.
func TestHandleSearchMouse_ClickOnRowOpensIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hit.go"), []byte("a\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a := newTestApp(t, dir)
	a.search.open = true
	a.search.results = []search.Match{{Path: "hit.go", Line: 1, Col: 1}}
	a.drawSearch()

	rect := a.search.rowRects[0]
	mx, _, _, _ := a.searchModalRect()
	a.handleSearchMouse(mx+2, rect.y, tcell.Button1)

	if a.search.open {
		t.Fatal("clicking a row should close the modal")
	}
	if tab := a.activeTabPtr(); tab == nil {
		t.Fatal("clicking a row should have opened the file")
	}
}

// -----------------------------------------------------------------------------
// Drawing (SimulationScreen)
// -----------------------------------------------------------------------------

// TestDrawSearch_PaintsTitleAndFooter pins that the layout actually
// reaches the screen: the title row and the footer's match-count text.
func TestDrawSearch_PaintsTitleAndFooter(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	a.search.query = []rune("needle")
	a.search.matches = 2
	a.search.files = 1
	a.drawSearch()
	a.screen.Show()

	_, my, _, mh := a.searchModalRect()
	title := pickerScreenText(t, a, my+1)
	if !strings.Contains(title, "Find in files") {
		t.Fatalf("title row: got %q", title)
	}
	footer := pickerScreenText(t, a, my+mh-2)
	if !strings.Contains(footer, "2 matches in 1 files") {
		t.Fatalf("footer row: got %q", footer)
	}
}

// TestDrawSearch_HighlightsMatchSpan pins the one piece of visual
// information the whole modal exists to surface: the matched substring
// within a result's line is picked out in theme.FindCurrent, not just
// present in the row's plain text.
func TestDrawSearch_HighlightsMatchSpan(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	a.search.results = []search.Match{
		{Path: "a.go", Line: 3, Text: "hello needle world", MatchStart: 6, MatchLen: 6},
	}
	a.drawSearch()
	a.screen.Show()

	if len(a.search.rowRects) != 1 {
		t.Fatalf("expected 1 row rect, got %d", len(a.search.rowRects))
	}
	rect := a.search.rowRects[0]
	row := pickerScreenText(t, a, rect.y)
	if !strings.Contains(row, "a.go") || !strings.Contains(row, "needle") {
		t.Fatalf("row text: got %q, want it to contain the path and the matched word", row)
	}

	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	// pickerScreenText's row is one rune per terminal column, but the
	// modal's border draws multi-byte box-drawing runes (│) earlier in
	// the line — strings.Index returns a BYTE offset, which overshoots
	// the actual column once those wider-than-one-byte runes precede the
	// match. Count runes up to the match instead of trusting the byte
	// offset as a column.
	byteIdx := strings.Index(row, "needle")
	if byteIdx < 0 {
		t.Fatal("could not locate \"needle\" in the rendered row")
	}
	idx := len([]rune(row[:byteIdx]))
	fg, _, _ := cells[rect.y*w+idx].Style.Decompose()
	if fg != a.theme.FindCurrent {
		t.Fatalf("matched span colour: got %v, want theme.FindCurrent %v", fg, a.theme.FindCurrent)
	}
	// The character just before the match (part of "hello ") must NOT
	// carry the highlight, or the whole row would read as one colour.
	fgBefore, _, _ := cells[rect.y*w+idx-1].Style.Decompose()
	if fgBefore == a.theme.FindCurrent {
		t.Fatal("highlight bled into text before the match")
	}
}

// TestDrawSearch_SelectedRowGetsDistinctBackground pins that the selected
// row reads as a single highlighted block, the same visual language the
// finder and root picker use for their own selection.
func TestDrawSearch_SelectedRowGetsDistinctBackground(t *testing.T) {
	a := newTestApp(t, t.TempDir())
	a.search.open = true
	a.search.results = []search.Match{
		{Path: "a.go", Line: 1, Text: "x"},
		{Path: "b.go", Line: 2, Text: "y"},
	}
	a.search.selected = 1
	a.drawSearch()
	a.screen.Show()

	cells, w, _ := a.screen.(tcell.SimulationScreen).GetContents()
	selectedY := a.search.rowRects[1].y
	unselectedY := a.search.rowRects[0].y
	mx, _, _, _ := a.searchModalRect()

	_, bgSel, _ := cells[selectedY*w+mx+1].Style.Decompose()
	_, bgUnsel, _ := cells[unselectedY*w+mx+1].Style.Decompose()
	if bgSel == bgUnsel {
		t.Fatal("selected row should have a different background than an unselected row")
	}
	if bgSel != a.theme.BG {
		t.Fatalf("selected row background: got %v, want theme.BG %v", bgSel, a.theme.BG)
	}
}
