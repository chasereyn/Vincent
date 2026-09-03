// =============================================================================
// File: internal/app/search.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream equivalent. Phase 8b: content search (Esc F) across every
// file the project index knows about. Modal shape and mechanics borrowed
// deliberately from internal/app/finder.go (Spicer Matthews / Cloudmanic,
// MIT) — centered box, query field on top, hover-follows-mouse rows below
// — so the two search modals feel like the same feature at two scopes.
// =============================================================================

// search.go is the Esc-F content search modal: a query field over a list
// of grep-style hits streamed in from internal/search's worker pool. It
// owns none of the actual searching — that is internal/search.Search,
// which is pure and knows nothing about tcell — this file's job is
// turning keystrokes into search runs and search runs into pixels.
//
// The event flow, matching the goroutine-to-UI-thread rule every other
// background worker in Vincent follows (see gitpoll.go, finder.go's
// Rebuild):
//
//   - Every query-changing keystroke calls searchQueryChanged, which
//     bumps a generation counter, cancels whatever the previous
//     keystroke had in flight (a pending debounce timer AND a
//     mid-search engine run — cancelling immediately, not waiting for
//     the new debounce, is what stops a fast typist from piling up
//     abandoned searches burning CPU on the internal/search worker
//     pool), and arms a new 150ms debounce timer tagged with the new
//     generation.
//   - The debounce timer is a plain time.AfterFunc — no goroutine of our
//     own to manage — that posts a searchDebounceEvent carrying its
//     generation. handleEvent routes it to runSearchIfCurrent, which
//     starts the actual engine run ONLY if nothing has superseded that
//     generation since the timer was armed.
//   - runSearchIfCurrent launches one goroutine that reads the engine's
//     match channel, batches results (time- and size-bounded so a huge
//     result set doesn't post one tcell event per match), and
//     PostEvents each batch as a searchResultsEvent — plus a final one
//     when the engine's onDone callback reports the run's Outcome. Both
//     event kinds carry the generation they were produced for;
//     applySearchResults and runSearchIfCurrent both drop anything whose
//     generation doesn't match the CURRENT generation, so a slow batch
//     from an abandoned query can never appear to answer the query on
//     screen now.
//
// engine is an injectable field (searchState.engine) rather than a
// package-level call to search.Search, for the same reason gitwrite.go's
// gitRunner is injectable: tests substitute a fake that returns
// synthetic matches on a channel they control, so the debounce/
// generation/event-flow logic can be pinned without touching a real
// filesystem or waiting on real timers.

package app

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/finder"
	"github.com/chasereyn/vincent/internal/search"
)

const (
	// searchModalMaxWidth is wider than the finder's 80: a row here has
	// to fit a repo-relative path, a line number, AND a snippet of code,
	// where the finder only ever draws a path.
	searchModalMaxWidth = 104
	// searchResultsVisible is how many result rows are painted at once.
	searchResultsVisible = 12
	// searchDebounceDelay is how long the query field waits after the
	// last keystroke before actually starting a search. 150ms is short
	// enough that typing still feels live, long enough that a normal
	// typing burst doesn't launch a search per keystroke.
	searchDebounceDelay = 150 * time.Millisecond
	// searchFlushInterval bounds how often a running search's batches
	// reach the screen. Without a cap, a query that matches thousands of
	// lines in a huge fast file would post a tcell event per match.
	searchFlushInterval = 50 * time.Millisecond
	// searchFlushBatchSize is the other half of that cap: flush early if
	// a batch gets this big, so a very fast match rate still updates the
	// modal well inside one flush interval's worth of visible lag.
	searchFlushBatchSize = 40
)

// searchEngineFunc is the shape of internal/search.Search — extracted so
// tests can inject a fake without the real engine touching the
// filesystem or a real 150ms clock. See withFakeSearch in search_test.go.
type searchEngineFunc func(ctx context.Context, files []string, root, query string, opts search.Options, onDone func(search.Outcome)) <-chan search.Match

// searchRowRect is one drawn result row's screen position, recorded
// during drawSearch so handleSearchMouse can hit-test against exactly
// what was painted rather than recomputing row arithmetic — the same
// discipline the Changes panel's row rects follow.
type searchRowRect struct {
	y     int
	index int // index into a.search.results
}

// searchState is all of the Esc-F modal's state: the query field, the
// accumulated results, and the bookkeeping (generation, debounce timer,
// cancel func) that keeps a fast typist's abandoned searches from piling
// up. A struct rather than flattened App fields (unlike the older
// finder*, which predates this convention) because the debounce timer
// and the cancel func have to travel together and be cleared together.
type searchState struct {
	open bool

	query  []rune
	cursor int
	scroll int // horizontal scroll of the input field

	results    []search.Match
	selected   int
	listScroll int

	// generation increments on every query-changing keystroke. A
	// searchDebounceEvent or searchResultsEvent carrying an older
	// generation than this is stale — produced for a query the user has
	// since changed — and is dropped rather than applied.
	generation int

	// searching is true from the first keystroke of a query until that
	// query's engine run reports its Outcome (or the query is cleared).
	// It covers both the debounce window and the actual scan, because
	// from the reviewer's perspective both are "still working on it".
	searching bool

	// Outcome of the last completed run for the CURRENT generation.
	matches int
	files   int
	capped  bool

	// debounce is the pending timer for the query as it currently reads.
	// Stopped and replaced on every keystroke; nil when no debounce is
	// armed (either idle, or the timer already fired).
	debounce *time.Timer

	// cancel stops the currently in-flight engine run's context. Called
	// (and cleared) whenever the query changes again or the modal
	// closes, so an abandoned search's workers stop reading files for a
	// query nobody's waiting on any more.
	cancel context.CancelFunc

	// engine actually runs the search. Sitting on the struct rather than
	// being a bare package call to search.Search is what lets tests
	// substitute a deterministic fake — see searchEngineFunc.
	engine searchEngineFunc

	// rowRects is the hit-test snapshot recorded during the last
	// drawSearch.
	rowRects []searchRowRect
}

// searchDebounceEvent is posted when a query's 150ms debounce window
// elapses with no further keystroke. Carries the generation the timer
// was armed for so a stale one (superseded by a newer keystroke's own
// debounce) is recognised and dropped rather than starting a search for
// a query that's no longer on screen.
type searchDebounceEvent struct {
	when       time.Time
	generation int
}

// When satisfies the tcell.Event interface.
func (e *searchDebounceEvent) When() time.Time { return e.when }

// searchResultsEvent carries one batch of matches, the run's final
// Outcome, or both together at the very end of a run. done distinguishes
// "this batch also happens to be the last one" from "there may be more
// batches coming" — outcome is only meaningful when done is true.
type searchResultsEvent struct {
	when       time.Time
	generation int
	batch      []search.Match
	done       bool
	outcome    search.Outcome
}

// When satisfies the tcell.Event interface.
func (e *searchResultsEvent) When() time.Time { return e.when }

// openSearch shows the content-search modal. Guarded the same way
// openFinder is: single-file mode has no project index to search over.
// Reopening after a previous close keeps a test-injected engine (if any)
// rather than losing it to the fresh zero-value searchState.
func (a *App) openSearch() {
	if a.tree == nil {
		a.flash("Find in files isn't available in single-file mode")
		return
	}
	engine := a.search.engine
	if engine == nil {
		engine = search.Search
	}
	a.closeAllModals()
	a.search = searchState{open: true, engine: engine}
	if a.finder != nil && a.finder.State() != finder.StateReady {
		// The finder's index is what supplies the file list (see
		// runSearchIfCurrent); a rebuild already in flight (or the
		// finder's periodic invalidation) will pick this up too, but a
		// cold open shouldn't require the reviewer to nudge the finder
		// modal first just to warm the cache.
		scr := a.screen
		a.finder.Rebuild(func() {
			_ = scr.PostEvent(&finderRebuiltEvent{when: time.Now()})
		})
	}
}

// closeSearch dismisses the modal and stops any work in flight: the
// debounce timer, if a keystroke's 150ms window hasn't elapsed yet, and
// the engine run's own context, if one is actively scanning files.
// Without this, closing the modal mid-search would leave a worker pool
// grepping the whole root for a query nobody is looking at results for.
func (a *App) closeSearch() {
	if a.search.debounce != nil {
		a.search.debounce.Stop()
		a.search.debounce = nil
	}
	if a.search.cancel != nil {
		a.search.cancel()
		a.search.cancel = nil
	}
	a.search.open = false
}

// searchQueryChanged fires on every edit to the query field. It clears
// the stale result list immediately (so a fast typist never sees results
// for three keystrokes ago hang around), cancels whatever the previous
// keystroke had in flight, bumps the generation counter, and — for a
// non-empty query — arms a fresh debounce timer.
func (a *App) searchQueryChanged() {
	a.search.selected = 0
	a.search.listScroll = 0
	a.search.results = nil
	a.search.matches = 0
	a.search.files = 0
	a.search.capped = false
	a.search.generation++
	if a.search.debounce != nil {
		a.search.debounce.Stop()
		a.search.debounce = nil
	}
	if a.search.cancel != nil {
		a.search.cancel()
		a.search.cancel = nil
	}
	if len(a.search.query) == 0 {
		a.search.searching = false
		return
	}
	a.search.searching = true
	a.scheduleSearch()
}

// scheduleSearch (re)arms the debounce timer for the query as it
// currently reads, tagged with the generation live right now. Pulled out
// of searchQueryChanged so the finderRebuiltEvent handler can also call
// it — a query typed before the index finished its first build has
// nothing to search yet (see runSearchIfCurrent), and once the index
// lands that query deserves a search without the reviewer typing again.
func (a *App) scheduleSearch() {
	gen := a.search.generation
	scr := a.screen
	a.search.debounce = time.AfterFunc(searchDebounceDelay, func() {
		_ = scr.PostEvent(&searchDebounceEvent{when: time.Now(), generation: gen})
	})
}

// runSearchIfCurrent starts the actual engine run for generation, unless
// a newer keystroke has already moved the query on since the debounce
// timer was armed. Main-thread only (it's called from handleEvent).
func (a *App) runSearchIfCurrent(generation int) {
	if !a.search.open || generation != a.search.generation {
		return
	}
	a.search.debounce = nil
	if a.finder == nil {
		a.search.searching = false
		return
	}
	files := a.finder.Paths()
	if files == nil {
		// Index not built yet. openSearch already kicked a rebuild;
		// when it lands, finderRebuiltEvent's handler re-schedules this
		// same query rather than leaving it stuck on "searching…".
		return
	}

	query := string(a.search.query)
	root := a.rootDir
	scr := a.screen
	engine := a.search.engine
	ctx, cancel := context.WithCancel(context.Background())
	a.search.cancel = cancel

	// outcomeCh decouples the engine's onDone callback from posting the
	// "done" event directly. That matters because search.Search's own
	// completion goroutine does close(out) THEN onDone(...) — two
	// separate statements with no synchronization forcing them together
	// — while THIS goroutine's read loop notices the close and flushes
	// its last buffered batch independently. If onDone posted the done
	// event itself, the two goroutines would be racing to post to the
	// screen's queue, and a done event that lands before the final batch
	// event would show the reviewer a final match count with a shorter
	// result list still catching up. Routing the outcome through this
	// goroutine instead means the done event is always the LAST thing
	// this goroutine posts, after every batch — see the natural-
	// completion branch below.
	outcomeCh := make(chan search.Outcome, 1)

	// Everything captured above is a plain value (or, for scr, already
	// safe to call from any goroutine) — nothing in this goroutine
	// touches App. Results only ever reach UI state through the
	// searchResultsEvent PostEvents below, applied on the main thread by
	// applySearchResults.
	go func() {
		ch := engine(ctx, files, root, query, search.Options{}, func(o search.Outcome) {
			outcomeCh <- o
		})
		var buf []search.Match
		flush := func() {
			if len(buf) == 0 {
				return
			}
			batch := buf
			buf = nil
			_ = scr.PostEvent(&searchResultsEvent{when: time.Now(), generation: generation, batch: batch})
		}
		ticker := time.NewTicker(searchFlushInterval)
		defer ticker.Stop()
	readLoop:
		for {
			select {
			case <-ctx.Done():
				break readLoop
			case m, ok := <-ch:
				if !ok {
					// Natural completion. The engine's documented
					// contract (see search.Search's doc comment) is
					// "onDone fires exactly once, after the channel
					// closes" — practically simultaneous with this
					// branch — so blocking here for it is safe for any
					// well-behaved engine and is what guarantees the
					// done event never beats the final flush below.
					flush()
					outcome := <-outcomeCh
					_ = scr.PostEvent(&searchResultsEvent{when: time.Now(), generation: generation, done: true, outcome: outcome})
					return
				}
				buf = append(buf, m)
				if len(buf) >= searchFlushBatchSize {
					flush()
				}
			case <-ticker.C:
				flush()
			}
		}
		// Cancelled path (ctx.Done fired): unlike natural completion,
		// don't trust the engine to still deliver onDone promptly — the
		// reviewer has already moved on from this query, and a
		// cancelled-but-slow-to-notice engine (or, in a test, one that
		// deliberately never calls onDone at all) shouldn't hang this
		// goroutine forever. One second is generous for any cooperative
		// engine to notice cancellation and call onDone; past that, post
		// a best-effort done event with a zero Outcome so the UI is
		// never stuck on "searching…" for an abandoned query.
		flush()
		var outcome search.Outcome
		select {
		case outcome = <-outcomeCh:
		case <-time.After(time.Second):
		}
		_ = scr.PostEvent(&searchResultsEvent{when: time.Now(), generation: generation, done: true, outcome: outcome})
	}()
}

// applySearchResults merges one searchResultsEvent into UI state. A
// stale event — produced for a generation the query has since moved past
// — is dropped, the same guard runSearchIfCurrent applies to the
// debounce event.
func (a *App) applySearchResults(ev *searchResultsEvent) {
	if ev.generation != a.search.generation {
		return
	}
	if len(ev.batch) > 0 {
		a.search.results = append(a.search.results, ev.batch...)
		sort.Slice(a.search.results, func(i, j int) bool {
			ri, rj := a.search.results[i], a.search.results[j]
			if ri.Path != rj.Path {
				return ri.Path < rj.Path
			}
			return ri.Line < rj.Line
		})
		if a.search.selected >= len(a.search.results) {
			a.search.selected = len(a.search.results) - 1
		}
		if a.search.selected < 0 {
			a.search.selected = 0
		}
	}
	if ev.done {
		a.search.searching = false
		a.search.matches = ev.outcome.Matches
		a.search.files = ev.outcome.FilesScanned
		a.search.capped = ev.outcome.Capped
		a.search.cancel = nil
	}
}

// handleSearchKey routes keyboard input while the search modal is open.
// Shape mirrors handleFinderKey: text editing for the query field, arrow
// keys for the result list, Enter to open.
func (a *App) handleSearchKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEsc:
		a.closeSearch()
	case tcell.KeyEnter:
		a.openSelectedSearchResult()
	case tcell.KeyUp:
		if a.search.selected > 0 {
			a.search.selected--
			a.ensureSearchSelectedVisible()
		}
	case tcell.KeyDown:
		if a.search.selected < len(a.search.results)-1 {
			a.search.selected++
			a.ensureSearchSelectedVisible()
		}
	case tcell.KeyLeft:
		if a.search.cursor > 0 {
			a.search.cursor--
		}
	case tcell.KeyRight:
		if a.search.cursor < len(a.search.query) {
			a.search.cursor++
		}
	case tcell.KeyHome:
		a.search.cursor = 0
	case tcell.KeyEnd:
		a.search.cursor = len(a.search.query)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if a.search.cursor > 0 {
			a.search.query = append(a.search.query[:a.search.cursor-1], a.search.query[a.search.cursor:]...)
			a.search.cursor--
			a.searchQueryChanged()
		}
	case tcell.KeyDelete:
		if a.search.cursor < len(a.search.query) {
			a.search.query = append(a.search.query[:a.search.cursor], a.search.query[a.search.cursor+1:]...)
			a.searchQueryChanged()
		}
	case tcell.KeyRune:
		r := ev.Rune()
		if r < 0x20 {
			return
		}
		next := make([]rune, 0, len(a.search.query)+1)
		next = append(next, a.search.query[:a.search.cursor]...)
		next = append(next, r)
		next = append(next, a.search.query[a.search.cursor:]...)
		a.search.query = next
		a.search.cursor++
		a.searchQueryChanged()
	}
}

// handleSearchMouse handles mouse input while the modal is open: wheel
// scrolls the result list, hover moves the selection, click opens the
// result under the pointer, and a click outside the modal dismisses it.
func (a *App) handleSearchMouse(x, y int, btn tcell.ButtonMask) {
	mx, my, mw, mh := a.searchModalRect()
	inside := x >= mx && x < mx+mw && y >= my && y < my+mh

	if btn&tcell.WheelUp != 0 {
		a.scrollSearchList(-1)
		return
	}
	if btn&tcell.WheelDown != 0 {
		a.scrollSearchList(1)
		return
	}

	if idx, ok := a.searchRowAt(x, y); ok {
		a.search.selected = idx
	}
	if btn&tcell.Button1 == 0 {
		return
	}
	if !inside {
		a.closeSearch()
		return
	}
	if idx, ok := a.searchRowAt(x, y); ok {
		a.search.selected = idx
		a.openSelectedSearchResult()
	}
}

// searchRowAt maps a screen point to a result index using the draw-time
// rowRects snapshot. Returns ok=false for anything that isn't a drawn
// row (the input field, the footer, a border).
func (a *App) searchRowAt(x, y int) (int, bool) {
	mx, _, mw, _ := a.searchModalRect()
	if x < mx || x >= mx+mw {
		return 0, false
	}
	for _, r := range a.search.rowRects {
		if r.y == y {
			return r.index, true
		}
	}
	return 0, false
}

// scrollSearchList adjusts listScroll by delta rows, clamped so the
// window never scrolls past the last full page — same clamp shape as
// scrollRootPickerList.
func (a *App) scrollSearchList(delta int) {
	max := len(a.search.results) - searchResultsVisible
	if max < 0 {
		max = 0
	}
	a.search.listScroll += delta
	if a.search.listScroll > max {
		a.search.listScroll = max
	}
	if a.search.listScroll < 0 {
		a.search.listScroll = 0
	}
}

// ensureSearchSelectedVisible slides listScroll so the selected row is
// within the visible window — called after arrow-key navigation, which
// (unlike the mouse) can move selected outside whatever's currently on
// screen.
func (a *App) ensureSearchSelectedVisible() {
	if a.search.selected < a.search.listScroll {
		a.search.listScroll = a.search.selected
	}
	if a.search.selected >= a.search.listScroll+searchResultsVisible {
		a.search.listScroll = a.search.selected - searchResultsVisible + 1
	}
}

// openSelectedSearchResult opens the result at search.selected: closes
// the modal, opens (or switches to) the file, and lands the cursor on
// the matched line and column. Silent no-op when the result list is
// empty.
func (a *App) openSelectedSearchResult() {
	if a.search.selected < 0 || a.search.selected >= len(a.search.results) {
		return
	}
	m := a.search.results[a.search.selected]
	a.closeSearch()
	abs := filepath.Join(a.rootDir, m.Path)
	a.openFile(abs)
	if tab := a.activeTabPtr(); tab != nil {
		// Line/Col are 1-based from the engine; Position is 0-based.
		// MoveCursorTo clamps within the buffer, so a match in a file
		// that's shrunk since the search ran lands somewhere sane
		// instead of panicking.
		tab.MoveCursorTo(editor.Position{Line: m.Line - 1, Col: m.Col - 1}, false)
	}
}

// searchModalRect returns the modal's on-screen rectangle: centered,
// anchored in the upper third the way the finder and root picker are, a
// row taller than the finder for the footer line.
func (a *App) searchModalRect() (x, y, w, h int) {
	w = searchModalMaxWidth
	if w > a.width-4 {
		w = a.width - 4
	}
	if w < 30 {
		w = 30
	}
	// 1 border + 1 title + 1 divider + 1 input + N results + 1 footer +
	// 1 border = N+7 rows.
	h = searchResultsVisible + 7
	if h > a.height-2 {
		h = a.height - 2
	}
	x = (a.width - w) / 2
	y = (a.height - h) / 3
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return
}

// drawSearch paints the modal. Layout (relY):
//
//	0     top border
//	1     title — "Find in files    esc"
//	2     divider
//	3     input          [ query…                          ]
//	4..N  result rows: dimmed path, line number, matched text
//	N+1   footer — "N matches in M files" / "capped at 2000" / "searching…"
//	N+2   bottom border
func (a *App) drawSearch() {
	mx, my, mw, mh := a.searchModalRect()
	bg := a.theme.LineHL
	bgStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text)
	borderStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Subtle)
	titleStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Accent).Bold(true)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Muted)

	fillRect(a.screen, mx, my, mw, mh, bgStyle)
	drawBorder(a.screen, mx, my, mw, mh, borderStyle)
	drawHDivider(a.screen, mx, my+2, mw, borderStyle)

	drawAt(a.screen, mx+1, my+1, " Find in files", titleStyle)
	hint := "esc "
	drawAt(a.screen, mx+mw-1-runeLen(hint), my+1, hint, mutedStyle)

	// Input row.
	inputBg := a.theme.BG
	inputStyle := tcell.StyleDefault.Background(inputBg).Foreground(a.theme.Text)
	fieldStart := mx + 3
	fieldEnd := mx + mw - 2
	fieldWidth := fieldEnd - fieldStart
	a.adjustSearchScroll(fieldWidth)
	for cx := fieldStart - 1; cx <= fieldEnd; cx++ {
		a.screen.SetContent(cx, my+3, ' ', nil, inputStyle)
	}
	for i := 0; i < fieldWidth; i++ {
		idx := a.search.scroll + i
		if idx >= len(a.search.query) {
			break
		}
		a.screen.SetContent(fieldStart+i, my+3, a.search.query[idx], nil, inputStyle)
	}
	caret := fieldStart + (a.search.cursor - a.search.scroll)
	if caret >= fieldStart && caret <= fieldEnd {
		a.screen.ShowCursor(caret, my+3)
	}

	// Result rows.
	rowsStart := my + 4
	rowsCap := mh - 6 // borders + title + divider + input + footer
	if rowsCap > searchResultsVisible {
		rowsCap = searchResultsVisible
	}
	a.search.rowRects = a.search.rowRects[:0]
	for i := 0; i < rowsCap; i++ {
		ry := rowsStart + i
		idx := a.search.listScroll + i
		if idx >= len(a.search.results) {
			for cx := mx + 1; cx < mx+mw-1; cx++ {
				a.screen.SetContent(cx, ry, ' ', nil, bgStyle)
			}
			continue
		}
		a.drawSearchRow(mx, ry, mw, a.search.results[idx], idx == a.search.selected, mutedStyle, bg)
		a.search.rowRects = append(a.search.rowRects, searchRowRect{y: ry, index: idx})
	}

	// Footer.
	footerY := my + mh - 2
	footer := a.searchFooterText()
	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, footerY, ' ', nil, bgStyle)
	}
	drawAt(a.screen, mx+2, footerY, footer, mutedStyle)
}

// searchFooterText renders the modal's status line: "searching…" while a
// debounce or engine run is outstanding, "capped at 2000" when the
// engine's match cap was hit, otherwise the plain "N matches in M files"
// count. Order matters — a capped run is also done searching, so
// "searching" is checked first and "capped" takes priority over the
// plain count once a run finishes.
func (a *App) searchFooterText() string {
	if a.search.searching {
		return "searching…"
	}
	if len(a.search.query) == 0 {
		return "Type to search file contents"
	}
	if a.search.capped {
		return "capped at " + itoa(a.search.matches)
	}
	return itoa(a.search.matches) + " matches in " + itoa(a.search.files) + " files"
}

// drawSearchRow paints one result row: the repo-relative path and line
// number dimmed, then the line's text with the matched span picked out
// in theme.FindCurrent — the same accent the finder's own fuzzy-match
// highlighting and the in-file find bar's current-match marker both use,
// so all three of Vincent's "here's what matched" cues read as one
// visual language instead of three competing accents.
func (a *App) drawSearchRow(mx, ry, mw int, m search.Match, selected bool, mutedStyle tcell.Style, modalBG tcell.Color) {
	rowBG := modalBG
	if selected {
		rowBG = a.theme.BG
	}
	rowStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.Text)
	mutedOnRow := mutedStyle.Background(rowBG)
	lineNoStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.LineNumber)
	hitStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.FindCurrent).Bold(true)

	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, ry, ' ', nil, rowStyle)
	}

	col := mx + 2
	maxCol := mx + mw - 2
	// drawClipped (gitpanel.go) takes a width budget and returns how many
	// cells it used, not an absolute column — advance col by that count
	// after each field so this reads as one running cursor laying out
	// path, separator, line number, separator, then the match text.
	budget := func() int {
		if maxCol-col < 0 {
			return 0
		}
		return maxCol - col
	}
	col += drawClipped(a.screen, col, ry, budget(), m.Path, mutedOnRow)
	col += drawClipped(a.screen, col, ry, budget(), ":", mutedOnRow)
	col += drawClipped(a.screen, col, ry, budget(), itoa(m.Line), lineNoStyle)
	col += drawClipped(a.screen, col, ry, budget(), ": ", mutedOnRow)

	textRunes := []rune(m.Text)
	for i, ch := range textRunes {
		if col >= maxCol {
			break
		}
		st := rowStyle
		if i >= m.MatchStart && i < m.MatchStart+m.MatchLen {
			st = hitStyle
		}
		a.screen.SetContent(col, ry, ch, nil, st)
		col++
	}
}

// adjustSearchScroll keeps the input cursor visible by sliding
// search.scroll left or right within the input field. Same shape as
// adjustFinderScroll.
func (a *App) adjustSearchScroll(width int) {
	if width <= 0 {
		a.search.scroll = 0
		return
	}
	if a.search.cursor < a.search.scroll {
		a.search.scroll = a.search.cursor
	}
	if a.search.cursor-a.search.scroll >= width {
		a.search.scroll = a.search.cursor - width + 1
	}
	if a.search.scroll < 0 {
		a.search.scroll = 0
	}
}
