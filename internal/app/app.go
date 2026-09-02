// =============================================================================
// File: internal/app/app.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-29
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Package app is the editor's top-level glue: it owns the tcell screen,
// the file tree, the open tabs, and the event loop. The drawing is split
// into four panels (sidebar / tab bar / editor body / status bar) and the
// mouse dispatcher routes presses, drags, and wheel events to whichever
// panel the cursor is over.
//
// The editor is mouse-first by design — there are no Ctrl-keyed shortcuts
// because they collide with terminal flow control (Ctrl-S/Q) and tmux/zellij
// prefixes. Every action is on an Esc-leader key instead (see leader.go),
// with Esc ? showing the whole table (see cheatsheet.go) and right-click
// carrying the handful of per-surface extras.
package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/clipboard"
	"github.com/chasereyn/vincent/internal/config"
	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/filetree"
	"github.com/chasereyn/vincent/internal/finder"
	"github.com/chasereyn/vincent/internal/icons"
	"github.com/chasereyn/vincent/internal/review"
	"github.com/chasereyn/vincent/internal/theme"
)

// Layout, behavior, and feel constants. Constants instead of config —
// the editor is opinionated by design.
const (
	// defaultSidebarWidth is the file explorer's opening width. Doubled
	// from 30 on 2026-09-02: the owner's first real session said the tree
	// should be about twice as wide, and it is right — a reviewer reads
	// paths, not code, out of that panel, and at 30 cells a nested Go
	// package name was clipped before the filename even started. Vincent
	// gets a monitor of its own, so the editor can afford it.
	defaultSidebarWidth = 60

	// startupSidebarPercent caps that default at a fraction of the window
	// on the first frame. 60 cells of an 80-column terminal leaves 20 for
	// the editor, which is not a code pane, so a narrow terminal gets a
	// proportional tree instead of the flat default. This is a STARTUP
	// clamp only — once the user has dragged the splitter, resizeSidebar's
	// minEditorAfterDrag budget is the only thing that overrides them.
	startupSidebarPercent = 40

	minSidebarWidth    = 18
	minEditorAfterDrag = 40

	// The Changes panel starts a little wider than the tree: its rows carry
	// a filename AND a dimmed parent directory, and the parent is the half
	// that gets clipped first.
	defaultGitPanelWidth = 34
	minGitPanelWidth     = 24
	minWidth             = 50
	minHeight            = 24
	statusFlashFor       = 3 * time.Second
	doubleClickMs        = 500 * time.Millisecond
	// doubleEscMs is how long an Esc stays "armed" for the Esc-<letter>
	// leader keys. The name predates the removal of the Esc-Esc menu
	// double-tap; it is kept so the tests and status-bar code that cite
	// it still read.
	//
	// Raised from spice-edit's 500ms after Esc-q failed to quit for the
	// person this is built for. Half a second is a typist's reflex window,
	// and Vincent is deliberately NOT driven by typing: you are reading, one
	// hand on the mouse, and the second key comes when it comes. A leader
	// that silently expires reads as a broken keybinding, and the status bar
	// now says when it is armed (see drawStatusBar) so the window is
	// visible rather than guessed at.
	doubleEscMs = 1500 * time.Millisecond
	wheelLines  = 3
	wheelCols   = 6 // horizontal step per WheelLeft/WheelRight event

	// modifierStickyWindow is how long a previously-seen Shift modifier
	// state is allowed to persist forward onto the next wheel event.
	// Some terminals (Zellij + macOS Terminal among them) emit the
	// Shift state as a separate ButtonNone+Shift event right before
	// firing the WheelUp/WheelDown without the modifier — so without
	// this carry-forward, shift+wheel reads as plain wheel. 250ms is
	// long enough to bridge the gap and short enough that releasing
	// Shift before scrolling reliably reverts to vertical scroll.
	modifierStickyWindow = 250 * time.Millisecond

	// treeRefreshInterval is how often the background goroutine kicks off
	// a file-tree reload so the sidebar stays in sync with on-disk changes
	// made by other tools (git, mv, another tmux pane, etc.). 10s feels
	// "fresh enough" while costing only a handful of ReadDir syscalls.
	treeRefreshInterval = 10 * time.Second

	// autoScrollTick is how often the auto-scroll goroutine emits a tick
	// while the user is drag-selecting with the cursor parked outside the
	// editor's vertical edges. ~16 ticks/sec feels responsive without
	// overshooting on small files.
	autoScrollTick = 60 * time.Millisecond
)

// autoScrollEvent is the custom tcell event our auto-scroll goroutine
// posts at autoScrollTick intervals while the user is drag-selecting past
// the top or bottom edge of the editor pane.
type autoScrollEvent struct {
	when time.Time
}

// When satisfies the tcell.Event interface.
func (e *autoScrollEvent) When() time.Time { return e.when }

// treeRefreshEvent is the custom tcell event the background tree-refresh
// goroutine posts every treeRefreshInterval. The main loop reacts by
// asking the file tree to re-read every loaded directory.
type treeRefreshEvent struct {
	when time.Time
}

// When satisfies the tcell.Event interface.
func (e *treeRefreshEvent) When() time.Time { return e.when }

// tabRect remembers where each tab was drawn so click handling can hit-test
// against the actual rendered geometry rather than re-deriving it.
type tabRect struct {
	Index    int
	X, Width int
	CloseX   int // Cell column of the × close button.
}

// clickRecord tracks the last mouse-press location and time so we can
// detect double-clicks (and select the word under the cursor).
type clickRecord struct {
	x, y int
	when time.Time
}

// hasTree is the "is there a file explorer at all" predicate. True when
// the file tree was built at startup; false in single-file mode, where we
// deliberately skipped tree construction to avoid indexing the working
// directory. It used to gate the tree rows' visibility in the ≡ menu;
// with the menu gone the tree-dependent leader actions guard themselves
// on a nil tree directly, and this stays as the readable name for that
// condition.
func (a *App) hasTree() bool {
	return a.tree != nil
}

// App is the editor's top-level state holder and event-loop owner.
type App struct {
	screen tcell.Screen
	theme  theme.Theme

	rootDir   string
	tree      *filetree.Tree
	tabs      []*editor.Tab
	activeTab int

	// activeFolder is the directory the editor is currently "working
	// in". It updates whenever the user clicks a folder in the tree,
	// opens a file (parent dir wins), or right-clicks a folder. See
	// setActiveFolder for the single write path so the file tree's
	// matching highlight stays in sync.
	activeFolder string

	width, height int

	// sidebarShown controls whether the file explorer panel is visible.
	// When false the editor and tab bar fill the whole window.
	sidebarShown bool

	// tabBarShown controls whether row 0 draws the full strip of open
	// tabs or just the active tab's name. The row itself never goes away
	// — see tabBarRect/editorRect — only its contents change. Loaded
	// from config.Config.TabBar (default off) by loadUserConfig;
	// toggled by Esc t / menuToggleTabBar.
	tabBarShown bool

	// sidebarWidth is the live width of the file-explorer block (file tree
	// + 1-cell splitter on its right edge), in screen cells. The user can
	// drag the splitter to change it within [minSidebarWidth, width-minEditorAfterDrag].
	sidebarWidth int

	// startupSidebarDone records that clampStartupSidebar has run. The
	// percentage cap is a one-shot on the first sized frame: applying it
	// again on a later resize would quietly undo a splitter drag.
	startupSidebarDone bool

	// The Changes panel down the right-hand side. gitPanelWidth mirrors
	// sidebarWidth (block width, splitter included — here on its LEFT edge).
	// gitSnap is the last `git status` read; the tree's dirty highlight is
	// derived from the same snapshot so the two can never disagree.
	// lastGitPanelRows is the hit-test snapshot recorded during the draw,
	// and gitPanelHover is the screen row under the pointer, or -1.
	gitPanelShown    bool
	gitPanelWidth    int
	gitSnap          gitSnapshot
	gitPanelScroll   int
	gitPanelHover    int
	lastGitPanelRows []gitPanelRowRect

	clipBuf      string
	statusMsg    string
	statusUntil  time.Time
	dragMode     string // "editor" while a drag-select is active.
	lastClick    clickRecord
	lastTabRects []tabRect

	// lastShiftAt is the wall-clock time we last saw any mouse event
	// carrying the Shift modifier. Some terminals (notably Zellij over
	// macOS Terminal) report modifier state in a separate ButtonNone
	// event right before the wheel event, instead of folding the
	// modifier into the wheel event itself. We treat a wheel event as
	// shifted when one of those modifier-state events arrived within
	// modifierStickyWindow. See handleMouse.
	lastShiftAt time.Time

	// cheatsheetOpen is the Esc-? key table. It is the one overlay with
	// no interactive state at all — no hover row, no selection — which is
	// why it needs a single bool where the ≡ menu it replaced needed a
	// bool plus a hovered-row index. See cheatsheet.go.
	cheatsheetOpen bool

	lastEscape time.Time // timestamp of the previous Esc press, for the leader window.

	// Prompt modal — single-line text input with OK / Cancel. Used by
	// Rename and New File. See modals.go for render + event handling.
	promptOpen     bool
	promptTitle    string
	promptHint     string
	promptValue    []rune
	promptCursor   int
	promptScroll   int
	promptCallback func(*App, string)

	// Confirm modal — Yes / No, used by Delete. confirmHover is 0 for No
	// (the safe default) or 1 for Yes.
	confirmOpen     bool
	confirmTitle    string
	confirmMessage  string
	confirmHover    int
	confirmCallback func(*App)

	// confirmInfo flips the confirm modal into a single-button "OK"
	// flavour used for reporting things back to the user — like the
	// full stderr from a failed custom action — that don't need a
	// Yes/No decision. confirmMessageLines, when non-nil, supersedes
	// confirmMessage so the renderer can draw a multi-line body for
	// scp / ssh diagnostics that naturally wrap.
	confirmInfo         bool
	confirmMessageLines []string
	confirmInfoScroll   int

	// The decision modal — a title, a one-line message, and a row of
	// buttons. Used when closing a dirty tab or quitting with unsaved
	// changes (Cancel / Discard / Save) and when a save runs into a file
	// that changed on disk (Cancel / Show diff / Reload / Overwrite).
	// dirtyHover indexes dirtyButtons; index 0 is always the safe option,
	// because that is where focus starts and an accidental Enter has to be
	// harmless. See modals.go for the button machinery.
	dirtyOpen    bool
	dirtyTitle   string
	dirtyMessage string
	dirtyHover   int
	dirtyButtons []dirtyButton

	// Right-click context menu over the file tree.
	contextOpen  bool
	contextX     int
	contextY     int
	contextNode  *filetree.Node
	contextItems []contextItem
	contextHover int

	// Find bar — opened with Esc-f or the "Find in file" menu entry. The
	// bar is a 1-row strip pinned above the status bar; while it's open
	// it owns the keyboard. The active tab carries the query and match
	// list (see editor.Tab.SetFindQuery), so each tab remembers its own
	// search across closes / reopens.
	findOpen   bool
	findValue  []rune
	findCursor int
	findScroll int

	// Auto-scroll while drag-selecting past the editor's top/bottom edge.
	// lastDragX/Y is the most recent mouse position so the auto-scroll
	// tick can extend the selection at the user's column even though the
	// mouse hasn't moved.
	autoScrollStop chan struct{}
	autoScrollDir  int // -1 up, 0 idle, +1 down
	lastDragX      int
	lastDragY      int

	// treeRefreshStop signals the background tree-refresh goroutine to exit.
	treeRefreshStop chan struct{}

	// gitPollBusy is true from the moment a background git refresh is
	// launched until its result is applied. One in flight at a time: a slow
	// repo would otherwise stack up workers, each forking git, which makes
	// the stall it exists to avoid worse. Written only on the main
	// goroutine (startGitPoll and applyGitPoll), so it needs no lock — see
	// gitpoll.go.
	gitPollBusy bool

	// gitBranch is the current branch name for the project root (or a
	// short commit SHA when HEAD is detached). Empty when the root isn't
	// a git repo. Updated on the same 10-second tick as refreshGitStatus.
	gitBranch string

	// finder + finder modal state — project-wide file search (Esc p).
	// The Finder owns the cached index and a
	// background-build goroutine; the rest of these fields are
	// transient UI state for the modal itself.
	finder         *finder.Finder
	finderOpen     bool
	finderQuery    []rune
	finderCursor   int
	finderScroll   int
	finderSelected int
	finderResults  []finder.Result

	// confirmCancelHook runs when the active confirm modal is dismissed
	// without a Yes — i.e. the user picked No, hit Esc, or clicked
	// outside. Set after openConfirm by flows that want to react to the
	// negative answer (today: format-trust deny, format-install
	// decline). closeAllModals clears it so a stale hook can't fire on
	// an unrelated future modal.
	confirmCancelHook func(*App)

	// Review notes — phase 3. reviewBatch is the set of notes waiting to
	// go back to an agent; everything else here is transient UI state for
	// the inline composer, the click snapshots recorded during the draw,
	// and the agent picker. See review.go.
	//
	// composerTab is the tab the open composer belongs to, so switching
	// tabs parks a half-written note instead of letting it eat keystrokes
	// over an unrelated file. composerEdit is the index of the comment
	// being edited, or -1 for a new one. The frozen fields (side, start,
	// end, hunk, snippet) are captured when the composer opens and are
	// never re-derived — see the never-rebase rule in review.go.
	reviewBatch review.Batch

	composerOpen    bool
	composerTab     *editor.Tab
	composerRow     int
	composerFile    string
	composerSide    review.Side
	composerStart   int
	composerEnd     int
	composerHunk    int
	composerSnippet string
	composerKind    review.Kind
	composerValue   []rune
	composerCursor  int
	composerScroll  int
	composerEdit    int

	lastReviewRows []reviewRowRect
	lastMarkerRefs []reviewMarkerRef

	// Agent picker — only opened when more than one agent is running in
	// this herdr workspace. pickerText is the rendered batch, frozen at
	// open so a background refresh can't change what gets sent while the
	// reviewer is deciding.
	pickerOpen    bool
	pickerTargets []review.Target
	pickerText    string
	pickerHover   int

	// signalStop is the channel startSignalWatch registered for Ctrl+C and
	// SIGTERM. Held so a normal exit can unregister it. See shutdown.go.
	signalStop chan os.Signal

	// pasting / pasteBuf carry a bracketed paste while it is arriving.
	// The terminal wraps a paste in EventPaste{Start} … EventPaste{End}
	// and delivers the body as ordinary key events in between, so every
	// keystroke inside that window is DATA, never a command: handleKey
	// appends to pasteBuf and returns without arming the Esc leader or
	// touching the buffer. Without this an Esc byte in pasted text arms
	// the leader and the next 'q' quits mid-paste. The whole buffer is
	// inserted once at End() so an N-line paste is one undo step.
	pasting  bool
	pasteBuf []rune
	// lastFrame is the observable state as of the last painted frame, and
	// frames counts the paints. The event loop compares a pure-motion
	// event's result against lastFrame and skips the repaint when they
	// match — see frame.go, which explains why the baseline is the last
	// frame rather than the state before the event. frames exists for the
	// tests: "did that motion event cost a frame" is otherwise
	// unobservable.
	lastFrame frameKey
	frames    int

	quit bool
}

// initScreenInput turns on the input modes both constructors need. Mouse
// events are the obvious half; bracketed paste is the half that is easy to
// forget, and forgetting it is a data-loss bug rather than a missing
// feature — see the pasting field above. Factored out so a test can hand
// in a recording screen and prove both calls happen.
func initScreenInput(scr tcell.Screen) {
	scr.EnableMouse(tcell.MouseButtonEvents | tcell.MouseDragEvents | tcell.MouseMotionEvents)
	scr.EnablePaste()
}

// New initialises the screen and mouse, builds the file tree at rootDir,
// and returns an App ready to Run.
func New(rootDir string) (*App, error) {
	scr, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}
	if err := scr.Init(); err != nil {
		return nil, err
	}
	initScreenInput(scr)

	th := theme.Default()
	scr.SetStyle(tcell.StyleDefault.Background(th.BG).Foreground(th.Text))
	scr.Clear()

	tree, err := filetree.New(rootDir)
	if err != nil {
		scr.Fini()
		return nil, err
	}

	a := &App{
		screen:        scr,
		theme:         th,
		rootDir:       rootDir,
		tree:          tree,
		sidebarShown:  true,
		sidebarWidth:  defaultSidebarWidth,
		gitPanelWidth: defaultGitPanelWidth,
		gitPanelHover: -1,
		// -1 is "the composer is writing a NEW note". Zero would read as
		// "editing comment 0", which matters the moment any code path
		// consults it without checking composerOpen first.
		composerEdit: -1,
	}
	a.setActiveFolder(tree.Root.Path)
	a.loadUserConfig()
	installReviewLog()
	a.refreshGitStatus()
	a.applyStartupPanelDefaults()
	a.flash("Welcome — click a file to open · Esc ? for the keys")
	a.startTreeRefresh()
	a.startSignalWatch()
	// Kick off the project file index in the background so that by
	// the time the user hits Esc p the modal can
	// open with results already in hand. On a 50k-file repo this
	// takes ~150ms; the user pays it once at startup instead of
	// when they're trying to navigate.
	a.finder = finder.New(rootDir)
	scr2 := a.screen
	a.finder.Rebuild(func() {
		_ = scr2.PostEvent(&finderRebuiltEvent{when: time.Now()})
	})
	return a, nil
}

// NewSingleFile is the lean alternative to New for the "vincent
// somefile.md" invocation: no file tree, no project finder index,
// no background tree-refresh goroutine, sidebar hidden. The user
// asked for one file — we don't pay the cost of walking and watching
// the surrounding directory tree just to render a file they wanted
// to look at in isolation. menuToggleSidebar guards itself on a nil
// tree so Esc f can't try to show a sidebar that doesn't exist.
//
// rootDir is still recorded (set to the file's parent) so file-level
// actions that need a base directory — Save As, New File, the
// relative/absolute path helpers — have somewhere to anchor.
func NewSingleFile(filePath string) (*App, error) {
	scr, err := tcell.NewScreen()
	if err != nil {
		return nil, err
	}
	if err := scr.Init(); err != nil {
		return nil, err
	}
	initScreenInput(scr)

	th := theme.Default()
	scr.SetStyle(tcell.StyleDefault.Background(th.BG).Foreground(th.Text))
	scr.Clear()

	rootDir := filepath.Dir(filePath)
	if rootDir == "" {
		rootDir = "."
	}

	a := &App{
		screen:        scr,
		theme:         th,
		rootDir:       rootDir,
		tree:          nil,
		sidebarShown:  false,
		sidebarWidth:  defaultSidebarWidth,
		gitPanelWidth: defaultGitPanelWidth,
		gitPanelHover: -1,
		// -1 is "the composer is writing a NEW note". Zero would read as
		// "editing comment 0", which matters the moment any code path
		// consults it without checking composerOpen first.
		composerEdit: -1,
	}
	a.setActiveFolder(rootDir)
	a.loadUserConfig()
	installReviewLog()
	// openFile loads the file's git gutter markers itself (a file-scoped
	// `git diff`), so single-file mode shows change bars on open without
	// the whole-repo status or tree walk that New performs.
	a.openFile(filePath)
	// Single-file mode skips the tree and the finder, but NOT this: an
	// orphaned process is just as unkillable either way.
	a.startSignalWatch()
	return a, nil
}

// loadUserConfig reads ~/.config/vincent/config.json (if any),
// resolves the Nerd Fonts auto/on/off mode to a concrete bool via
// icons.Resolve, and stamps the result onto the file tree so the
// next render starts drawing glyphs (or doesn't). A malformed
// config flashes a status message but never blocks startup — the
// editor falls back to Defaults() and keeps going.
func (a *App) loadUserConfig() {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		a.flash("config: " + err.Error())
	}
	if a.tree != nil {
		a.tree.IconsEnabled = icons.Resolve(cfg.Icons)
	}
	a.tabBarShown = cfg.TabBar
}

// refreshTree calls tree.Refresh when the file tree exists, and is a
// no-op in single-file mode. File operations (create / rename / delete)
// call this after touching the disk so callers don't have to nil-check
// every site. The git-status and finder refreshes that usually
// accompany it already guard themselves internally.
func (a *App) refreshTree() {
	if a.tree == nil {
		return
	}
	a.tree.Refresh()
}

// refreshGitStatus re-runs `git status --porcelain` against the project
// root and stamps the resulting dirty-paths sets onto the file tree, so
// changed files render in the Modified color on the next draw. It's
// cheap (a couple of forks) but not free — we only call it from the
// 10-second tree-refresh tick and right after our own file operations,
// not on every keystroke. A non-git project leaves the tree's dirty
// maps empty, which the renderer treats as "everything clean".
func (a *App) refreshGitStatus() {
	if a.tree == nil {
		// Single-file mode has no tree to stamp dirty-path sets onto,
		// but the open tab can still show per-line gutter markers —
		// those come from a single `git diff` on the file itself and
		// don't need the (deliberately skipped) directory walk. Skip
		// the whole-repo `git status` and just refresh the gutter.
		a.refreshGitLineChanges()
		return
	}
	// ONE `git status` run feeds both consumers. The tree wants a
	// path -> kind map and the panel wants an ordered list, which is
	// exactly the difference that tempts you into a second run and a
	// second parser — and then they drift, and a file is orange in the
	// tree but missing from the panel.
	a.applyGitSnapshot(loadGitSnapshot(a.rootDir))
	a.refreshGitLineChanges()
}

// applyGitSnapshot stamps one `git status` read onto the Changes panel, the
// tree's dirty-path maps, and the branch label.
//
// Split out of refreshGitStatus so the background poller can read a snapshot
// on its own goroutine and hand it here without a second copy of the
// stamping rules — which is the same "one parse, two consumers" argument
// that gitentries.go makes, one level up. Every line of this writes UI
// state, so it must only ever run on the main goroutine.
func (a *App) applyGitSnapshot(snap gitSnapshot) {
	if a.tree == nil {
		return
	}
	a.refreshGitPanel(snap)
	if !snap.IsRepo {
		a.tree.DirtyFiles = nil
		a.tree.DirtyFolders = nil
		a.gitBranch = ""
		return
	}
	dirtyFiles := rebaseGitPaths(snap.DirtyFiles(), a.tree.Root.Path)
	a.tree.DirtyFiles = dirtyFiles
	a.tree.DirtyFolders = dirtyFolderSet(dirtyFiles, a.tree.Root.Path)
	a.gitBranch = snap.Branch
}

// refreshGitLineChanges refreshes gutter markers for every open text tab.
func (a *App) refreshGitLineChanges() {
	for _, tab := range a.tabs {
		if tab == nil || tab.Path == "" || tab.IsImage() {
			continue
		}
		tab.GitLines = loadGitLineChanges(a.rootDir, tab.Path)
	}
}

// startTreeRefresh launches a goroutine that posts a treeRefreshEvent every
// treeRefreshInterval. The main event loop reacts by calling tree.Refresh,
// which keeps the sidebar in sync with on-disk changes from outside the
// editor (git, mv, another tmux pane, etc.).
func (a *App) startTreeRefresh() {
	a.treeRefreshStop = make(chan struct{})
	stop := a.treeRefreshStop
	scr := a.screen
	go func() {
		ticker := time.NewTicker(treeRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case t := <-ticker.C:
				_ = scr.PostEvent(&treeRefreshEvent{when: t})
			}
		}
	}()
}

// stopTreeRefresh signals the background tree-refresh goroutine to exit.
// Safe to call multiple times.
func (a *App) stopTreeRefresh() {
	if a.treeRefreshStop != nil {
		close(a.treeRefreshStop)
		a.treeRefreshStop = nil
	}
}

// Close releases the terminal back to the user. Always call this before exit.
func (a *App) Close() {
	a.stopSignalWatch()
	a.stopTreeRefresh()
	a.stopAutoScroll()
	if a.screen != nil {
		a.screen.Fini()
	}
}

// Run is the editor's main event loop. It blocks on PollEvent, dispatches
// each event, redraws when the screen would actually differ, and exits when
// a.quit is set.
//
// Two things here are load-bearing and both are about mouse motion; see
// frame.go for the measurement behind them.
//
//   - handleEventForFrame answers "does this event change what the user
//     sees". A pure-motion event that changed nothing observable skips the
//     repaint entirely. Everything else always repaints.
//   - The inner drain loop empties whatever tcell has already queued before
//     painting, so a burst of motion reports (the pointer crossing forty
//     cells in one 16 ms window) costs one frame rather than forty. Events
//     are still handled individually and in arrival order, so a key or a
//     resize buried in the burst behaves exactly as it would alone — it just
//     shares the frame with its neighbours.
func (a *App) Run() error {
	a.width, a.height = a.screen.Size()
	a.paint()

	for !a.quit {
		ev := a.screen.PollEvent()
		if ev == nil {
			break
		}
		dirty := a.handleEventForFrame(ev)
		for !a.quit && a.screen.HasPendingEvent() {
			next := a.screen.PollEvent()
			if next == nil {
				return nil
			}
			if a.handleEventForFrame(next) {
				dirty = true
			}
		}
		if dirty {
			a.paint()
		}
	}
	return nil
}

// handleEvent routes a tcell event to its specific handler.
func (a *App) handleEvent(ev tcell.Event) {
	switch e := ev.(type) {
	case *tcell.EventResize:
		a.width, a.height = a.screen.Size()
		// tcell delivers a resize before any other event, so this is the
		// first frame that knows how wide the terminal is — and therefore
		// the only place the startup percentage cap can be applied. It is
		// a one-shot; see clampStartupSidebar.
		a.clampStartupSidebar()
		// Re-clamp both side panels before anything reads a rect: at the
		// old widths a narrowed window leaves the editor at zero or
		// negative width.
		a.reflowPanels()
		a.screen.Sync()
	case *tcell.EventKey:
		a.handleKey(e)
	case *tcell.EventMouse:
		a.handleMouse(e)
	case *tcell.EventPaste:
		a.handlePaste(e)
	case *autoScrollEvent:
		a.handleAutoScroll()
	case *quitEvent:
		// Posted by the signal watcher. Setting the flag here rather than
		// from its goroutine keeps every write to UI state on the main
		// thread.
		a.quit = true
	case *treeRefreshEvent:
		a.refreshTreeNow()
	case *gitPollEvent:
		// A background refresh came back. Everything in it was read on a
		// worker goroutine; this is where it becomes UI state. See
		// gitpoll.go.
		a.applyGitPoll(e.res)
	case *finderRebuiltEvent:
		// The background indexer just finished. Re-run the visible
		// query so "Indexing…" gives way to real results without
		// the user having to type or wait for the next keystroke.
		if a.finderOpen {
			a.refreshFinderResults()
		}
	}
}

// refreshTreeNow is what the 10s timer fires: rescan the file tree
// (preserving expansion state) and kick off a background git refresh.
//
// The tree rescan stays on this goroutine — it is ReadDir over the loaded
// directories, and the identity-preserving reload has to be able to mutate
// the live *Node tree. Everything that shells out to git, plus the disk
// stats behind the open-tab reconcile, is now the poller's job: those were
// four-plus forks per tick on the UI thread, and a fork that blocks blocks
// the event loop, which is the pointer freezing mid-review while an agent
// hammers the repo. The results come back as a gitPollEvent and are applied
// by applyGitPoll. See gitpoll.go.
func (a *App) refreshTreeNow() {
	a.refreshTree()
	a.startGitPoll()
	a.invalidateFinder()
}

// reconcileOpenTabsWithDisk applies one background poll to the open tabs.
// For every tab with a real path it compares the polled on-disk mtime to
// what the tab last knew, and reacts:
//
//   - File missing  → flash once, mark the tab dirty so the user knows.
//   - Disk newer, tab clean → reload the buffer, silently.
//   - Disk newer, tab dirty, same bytes → just take the new mtime.
//   - Disk newer, tab dirty, different bytes → record a conflict, flash
//     once, and leave the buffer alone. Save then refuses until the user
//     picks Overwrite or Reload.
//
// Untitled tabs (Path == "") are skipped because there's no disk file to
// reconcile against.
//
// Two things here are load-bearing and easy to undo by accident:
//
// A conflicted tab keeps its OLD Mtime. The flag, not the timestamp, is
// what suppresses the re-flash — which is the whole bug this replaced:
// advancing Mtime to stop the warning repeating also erased the only
// record that a conflict existed, and the next Save overwrote the agent
// with no prompt. Leaving Mtime behind has a second payoff: if the user
// undoes their way back to a clean buffer the conflict clears, and the
// next tick sees the disk as still newer and reloads it.
//
// The byte comparison is not an optimisation. Agents and formatters bump
// mtime without changing content constantly, and a warning that fires on
// those is one the user stops reading.
// Tabs the poll has no entry for are also skipped: those were tabs the poll has no entry for: those were
// opened after the poll started, and the next tick will cover them. That is
// also what drops results for a tab closed while the poll was in flight —
// the loop walks the live tab list, not the poll's.
//
// The stat itself happened on the poller's goroutine (gitpoll.go); the
// decisions and every write below happen here, on the main goroutine.
func (a *App) reconcileOpenTabsWithDisk(files map[string]gitPollFile) {
	for _, tab := range a.tabs {
		if tab.Path == "" {
			continue
		}
		polled, ok := files[tab.Path]
		if !ok {
			continue
		}
		if tab.IsDiff() {
			// Diff tabs reconcile differently: there is no buffer to
			// reload, nothing can be dirty, and a file disappearing is a
			// legitimate diff (an all-deletions one) rather than the
			// warning case it is for an open file.
			a.reconcileDiffTab(tab, polled)
			continue
		}
		if tab.IsImage() {
			continue
		}
		// Gutter markers first: they are a straight overwrite and do not
		// depend on any of the mtime reasoning below.
		tab.GitLines = polled.lines
		if polled.missing {
			if !tab.DiskGone {
				tab.DiskGone = true
				tab.Dirty = true
				a.flash(fmt.Sprintf("%s deleted on disk", filepath.Base(tab.Path)))
			}
			continue
		}
		if polled.statErr {
			// Permission denied or some other transient stat error — leave
			// the tab as-is rather than spamming the user with a flash.
			continue
		}
		if tab.DiskGone {
			// File reappeared. Force the mtime check below to fire so we
			// either reload or warn about a dirty conflict.
			tab.DiskGone = false
			tab.Mtime = time.Time{}
		}
		if !polled.mtime.After(tab.Mtime) {
			continue // unchanged on disk.
		}
		if tab.Dirty {
			if tab.Conflict {
				// Already recorded and already announced. Skipping here
				// is also what keeps the file read below off the tick for
				// as long as the conflict stands.
				continue
			}
			data, readErr := os.ReadFile(tab.Path)
			if readErr == nil && tab.DiskUnchangedSince(data) {
				// Same bytes as when we loaded it: something rewrote the
				// file without changing it. Not a conflict.
				tab.Mtime = polled.mtime
				continue
			}
			tab.Conflict = true
			a.flash(fmt.Sprintf("%s changed on disk — save will ask before overwriting",
				filepath.Base(tab.Path)))
			continue
		}
		if err := tab.Reload(); err != nil {
			a.flash(fmt.Sprintf("%s reload failed: %v", filepath.Base(tab.Path), err))
		}
		// No flash on success. A clean tab reloading when the agent writes
		// is the normal case in this workflow, and a flash per write is
		// constant noise during exactly the stretch you are reading. Same
		// call diff tabs already made — see reconcileDiffTab.
	}
}

// -----------------------------------------------------------------------------
// Layout helpers
// -----------------------------------------------------------------------------

// sidebarW is the effective width of the sidebar block (file tree +
// splitter): zero when hidden, a.sidebarWidth otherwise. Every layout
// helper and click router goes through this so toggling/resizing the
// panel reshapes the entire UI in one place.
func (a *App) sidebarW() int {
	if !a.sidebarShown {
		return 0
	}
	return a.sidebarWidth
}

// sidebarRect returns the file tree's render rectangle (one column
// narrower than the sidebar block — the rightmost column belongs to the
// resize splitter). Zero width when the sidebar is hidden.
func (a *App) sidebarRect() (x, y, w, h int) {
	sw := a.sidebarW()
	if sw <= 0 {
		return 0, 0, 0, 0
	}
	return 0, 0, sw - 1, a.height - 1
}

// splitterX returns the x coordinate of the resize splitter column, or -1
// when the sidebar is hidden (no splitter to draw or click).
func (a *App) splitterX() int {
	if !a.sidebarShown {
		return -1
	}
	return a.sidebarWidth - 1
}

// clampStartupSidebar trims the opening sidebar width to at most
// startupSidebarPercent of the window, once, on the first frame that has a
// real window size to measure against.
//
// It has to be separate from resizeSidebar because the two answer different
// questions. resizeSidebar enforces a floor for the editor (keep 40
// columns) and is the right rule for a deliberate splitter drag — the user
// asked for that width. This one enforces a proportion, and only for a
// width nobody asked for: defaultSidebarWidth is tuned for the wide monitor
// Vincent lives on, and on an 80-column terminal it would leave 20 cells of
// editor, which resizeSidebar alone would happily allow (80 - 40 = 40, so
// 60 would clamp to 40, still half the window).
//
// Once only, and gated on a.width, because a resize is where it runs from:
// re-clamping on every resize would silently undo a splitter drag the next
// time the terminal changed size.
func (a *App) clampStartupSidebar() {
	if a.startupSidebarDone || a.width <= 0 {
		return
	}
	a.startupSidebarDone = true
	max := a.width * startupSidebarPercent / 100
	if max > 0 && a.sidebarWidth > max {
		a.sidebarWidth = max
	}
}

// resizeSidebar applies the user's desired sidebar width while clamping it
// to a sensible range — the file tree stays wide enough to read names and
// the editor keeps at least minEditorAfterDrag columns. Tiny windows that
// can't satisfy both fall back to the minimum and let the editor shrink.
func (a *App) resizeSidebar(target int) {
	if target < minSidebarWidth {
		target = minSidebarWidth
	}
	max := a.width - minEditorAfterDrag - a.gitPanelW()
	if max < minSidebarWidth {
		max = minSidebarWidth
	}
	if target > max {
		target = max
	}
	a.sidebarWidth = target
}

// tabBarRect returns the tab bar's screen rectangle (one row tall).
func (a *App) tabBarRect() (x, y, w, h int) {
	sw := a.sidebarW()
	return sw, 0, a.width - sw - a.gitPanelW(), 1
}

// editorRect returns the editor body's screen rectangle (everything to the
// right of the sidebar, between the tab bar and the status bar). When the
// find bar is open, one row is taken out of the bottom — the bar is
// pinned directly above the status bar.
func (a *App) editorRect() (x, y, w, h int) {
	sw := a.sidebarW()
	h = a.height - 2
	if a.findOpen {
		h -= findBarHeight
	}
	return sw, 1, a.width - sw - a.gitPanelW(), h
}

// statusRect returns the status bar's screen rectangle (full-width bottom row).
func (a *App) statusRect() (x, y, w, h int) {
	return 0, a.height - 1, a.width, 1
}

// editorSize returns just the (width, height) of the editor body. Used by
// keyboard handlers that need to compute page-up / page-down deltas.
func (a *App) editorSize() (int, int) {
	_, _, w, h := a.editorRect()
	return w, h
}

// -----------------------------------------------------------------------------
// Keyboard
// -----------------------------------------------------------------------------

// handleKey responds to keyboard events. There are intentionally no Ctrl-
// based shortcuts, so the editor never fights the terminal (Ctrl-S/Q flow
// control) or a tmux/zellij prefix. The only "command" key is Esc, which
// arms the leader for the hotkey table in leader.go (Esc s = Save, Esc u
// = Undo, Esc ? = the whole table).
func (a *App) handleKey(ev *tcell.EventKey) {
	// A bracketed paste is in flight: every key between EventPaste{Start}
	// and EventPaste{End} is pasted text, not a command. This branch is
	// first for exactly that reason — an Esc byte in the middle of a paste
	// must not arm the leader, because the next character is very likely a
	// letter, and 'q' would quit mid-paste.
	if a.pasting {
		a.appendPasteKey(ev)
		return
	}
	// Secondary modals own the keyboard while they're up. Each handler
	// understands Esc (cancel), Enter (submit / activate), and the keys
	// relevant to its layout (text editing for the prompt, arrow keys for
	// the context menu, etc.).
	// The cheatsheet is first among the overlays: it is the one that Esc
	// itself has to reach, and it must not let a keystroke through to the
	// buffer it is covering.
	if a.cheatsheetOpen {
		a.handleCheatsheetKey(ev)
		return
	}
	if a.promptOpen {
		a.handlePromptKey(ev)
		return
	}
	if a.confirmOpen {
		a.handleConfirmKey(ev)
		return
	}
	if a.dirtyOpen {
		a.handleDirtyKey(ev)
		return
	}
	if a.contextOpen {
		a.handleContextKey(ev)
		return
	}
	if a.findOpen {
		a.handleFindKey(ev)
		return
	}
	if a.finderOpen {
		a.handleFinderKey(ev)
		return
	}
	if a.pickerOpen {
		a.handlePickerKey(ev)
		return
	}
	// The review composer is not a modal — it lives inline in the diff —
	// but while it is on screen it owns the keyboard, including Esc, which
	// there means "cancel this note" rather than "arm the leader". Routed
	// ahead of the Esc handling below for exactly that reason.
	if a.composerActive() {
		a.handleComposerKey(ev)
		return
	}

	if ev.Key() == tcell.KeyEsc {
		// Esc is the editor's only command key. Behavior:
		//   • leader armed → cancel it (a second Esc is a "never mind")
		//   • otherwise  → arm the leader table (see below)
		// A lone Esc that isn't followed by a leader binding within the
		// window is intentionally a no-op so the key still feels harmless
		// to mash. The cheatsheet is routed above, so an Esc while it is
		// up closes it rather than arming the leader underneath it.
		if a.leaderArmed() {
			a.lastEscape = time.Time{}
			return
		}
		a.lastEscape = time.Now()
		return
	}
	// Esc-leader hotkey: if Esc was pressed within doubleEscMs and this
	// key is bound in the leader table, fire the action and consume the
	// keystroke. Unbound keys fall through to normal handling so a stray
	// Esc doesn't swallow the next character the user types.
	if !a.lastEscape.IsZero() && time.Since(a.lastEscape) < doubleEscMs {
		if ev.Key() == tcell.KeyRune {
			if action := leaderActionFor(ev.Rune()); action != nil {
				a.lastEscape = time.Time{}
				action(a)
				return
			}
		} else if action := leaderActionForKey(ev.Key()); action != nil {
			// Named-key leader bindings — Esc-Enter to send the review.
			// Same fall-through contract as the rune table: an unbound
			// key still reaches normal handling.
			a.lastEscape = time.Time{}
			action(a)
			return
		}
	}
	// Any other key cancels a pending Esc so a stale half-tap doesn't
	// surprise the user later.
	a.lastEscape = time.Time{}

	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	// Image-preview tabs are read-only — no cursor, no editing, no
	// caret movement. Drop every key here so the user can mash arrow
	// keys without anything mysterious happening behind the splash.
	if tab.IsImage() {
		return
	}
	extend := ev.Modifiers()&tcell.ModShift != 0

	switch ev.Key() {
	case tcell.KeyUp:
		tab.MoveCursor(-1, 0, extend)
	case tcell.KeyDown:
		tab.MoveCursor(1, 0, extend)
	case tcell.KeyLeft:
		tab.MoveCursor(0, -1, extend)
	case tcell.KeyRight:
		tab.MoveCursor(0, 1, extend)
	case tcell.KeyHome:
		tab.MoveLineHome(extend)
	case tcell.KeyEnd:
		tab.MoveLineEnd(extend)
	case tcell.KeyPgUp:
		_, h := a.editorSize()
		tab.MoveCursor(-h, 0, extend)
	case tcell.KeyPgDn:
		_, h := a.editorSize()
		tab.MoveCursor(h, 0, extend)
	case tcell.KeyEnter, tcell.KeyBackspace, tcell.KeyBackspace2,
		tcell.KeyDelete, tcell.KeyTab, tcell.KeyRune:
		// Everything above this point moves the caret; everything below
		// changes the buffer. A read-only tab (a diff) takes the movement
		// keys and drops the rest, so arrows and PgUp/PgDn navigate a diff
		// exactly like they navigate a file.
		if tab.ReadOnly() {
			return
		}
		a.applyEditKey(tab, ev)
	}
}

// applyEditKey performs the buffer mutation for an editing keystroke. Split
// out of handleKey so the read-only guard above has a single thing to gate
// rather than a run of cases each needing its own check.
func (a *App) applyEditKey(tab *editor.Tab, ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEnter:
		tab.InsertString("\n")
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		tab.Backspace()
	case tcell.KeyDelete:
		tab.Delete()
	case tcell.KeyTab:
		tab.InsertString(tab.IndentUnit)
	case tcell.KeyRune:
		tab.InsertRune(ev.Rune())
	}
}

// handlePaste consumes a bracketed paste. Start() opens the window and
// resets the accumulator; End() closes it and delivers the whole payload
// in a single InsertString, which is what makes an N-line paste one undo
// step instead of N, and what keeps a literal tab in the pasted text a tab
// rather than the buffer's IndentUnit.
//
// A paste is only ever accepted by an editable text buffer. Any overlay
// that owns the keyboard (the cheatsheet, a modal, the find bar, the finder)
// and any read-only tab discard it with a flash instead — delivering it
// would either leak the text into a surface that cannot hold it or, on a
// diff tab, write diff text over the user's source.
func (a *App) handlePaste(ev *tcell.EventPaste) {
	if ev.Start() {
		a.pasting = true
		a.pasteBuf = a.pasteBuf[:0]
		return
	}
	if !a.pasting {
		return // End with no Start — nothing was accumulated.
	}
	a.pasting = false
	text := string(a.pasteBuf)
	a.pasteBuf = a.pasteBuf[:0]
	if text == "" {
		return
	}
	if a.anyModalOpen() {
		a.flash("Paste ignored — close the open panel first")
		return
	}
	tab := a.activeTabPtr()
	if tab == nil {
		a.flash("Paste ignored — no file open")
		return
	}
	if tab.ReadOnly() {
		a.flash(fmt.Sprintf("Paste ignored — %s is read-only", tab.DisplayName()))
		return
	}
	tab.InsertString(text)
}

// appendPasteKey folds one key event into the in-flight paste buffer.
// Runes go in as themselves, Enter as "\n", Tab as "\t"; everything else
// — arrows, function keys, and a bare Esc — is dropped. Dropping Esc is
// the point of the whole mechanism: it is data here, not the leader key,
// and a terminal in bracketed-paste mode filters real escape sequences out
// of the payload anyway, so nothing legible is lost.
func (a *App) appendPasteKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyRune:
		a.pasteBuf = append(a.pasteBuf, ev.Rune())
	case tcell.KeyEnter:
		a.pasteBuf = append(a.pasteBuf, '\n')
	case tcell.KeyTab:
		a.pasteBuf = append(a.pasteBuf, '\t')
	}
}

// -----------------------------------------------------------------------------
// Mouse
// -----------------------------------------------------------------------------

// handleMouse routes a mouse event to whichever panel the cursor is over,
// tracking drag state so a click-drag inside the editor extends the
// selection. An open overlay absorbs every mouse event first — the key
// cheatsheet dismisses on any press, wherever it landed.
func (a *App) handleMouse(ev *tcell.EventMouse) {
	x, y := ev.Position()
	btn := ev.Buttons()

	// Remember when we last saw Shift held down on ANY mouse event.
	// Zellij + macOS Terminal split shift+wheel into two events: a
	// ButtonNone+Shift "modifier state" event, then a WheelDown/Up
	// with no modifier. We bridge them via modifierStickyWindow below.
	if ev.Modifiers()&tcell.ModShift != 0 {
		a.lastShiftAt = time.Now()
	}

	// Secondary modals absorb all mouse input. The order here matches
	// keyboard routing so behavior stays predictable.
	if a.promptOpen {
		a.handlePromptMouse(x, y, btn)
		return
	}
	if a.confirmOpen {
		a.handleConfirmMouse(x, y, btn)
		return
	}
	if a.dirtyOpen {
		a.handleDirtyMouse(x, y, btn)
		return
	}
	if a.contextOpen {
		a.handleContextMouse(x, y, btn)
		return
	}
	if a.finderOpen {
		a.handleFinderMouse(x, y, btn)
		return
	}
	if a.pickerOpen {
		a.handlePickerMouse(x, y, btn)
		return
	}

	if a.cheatsheetOpen {
		a.handleCheatsheetMouse(x, y, btn)
		return
	}

	// File-tree row hover. Same reasoning as the Changes panel just below:
	// updated on every mouse event, not just motion, since terminals emit
	// no "pointer left the window" event to clear a stale highlight with.
	if a.sidebarShown {
		a.updateTreeHover(x, y)
	}

	// Changes-panel hover. Updated on every mouse event rather than only on
	// motion, because terminals emit no "pointer left the window" event —
	// tying it to motion alone leaves a row lit after the pointer moves to
	// another pane.
	if a.gitPanelShown {
		a.updateGitPanelHover(x, y)
	}

	// Right-click handling. Over a file-tree row, a diff, or a text tab it
	// opens that surface's small context menu; on nothing in particular it
	// falls through to the key cheatsheet, which is the mouse-only path to
	// "what can I even do here" now that the ≡ button is gone. Note: macOS
	// Terminal + tmux often swallows Button3, which is why every
	// right-click action also has a leader key or appears in the
	// cheatsheet's key table.
	if btn&tcell.Button3 != 0 {
		if a.tryTreeContextClick(x, y) {
			return
		}
		if a.tryDiffContextClick(x, y) || a.tryEditorContextClick(x, y) {
			return
		}
		a.openCheatsheet()
		return
	}

	// Wheel events take priority — they fire even with no button held.
	// Shift+wheel rotates the vertical wheel into horizontal scrolling
	// (the VS Code convention). Most terminals never emit native
	// WheelLeft/WheelRight, so this is the path that actually fires in
	// practice; the dedicated horizontal-wheel branch below is a bonus
	// for terminals that do.
	//
	// We accept "shift was just seen" within modifierStickyWindow as
	// equivalent to shift-on-this-event, because Zellij and friends
	// strip the modifier from the actual wheel event.
	shift := ev.Modifiers()&tcell.ModShift != 0 ||
		(!a.lastShiftAt.IsZero() && time.Since(a.lastShiftAt) < modifierStickyWindow)
	if btn&tcell.WheelUp != 0 {
		if shift {
			a.scrollAtH(x, y, -wheelCols)
		} else {
			a.scrollAt(x, y, -wheelLines)
		}
		return
	}
	if btn&tcell.WheelDown != 0 {
		if shift {
			a.scrollAtH(x, y, wheelCols)
		} else {
			a.scrollAt(x, y, wheelLines)
		}
		return
	}
	if btn&tcell.WheelLeft != 0 {
		a.scrollAtH(x, y, -wheelCols)
		return
	}
	if btn&tcell.WheelRight != 0 {
		a.scrollAtH(x, y, wheelCols)
		return
	}

	leftDown := btn&tcell.Button1 != 0

	// Drag continuation: while we're mid-drag in the editor, every event
	// with the button held extends the selection — even if the cursor has
	// wandered out of the editor pane.
	if leftDown && a.dragMode == "editor" {
		a.editorDrag(x, y)
		return
	}

	// Sidebar resize drag: keep the splitter glued to the mouse x so the
	// panel reshapes live as the user drags.
	if leftDown && a.dragMode == "sidebar" {
		a.resizeSidebar(x + 1)
		return
	}

	// Changes-panel resize drag. The panel grows leftwards, so its width is
	// measured from the right edge rather than from zero.
	if leftDown && a.dragMode == "gitpanel" {
		a.resizeGitPanel(a.width - x)
		return
	}

	// Initial press dispatch.
	if leftDown && a.dragMode == "" {
		sw := a.sidebarW()
		splitX := a.splitterX()
		gitSplitX := a.gitSplitterX()
		switch {
		case splitX >= 0 && x == splitX:
			a.dragMode = "sidebar"
		case gitSplitX >= 0 && x == gitSplitX:
			a.dragMode = "gitpanel"
		// The Changes panel is tested before the tab bar and the editor so
		// its top row belongs to the panel header, not to the tab strip
		// that ends to its left.
		case gitSplitX >= 0 && x > gitSplitX && y < a.height-1:
			a.gitPanelClick(x, y)
		case sw > 0 && x < splitX:
			a.sidebarClick(x, y)
		case y == 0:
			a.tabBarClick(x, y)
		case y > 0 && y < a.height-1:
			a.editorPress(x, y)
			a.dragMode = "editor"
		}
		return
	}

	// Button released — exit any drag mode we were in.
	a.dragMode = ""
	a.stopAutoScroll()
}

// scrollAt scrolls whichever panel the (x, y) cursor is over.
func (a *App) scrollAt(x, y, delta int) {
	if gs := a.gitSplitterX(); gs >= 0 && x >= gs {
		a.scrollGitPanel(delta)
		return
	}
	if sw := a.sidebarW(); sw > 0 && x < sw {
		a.tree.Scroll(delta)
		return
	}
	if y > 0 && y < a.height-1 {
		if t := a.activeTabPtr(); t != nil {
			t.Scroll(delta)
		}
	}
}

// scrollAtH scrolls the panel under (x, y) horizontally by delta cells.
// The file tree has no useful horizontal axis (each row is a single label),
// so we only honor horizontal wheel events when they fall inside the
// editor pane.
func (a *App) scrollAtH(x, y, delta int) {
	if gs := a.gitSplitterX(); gs >= 0 && x >= gs {
		return
	}
	if sw := a.sidebarW(); sw > 0 && x < sw {
		return
	}
	if y > 0 && y < a.height-1 {
		if t := a.activeTabPtr(); t != nil {
			t.ScrollH(delta)
		}
	}
}

// updateTreeHover keeps the file tree's row highlight in sync with the
// mouse, mirroring updateGitPanelHover: recorded on every mouse event that
// lands inside the tree's rectangle, cleared for one that doesn't. sw<=0
// (sidebar hidden or too narrow) and any (x, y) outside sidebarRect both
// route through the same ClearHover call.
func (a *App) updateTreeHover(x, y int) {
	if a.tree == nil {
		return
	}
	sx, sy, sw, sh := a.sidebarRect()
	if sw <= 0 || x < sx || x >= sx+sw || y < sy || y >= sy+sh {
		a.tree.ClearHover()
		return
	}
	a.tree.SetHover(y - sy)
}

// tryTreeContextClick opens the right-click context menu when (x, y) lands
// on a tree row. Returns true if it consumed the event so the caller knows
// not to fall back to the key cheatsheet. Right-clicking a node also counts
// as "I'm working here" — the active folder updates even after the context
// menu closes.
func (a *App) tryTreeContextClick(x, y int) bool {
	sw := a.sidebarW()
	if sw <= 0 {
		return false
	}
	splitX := a.splitterX()
	if x >= splitX {
		return false
	}
	sx, sy, _, _ := a.sidebarRect()
	n, ok := a.tree.HitTest(x-sx, y-sy)
	if !ok {
		return false
	}
	if n.IsDir {
		a.setActiveFolder(n.Path)
	} else {
		a.setActiveFolder(filepath.Dir(n.Path))
	}
	a.openTreeContext(n, x, y)
	return true
}

// tryDiffContextClick opens the diff view's right-click menu when the
// press landed on a diff row, and reports whether it did.
//
// Gated on the click being inside the editor body: the tab bar, the status
// bar, and either side panel all have their own right-click behaviour, and
// a review menu appearing over the file tree would be a surprise.
func (a *App) tryDiffContextClick(x, y int) bool {
	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() {
		return false
	}
	ex, ey, ew, eh := a.editorRect()
	if x < ex || x >= ex+ew || y < ey || y >= ey+eh {
		return false
	}
	return a.openDiffContext(tab, x, y)
}

// sidebarClick toggles a directory or opens a file when the user clicks a
// row in the file tree. Either action also updates the editor's "active
// folder" so the next New File from the main menu defaults to wherever
// the user is currently focused. Clicking the project-root row only
// resets the active folder — it never toggles the root's expansion
// since the root is always shown and there's no useful "collapsed
// root" state.
func (a *App) sidebarClick(x, y int) {
	sx, sy, _, _ := a.sidebarRect()
	n, ok := a.tree.HitTest(x-sx, y-sy)
	if !ok {
		return
	}
	if n == a.tree.Root {
		a.setActiveFolder(a.rootDir)
		return
	}
	if n.IsDir {
		a.setActiveFolder(n.Path)
		a.tree.Toggle(n)
		return
	}
	a.setActiveFolder(filepath.Dir(n.Path))
	a.openFile(n.Path)
}

// setActiveFolder records path as the editor's current working folder and
// mirrors it onto the file tree so the matching row renders with the
// "active" highlight. All writes to a.activeFolder go through here.
func (a *App) setActiveFolder(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	a.activeFolder = path
	if a.tree != nil {
		a.tree.ActiveFolder = path
	}
}

// tabBarClick switches or closes a tab based on where the click landed
// within its rendered geometry. Row 0 used to open with a ≡ menu button;
// with the menu gone the whole row belongs to the tabs, and a click on the
// empty part of it does nothing rather than popping an overlay the user
// did not aim at.
func (a *App) tabBarClick(x, _ int) {
	for _, r := range a.lastTabRects {
		if x >= r.X && x < r.X+r.Width {
			if x == r.CloseX {
				a.requestCloseTab(r.Index)
				return
			}
			a.activeTab = r.Index
			a.syncActiveTreeFile()
			return
		}
	}
}

// syncActiveTreeFile mirrors the active tab path into the file tree.
func (a *App) syncActiveTreeFile() {
	if a.tree == nil {
		return
	}
	tab := a.activeTabPtr()
	if tab == nil || tab.Path == "" {
		a.tree.ActiveFile = ""
		return
	}
	a.tree.ActiveFile = tab.Path
}

// editorPress handles the initial mouse press inside the editor — placing
// the caret, optionally selecting a word on double-click. Image tabs
// have no caret, so the press is dropped.
func (a *App) editorPress(x, y int) {
	tab := a.activeTabPtr()
	if tab == nil || tab.IsImage() {
		return
	}
	ex, ey, ew, eh := a.editorRect()
	if a.openGitHunkAt(tab, x-ex, y-ey) {
		return
	}
	// A press on the review composer or on a saved note's marker belongs
	// to the review layer, not to the diff underneath it. Claimed before
	// the hit test so clicking inside the box you are typing into doesn't
	// move the selection out from under it.
	if a.reviewDiffPress(tab, x-ex, y-ey) {
		return
	}
	pos, ok := tab.HitTest(x-ex, y-ey, ew, eh)
	if !ok {
		return
	}

	now := time.Now()
	if a.lastClick.x == x && a.lastClick.y == y && now.Sub(a.lastClick.when) < doubleClickMs {
		a.selectWordAt(tab, pos)
		a.lastClick = clickRecord{} // prevent triple-click from selecting nothing.
		return
	}
	a.lastClick = clickRecord{x: x, y: y, when: now}
	tab.MoveCursorTo(pos, false)
}

// openGitHunkAt opens the file's inline diff, scrolled to the clicked line,
// when the user clicks a change bar in the editor's git gutter. Returns true
// when it handled the click so the caller skips normal cursor placement.
//
// spice-edit showed the hunk in a scrollable info modal here. Now that
// Vincent has a real diff view there's no reason for a second, worse one:
// the modal couldn't be scrolled alongside the file, couldn't be kept open,
// and had nowhere to hang a review note. Phase 2 attaches comments to diff
// rows, which the modal could never have supported.
func (a *App) openGitHunkAt(tab *editor.Tab, localX, localY int) bool {
	if localX != 0 || localY < 0 || tab.IsDiff() {
		return false
	}
	line := tab.ScrollY + localY
	if tab.GitLines[line] == editor.GitLineNone {
		return false
	}
	a.openDiffAtLine(tab.Path, line)
	return true
}

// editorDrag extends the selection during a click-drag inside the editor.
// (x, y) is clamped to the editor rect so dragging into another pane still
// extends the selection sensibly. When the mouse passes above or below the
// editor we engage auto-scroll so the user can select content that's not
// yet on screen — same feel as VS Code or any GUI text editor. Image tabs
// drop the drag entirely.
func (a *App) editorDrag(x, y int) {
	tab := a.activeTabPtr()
	if tab == nil || tab.IsImage() {
		return
	}
	ex, ey, ew, eh := a.editorRect()

	// Remember where the mouse is so the auto-scroll tick can extend the
	// selection at this column even while the mouse stops moving.
	a.lastDragX = x
	a.lastDragY = y

	// Edge detection: outside the editor's vertical bounds turns on
	// auto-scroll; back inside turns it off.
	switch {
	case y < ey:
		a.startAutoScroll(-1)
	case y >= ey+eh:
		a.startAutoScroll(1)
	default:
		a.stopAutoScroll()
	}

	// Clamp the mouse into the editor and extend the selection there.
	localX := x - ex
	localY := y - ey
	if localX < 0 {
		localX = 0
	}
	if localY < 0 {
		localY = 0
	}
	if localX >= ew {
		localX = ew - 1
	}
	if localY >= eh {
		localY = eh - 1
	}
	pos, ok := tab.HitTest(localX, localY, ew, eh)
	if !ok {
		return
	}
	tab.MoveCursorTo(pos, true)
}

// startAutoScroll begins a timer goroutine that posts autoScrollEvents at
// autoScrollTick intervals so the editor keeps scrolling while the user
// holds the mouse past an edge. dir is -1 (up) or +1 (down). Calling with
// the same direction is a no-op so we don't restart the timer on every
// drag motion event.
func (a *App) startAutoScroll(dir int) {
	if a.autoScrollDir == dir {
		return
	}
	a.stopAutoScroll()
	a.autoScrollDir = dir
	a.autoScrollStop = make(chan struct{})
	stop := a.autoScrollStop
	scr := a.screen
	go func() {
		ticker := time.NewTicker(autoScrollTick)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case t := <-ticker.C:
				_ = scr.PostEvent(&autoScrollEvent{when: t})
			}
		}
	}()
}

// stopAutoScroll signals the auto-scroll goroutine to exit (idempotent).
func (a *App) stopAutoScroll() {
	if a.autoScrollStop != nil {
		close(a.autoScrollStop)
		a.autoScrollStop = nil
	}
	a.autoScrollDir = 0
}

// handleAutoScroll runs once per autoScrollEvent: nudge the viewport in the
// armed direction and extend the selection to the edge row at the user's
// last known mouse column. Bails out (and stops the timer) if anything
// suggests the user is no longer drag-selecting (button released, menu
// opened, no active tab).
func (a *App) handleAutoScroll() {
	if a.autoScrollDir == 0 || a.dragMode != "editor" || a.anyModalOpen() {
		a.stopAutoScroll()
		return
	}
	tab := a.activeTabPtr()
	if tab == nil {
		a.stopAutoScroll()
		return
	}
	tab.Scroll(a.autoScrollDir)

	ex, _, ew, eh := a.editorRect()
	localX := a.lastDragX - ex
	if localX < 0 {
		localX = 0
	}
	if localX >= ew {
		localX = ew - 1
	}
	localY := eh - 1
	if a.autoScrollDir < 0 {
		localY = 0
	}
	pos, ok := tab.HitTest(localX, localY, ew, eh)
	if !ok {
		return
	}
	tab.MoveCursorTo(pos, true)
}

// selectWordAt selects the word under the buffer position p (or does
// nothing if p sits in whitespace / punctuation).
func (a *App) selectWordAt(tab *editor.Tab, p editor.Position) {
	line := tab.Buffer.LineRunes(p.Line)
	if len(line) == 0 {
		return
	}
	start := p.Col
	if start > len(line) {
		start = len(line)
	}
	for start > 0 && isWordChar(line[start-1]) {
		start--
	}
	end := p.Col
	for end < len(line) && isWordChar(line[end]) {
		end++
	}
	if start == end {
		return
	}
	tab.Anchor = editor.Position{Line: p.Line, Col: start}
	tab.Cursor = editor.Position{Line: p.Line, Col: end}
}

// isWordChar reports whether r is part of a "word" for double-click select.
// Intentionally simple ASCII-ish definition; covers the common cases.
func isWordChar(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// -----------------------------------------------------------------------------
// Tab + clipboard actions
// -----------------------------------------------------------------------------

// activeTabPtr returns the currently active *editor.Tab, or nil when there
// are no tabs open.
func (a *App) activeTabPtr() *editor.Tab {
	if a.activeTab < 0 || a.activeTab >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.activeTab]
}

// flash sets a transient status message that displays for statusFlashFor
// before the status bar reverts to the active file's info.
func (a *App) flash(msg string) {
	a.statusMsg = msg
	a.statusUntil = time.Now().Add(statusFlashFor)
}

// OpenFile opens the file at path in a new tab — or switches to it if
// it is already open. Exported so main.go can seed the editor with the
// file the user named on the command line ("vincent foo.go"). Thin
// wrapper around openFile so internal callers keep using the lowercase
// name and the public surface stays small.
func (a *App) OpenFile(path string) { a.openFile(path) }

// openFile opens the file at path in a new tab — or switches to it if it is
// already open in another tab. Errors are surfaced as a flash message.
// Whatever the path resolves to, its parent becomes the active folder so
// the next New File from the main menu lands next to it.
func (a *App) openFile(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	a.setActiveFolder(filepath.Dir(path))
	if a.tree != nil {
		a.tree.ActiveFile = path
		// Reveal the file's location in the sidebar: expand every ancestor
		// directory and scroll the row into view. Without this, opening a
		// file via the finder (Esc-p) or the command line leaves the tree
		// collapsed at the top, so the active-file highlight is set on a
		// row nobody can see. listH mirrors Render's own list-area height
		// (sidebarH - 2) so the "already visible" guard inside Reveal uses
		// the same viewport the next paint will.
		_, _, _, sh := a.sidebarRect()
		listH := sh - 2
		if listH < 0 {
			listH = 0
		}
		a.tree.Reveal(path, listH)
	}
	for i, t := range a.tabs {
		// Mode matters as well as path: a file and its diff are separate
		// tabs sharing one Path, and "open foo.go" must never land on the
		// diff of foo.go.
		if t.Path == path && !t.IsDiff() {
			a.activeTab = i
			t.GitLines = loadGitLineChanges(a.rootDir, t.Path)
			return
		}
	}
	t, err := editor.NewTab(path)
	if err != nil {
		a.flash(fmt.Sprintf("Error: %v", err))
		return
	}
	a.tabs = append(a.tabs, t)
	a.activeTab = len(a.tabs) - 1
	t.GitLines = loadGitLineChanges(a.rootDir, t.Path)
	a.flash(fmt.Sprintf("Opened %s", filepath.Base(path)))
}

// saveActiveTab writes the active tab's buffer to disk.
func (a *App) saveActiveTab() {
	a.saveTabAt(a.activeTab)
}

// saveTabAt saves the tab at idx. Returns true on success, false on
// any kind of failure (no tab, untitled, IO error). Failures flash a
// status message so the caller doesn't have to. Pulled out from
// saveActiveTab so the dirty-close modal can save a specific tab and
// branch on success — saving and then closing must not eat the user's
// work when the save itself failed.
func (a *App) saveTabAt(idx int) bool {
	if idx < 0 || idx >= len(a.tabs) {
		return false
	}
	tab := a.tabs[idx]
	if tab.Path == "" {
		a.flash("Saving untitled tabs is not supported yet")
		return false
	}
	if tab.Conflict {
		// The file moved under us. Do not write — ask. Returning false
		// aborts whatever this save was a step of (a close, a quit), which
		// is right: the user has a decision to make first.
		a.openConflictPrompt(idx)
		return false
	}
	if err := tab.Save(); err != nil {
		a.flash(fmt.Sprintf("Save failed: %v", err))
		return false
	}
	a.refreshGitStatus()
	a.flash(fmt.Sprintf("Saved %s", filepath.Base(tab.Path)))
	return true
}

// saveAllDirty walks every open tab and saves each dirty one. Returns
// true when every dirty tab saved successfully — used by the quit flow
// to decide whether it's safe to actually exit. The first failure
// short-circuits because there's no point cascading more failed saves
// past one we've already flashed about, and the user needs to react to
// the first error before deciding what to do with the rest.
func (a *App) saveAllDirty() bool {
	for i, tab := range a.tabs {
		if !tab.Dirty {
			continue
		}
		if !a.saveTabAt(i) {
			return false
		}
	}
	return true
}

// dirtyTabCount returns the number of tabs with unsaved changes.
// Used by the quit flow to decide whether to skip the modal entirely.
func (a *App) dirtyTabCount() int {
	n := 0
	for _, tab := range a.tabs {
		if tab.Dirty {
			n++
		}
	}
	return n
}

// requestCloseTab closes the tab at idx. A clean tab closes immediately;
// a dirty tab opens the unsaved-changes modal so the user can pick
// Save / Discard / Cancel. The Save path saves the buffer first and only
// closes the tab on success — a save error would otherwise silently lose
// the user's work.
func (a *App) requestCloseTab(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	tab := a.tabs[idx]
	if !tab.Dirty {
		a.closeTab(idx)
		return
	}
	name := filepath.Base(tab.Path)
	if name == "" || name == "." {
		name = "untitled"
	}
	a.openDirtyClose(
		"Unsaved changes",
		name+" has unsaved changes.",
		func(app *App) {
			// Save → close. saveTabAt flashes its own error, in which
			// case we keep the tab around so the user can react.
			if app.saveTabAt(idx) {
				app.closeTab(idx)
			}
		},
		func(app *App) { app.closeTab(idx) },
	)
}

// closeTab removes the tab at idx without any dirty-check.
func (a *App) closeTab(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	// A composer belonging to the tab going away has nowhere left to
	// draw, so drop it instead of leaving state pointing at a freed tab.
	// The note is unsaved either way — closing the diff you were writing
	// about is as clear a cancel as Esc.
	if a.composerTab == a.tabs[idx] {
		a.closeReviewComposer()
	}
	a.tabs = append(a.tabs[:idx], a.tabs[idx+1:]...)
	if a.activeTab >= len(a.tabs) {
		a.activeTab = len(a.tabs) - 1
	}
	if a.activeTab < 0 {
		a.activeTab = 0
	}
	a.syncActiveTreeFile()
}

// copySelection puts the active tab's selection on the system clipboard
// (via OSC 52) and into the editor's internal clipboard.
func (a *App) copySelection() {
	tab := a.activeTabPtr()
	if tab == nil || !tab.HasSelection() {
		return
	}
	txt := tab.SelectionText()
	a.clipBuf = txt
	if err := clipboard.CopyToSystem(txt); err != nil {
		a.flash("Copied (system clipboard unavailable)")
		return
	}
	a.flash("Copied")
}

// cutSelection copies the selection then deletes it.
func (a *App) cutSelection() {
	tab := a.activeTabPtr()
	if tab == nil || !tab.HasSelection() {
		return
	}
	a.copySelection()
	tab.DeleteSelection()
}

// pasteClipboard inserts the editor's internal clipboard at the cursor.
// We can't read the system clipboard from a TUI, so external pastes have
// to come in through the user's terminal paste (Cmd-V / right-click paste).
func (a *App) pasteClipboard() {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	if a.clipBuf == "" {
		a.flash("Internal clipboard empty — paste from your terminal (Cmd-V)")
		return
	}
	tab.InsertString(a.clipBuf)
}

// leaderArmed reports whether an Esc is currently waiting for a second key.
// Drives the status-bar hint so the leader window is something the user can
// see rather than something they have to time blind.
func (a *App) leaderArmed() bool {
	return !a.lastEscape.IsZero() && time.Since(a.lastEscape) < doubleEscMs
}

// leaderHint builds the armed-leader line for the status bar: "Esc — "
// followed by every binding as "<key> <hint>", middle-dot separated, in
// leaderRows order.
//
// Generated rather than written out because the hand-written version went
// stale exactly the way you would expect — it still said "f find · t tree"
// two renames after f became the file explorer and / became find, so the
// one piece of UI whose whole job is telling you what the keys are was
// telling you the wrong keys. The caller truncates it to the width it has,
// which is why leaderRows puts the review bindings first: on a narrow
// terminal the tail is what gets dropped.
func leaderHint() string {
	var b strings.Builder
	b.WriteString(" Esc — ")
	for i, r := range leaderRows() {
		if i > 0 {
			b.WriteString(" · ")
		}
		b.WriteString(r.key)
		b.WriteByte(' ')
		b.WriteString(r.hint)
	}
	return b.String()
}

// hasTab reports whether there is an active tab to act on.
func (a *App) hasTab() bool { return a.activeTabPtr() != nil }

// hasSavableTab reports whether the active tab is one we can persist —
// it must exist, have a path on disk, and not be read-only. Used by Save
// and Save & Close.
//
// The read-only check covers diffs as well as image previews, and it
// matters more for diffs: a diff tab carries the real file's Path, so an
// enabled Save row would offer to write the diff text over the source.
func (a *App) hasSavableTab() bool {
	t := a.activeTabPtr()
	return t != nil && t.Path != "" && !t.ReadOnly()
}

// hasFileTab reports whether the active tab is backed by a real file
// (text or image). Used by Rename / Delete which act on the file
// regardless of how the tab is rendered.
func (a *App) hasFileTab() bool {
	t := a.activeTabPtr()
	return t != nil && t.Path != ""
}

// hasSelection reports whether the active tab has a non-empty selection.
func (a *App) hasSelection() bool {
	t := a.activeTabPtr()
	return t != nil && t.HasSelection()
}

// hasCommentableTab reports whether the active tab is editable text with a
// known single-line comment marker. Read-only tabs are excluded: toggling a
// comment on a diff row would edit the rendered diff, not the file.
func (a *App) hasCommentableTab() bool {
	t := a.activeTabPtr()
	if t == nil || t.ReadOnly() {
		return false
	}
	_, ok := editor.LineCommentPrefix(t.Path)
	return ok
}

// hasClipboard reports whether the editor's internal clipboard has content
// to paste.
func (a *App) hasClipboard() bool { return a.clipBuf != "" }

// hasUndo reports whether the active tab has anything to undo. Used to
// enable / disable the Undo row in the action menu.
func (a *App) hasUndo() bool {
	t := a.activeTabPtr()
	return t != nil && t.CanUndo()
}

// hasRedo reports whether the active tab has anything to redo.
func (a *App) hasRedo() bool {
	t := a.activeTabPtr()
	return t != nil && t.CanRedo()
}

// hasRevert reports whether the active tab differs from its on-open
// (or last-reload) baseline — i.e. there is something to revert.
func (a *App) hasRevert() bool {
	t := a.activeTabPtr()
	return t != nil && t.CanRevert()
}

// menuUndo rolls the active tab back one undo step.
func (a *App) menuUndo() {
	t := a.activeTabPtr()
	if t == nil {
		return
	}
	if !t.Undo() {
		a.flash("Nothing to undo")
	}
}

// menuRedo re-applies the most recently undone step.
func (a *App) menuRedo() {
	t := a.activeTabPtr()
	if t == nil {
		return
	}
	if !t.Redo() {
		a.flash("Nothing to redo")
	}
}

// menuRevert rewinds the active tab all the way back to the buffer
// state we captured the moment the file was opened (or last reloaded).
// The pre-revert state goes onto the undo stack so an accidental click
// is recoverable with one Undo.
func (a *App) menuRevert() {
	t := a.activeTabPtr()
	if t == nil {
		return
	}
	if !t.RevertFile() {
		a.flash("File matches its on-open state — nothing to revert")
		return
	}
	a.flash("Reverted to on-open state — Undo to recover")
}

// menuSave runs the Save action and dismisses the menu.
func (a *App) menuSave() {
	a.saveActiveTab()
}

// menuClose closes the active tab via the same dirty-tab confirmation flow
// used by clicking the × on the tab.
func (a *App) menuClose() {
	a.requestCloseTab(a.activeTab)
}

// menuCopy copies the current selection.
func (a *App) menuCopy() {
	a.copySelection()
}

// menuSelectAll selects the whole buffer of the active tab. Tab.SelectAll
// existed since the fork with no caller; Esc a and the menu row give it
// one. It works on diff tabs too — selecting a diff to copy it is a
// legitimate read-only gesture — but not on image tabs, which have no
// text to select.
func (a *App) menuSelectAll() {
	tab := a.activeTabPtr()
	if tab == nil || tab.IsImage() {
		return
	}
	tab.SelectAll()
}

// menuCut cuts the current selection.
func (a *App) menuCut() {
	a.cutSelection()
}

// menuPaste pastes the editor's internal clipboard at the cursor.
func (a *App) menuPaste() {
	a.pasteClipboard()
}

// menuToggleLineComment comments or uncomments the active line selection.
func (a *App) menuToggleLineComment() {
	tab := a.activeTabPtr()
	if tab == nil || tab.IsImage() {
		return
	}
	changed, ok := tab.ToggleLineComment()
	if !ok {
		a.flash("No line comment syntax for this file")
		return
	}
	if !changed {
		a.flash("No non-blank lines to comment")
		return
	}
	a.flash("Toggled line comment")
}

// menuRefreshTree forces an immediate sidebar reload. Currently unwired
// from the menu — the 10s background poller covers the common case — but
// the method is kept so re-adding the menu row (see menuItems) only
// requires uncommenting one line.
func (a *App) menuRefreshTree() {
	a.refreshTree()
	a.flash("File tree refreshed")
}

// menuToggleSidebar shows or hides the file explorer panel. The editor and
// tab bar reflow to fill the freed cells when the panel is hidden, and
// snap back when it returns.
func (a *App) menuToggleSidebar() {
	// Single-file mode has no file tree, so there's nothing to show or
	// hide, and Esc f reaches here regardless — guard it so the toggle
	// can't flip sidebarShown true and send draw() into a.tree.Render on
	// a nil tree.
	if a.tree == nil {
		a.flash("No file explorer in single-file mode")
		return
	}
	a.sidebarShown = !a.sidebarShown
}

// menuCollapseTree folds every folder in the file tree back to the root.
// Bound to Esc z and offered on a tree row's right-click.
//
// Reviewing agent work expands the tree behind your back: every file
// opened from the Changes panel or the finder calls Tree.Reveal, which
// expands its ancestors, so after twenty minutes the sidebar is a long
// ragged column shaped like your history rather than like the project.
// This is the one key that gets the shape back.
func (a *App) menuCollapseTree() {
	if a.tree == nil {
		a.flash("No file explorer in single-file mode")
		return
	}
	a.tree.CollapseAll()
	a.flash("Folded all folders")
}

// sidebarToggleLabel returns "Hide file explorer" or "Show file explorer"
// for the current state. It lost its caller with the ≡ menu's dynamic
// label row; kept (and still tested) because the phase-3b panel header is
// the next surface that wants exactly this wording.
func (a *App) sidebarToggleLabel() string {
	if a.sidebarShown {
		return "Hide file explorer"
	}
	return "Show file explorer"
}

// menuToggleTabBar shows or hides the tab strip in row 0. The row itself
// always stays — see tabBarRect/editorRect — only its contents change: a
// full strip of open tabs when on, just the active tab's name when off
// (see drawTabBar).
func (a *App) menuToggleTabBar() {
	a.tabBarShown = !a.tabBarShown
}

// tabBarToggleLabel returns "Hide tab bar" or "Show tab bar" for the
// current state. Same story as sidebarToggleLabel above: no caller since
// the ≡ menu went away, kept for the next surface that needs the wording.
func (a *App) tabBarToggleLabel() string {
	if a.tabBarShown {
		return "Hide tab bar"
	}
	return "Show tab bar"
}

// menuQuit exits the editor. When any tab has unsaved changes, opens the
// dirty-close modal so the user can pick Save (save all then quit),
// Discard (quit anyway), or Cancel. With no dirty tabs we exit straight
// away.
func (a *App) menuQuit() {
	dirty := a.dirtyTabCount()
	if dirty == 0 {
		a.quit = true
		return
	}
	var message string
	if dirty == 1 {
		// Find the one dirty tab so we can name it in the modal.
		for _, tab := range a.tabs {
			if tab.Dirty {
				name := filepath.Base(tab.Path)
				if name == "" || name == "." {
					name = "untitled"
				}
				message = name + " has unsaved changes. Save before quitting?"
				break
			}
		}
	} else {
		message = fmt.Sprintf("%d files have unsaved changes. Save all before quitting?", dirty)
	}
	a.openDirtyClose(
		"Unsaved changes",
		message,
		func(app *App) {
			// Only quit if every save succeeded — a half-saved exit
			// would silently lose work on whichever tab failed.
			if app.saveAllDirty() {
				app.quit = true
			}
		},
		func(app *App) { app.quit = true },
	)
}

// -----------------------------------------------------------------------------
// Drawing
// -----------------------------------------------------------------------------

// draw paints the entire screen. Called from the main loop for any event
// that changed something observable — see frame.go for the pure-motion
// events that no longer reach it. The action modal — if open — is drawn last
// so it sits on top of everything.
//
// The deferred frameKey stamp is what the motion guard compares against, and
// it has to be taken on the way out rather than the way in: drawing the git
// panel re-clamps its scroll offset, so a key sampled at the top would
// record a value the painted frame does not match and buy a spurious repaint
// on the next motion event. The counter is for tests.
func (a *App) draw() {
	a.frames++
	defer func() { a.lastFrame = a.frameKey() }()

	// No screen.Clear() here, deliberately. Clear() writes a space in the
	// default style into every cell of the back buffer, which marks all
	// 10,000 of them dirty and throws away tcell's record of what it last
	// sent — so the painters below re-transmit the entire screen even when
	// the frame is identical to the one before it. Every region fills its
	// own rect (tree, splitters, panel, tab bar, editor, find bar, status
	// bar, and drawTooSmall for the degenerate case), and between them they
	// tile the window exactly, so the clear was redundant for coverage and
	// expensive for bandwidth. A resize is the one case that genuinely needs
	// a full repaint, and handleEvent calls screen.Sync() for it.
	if a.width < minWidth || a.height < minHeight {
		a.drawTooSmall()
		return
	}

	if a.sidebarShown {
		sx, sy, sw, sh := a.sidebarRect()
		a.tree.Render(a.screen, a.theme, sx, sy, sw, sh)
		a.drawSplitter()
	}

	if a.gitPanelShown {
		a.drawGitPanel()
		a.drawGitSplitter()
	}

	a.drawTabBar()

	if tab := a.activeTabPtr(); tab != nil {
		ex, ey, ew, eh := a.editorRect()
		// Review overlays are derived state — which notes exist, and
		// whether the diff still contains the lines they were written
		// against — so they are rebuilt from the batch on every frame
		// rather than maintained alongside it. The rebuild also records
		// the marker click map the next press is tested against.
		if tab.IsDiff() {
			tab.SetDiffOverlays(a.buildDiffOverlays(tab, ew))
		}
		tab.Render(a.screen, a.theme, ex, ey, ew, eh)
	} else {
		a.drawEmptyEditor()
	}

	if a.findOpen {
		a.drawFindBar()
	}
	a.drawStatusBar()

	// Modal layering, bottom-up. Only one of these is open at a time
	// (closeAllModals enforces it), but the order still matters so a
	// future contributor can't accidentally double-open them.
	if a.cheatsheetOpen {
		a.drawCheatsheet()
	}
	if a.contextOpen {
		a.drawContext()
	}
	if a.promptOpen {
		a.drawPrompt()
	}
	if a.confirmOpen {
		a.drawConfirm()
	}
	if a.dirtyOpen {
		a.drawDirtyClose()
	}
	if a.finderOpen {
		a.drawFinder()
	}
	if a.pickerOpen {
		a.drawReviewPicker()
	}
}

// iconsOn reports whether Nerd Font glyphs should render in places
// outside the file tree (e.g. the tab bar). The single source of
// truth is the file tree — App.loadUserConfig stamped the resolved
// auto/on/off decision onto t.IconsEnabled there, so consulting the
// tree keeps tabs and tree perfectly in sync (turning icons off via
// config.json hides them everywhere at once).
func (a *App) iconsOn() bool {
	return a.tree != nil && a.tree.IconsEnabled
}

// layoutTabs computes the tabRect geometry for every tab. The strip starts
// at the sidebar's right edge — it used to start four cells in, past the ≡
// menu button — in the format:
//
//	" <dirty><icon? ><name> × " — a single space pad, two-cell dirty slot
//	(dot+space, or two spaces), an optional Nerd Font glyph + 1-space
//	separator (only when icons are enabled), the file name, a separator
//	space, the close ×, and a trailing space.
func (a *App) layoutTabs() []tabRect {
	out := make([]tabRect, 0, len(a.tabs))
	cursor := a.sidebarW()
	iconW := 0
	if a.iconsOn() {
		iconW = 2 // glyph + space
	}
	for i, t := range a.tabs {
		nameLen := len([]rune(t.DisplayName()))
		w := 1 + 2 + iconW + nameLen + 1 + 1 + 1 // pad+dirty+icon?+name+space+×+pad
		out = append(out, tabRect{
			Index:  i,
			X:      cursor,
			Width:  w,
			CloseX: cursor + 1 + 2 + iconW + nameLen + 1,
		})
		cursor += w
	}
	return out
}

// drawTabBar paints row 0 across the top of the editor area: either the
// full tab strip (tabBarShown) or just the active tab's name (see
// drawActiveTabName) when the strip is toggled off. The row itself is
// never skipped — see tabBarRect/editorRect — only its contents change.
// The ≡ button that used to lead the row is gone with the action menu.
func (a *App) drawTabBar() {
	tx, ty, tw, _ := a.tabBarRect()
	barStyle := tcell.StyleDefault.Background(a.theme.SidebarBG).Foreground(a.theme.Muted)
	for cx := tx; cx < tx+tw; cx++ {
		a.screen.SetContent(cx, ty, ' ', nil, barStyle)
	}

	if !a.tabBarShown {
		// No tab strip to hit-test against — clearing lastTabRects means a
		// stale click target from before the toggle can't fire.
		a.lastTabRects = nil
		a.drawActiveTabName(tx, ty, tw)
		return
	}

	rects := a.layoutTabs()
	a.lastTabRects = rects
	for _, r := range rects {
		active := r.Index == a.activeTab
		bg := a.theme.SidebarBG
		fg := a.theme.Muted
		if active {
			bg = a.theme.BG
			fg = a.theme.Text
		}
		st := tcell.StyleDefault.Background(bg).Foreground(fg)
		if active {
			st = st.Bold(true)
		}
		// Background.
		for cx := r.X; cx < r.X+r.Width; cx++ {
			if cx >= tx+tw {
				break
			}
			a.screen.SetContent(cx, ty, ' ', nil, st)
		}
		tab := a.tabs[r.Index]
		col := r.X + 1
		if tab.Dirty || tab.Conflict {
			// One dot, two states. Conflict outranks dirty: both mean
			// "unsaved", but only one of them can lose an agent's work.
			dot := a.theme.Modified
			if tab.Conflict {
				dot = a.theme.Conflict
			}
			a.screen.SetContent(col, ty, '●', nil, st.Foreground(dot))
		}
		col += 2 // skip dirty slot.
		// Per-language Nerd Font glyph between the dirty dot and the
		// filename — only when icons are enabled. Coloured the same
		// way the file tree glyphs are (icons.ColorFor) so the eye
		// connects "this tab" to "that row in the tree" instantly.
		if a.iconsOn() {
			name := tab.DisplayName()
			glyph := icons.For(name, false, false)
			gfg := icons.ColorFor(name, false, fg)
			gst := tcell.StyleDefault.Background(bg).Foreground(gfg)
			if active {
				gst = gst.Bold(true)
			}
			for _, gr := range glyph {
				if col >= tx+tw {
					break
				}
				a.screen.SetContent(col, ty, gr, nil, gst)
				col++
			}
			col++ // separator space after glyph
		}
		for _, ru := range tab.DisplayName() {
			if col >= tx+tw {
				break
			}
			a.screen.SetContent(col, ty, ru, nil, st)
			col++
		}
		col++ // separator space before ×
		if col < tx+tw {
			closeStyle := st.Foreground(a.theme.Muted)
			if active {
				closeStyle = st.Foreground(a.theme.Subtle)
			}
			a.screen.SetContent(col, ty, '×', nil, closeStyle)
		}
	}
}

// drawActiveTabName paints the active tab's dirty dot (if any) and display
// name directly in row 0, at the same column layoutTabs would put the
// first tab's dirty slot. This is what row 0 shows when tabBarShown is
// off: "what file am I looking at" survives the tab strip disappearing,
// without the row itself going away.
func (a *App) drawActiveTabName(tx, ty, tw int) {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	style := tcell.StyleDefault.Background(a.theme.SidebarBG).Foreground(a.theme.Muted)
	col := tx + 1
	if tab.Dirty {
		a.screen.SetContent(col, ty, '●', nil, style.Foreground(a.theme.Modified))
	}
	col += 2 // skip dirty slot, dirty or not — matches layoutTabs' spacing.
	for _, ru := range tab.DisplayName() {
		if col >= tx+tw {
			break
		}
		a.screen.SetContent(col, ty, ru, nil, style)
		col++
	}
}

// drawSplitter paints a 1-column vertical line at the right edge of the
// sidebar. Idle it sits in Subtle grey; while the user is dragging it
// brightens to Accent so the active grab handle is unmistakable.
func (a *App) drawSplitter() {
	x := a.splitterX()
	if x < 0 {
		return
	}
	fg := a.theme.Subtle
	if a.dragMode == "sidebar" {
		fg = a.theme.Accent
	}
	style := tcell.StyleDefault.Background(a.theme.SidebarBG).Foreground(fg)
	for y := 0; y < a.height-1; y++ {
		a.screen.SetContent(x, y, '│', nil, style)
	}
}

// drawGitSplitter paints the Changes panel's grab handle down its LEFT
// edge — the mirror of drawSplitter, which sits on the sidebar's right.
// Same idle / dragging colours so both handles read as the same control.
func (a *App) drawGitSplitter() {
	x := a.gitSplitterX()
	if x < 0 {
		return
	}
	fg := a.theme.Subtle
	if a.dragMode == "gitpanel" {
		fg = a.theme.Accent
	}
	style := tcell.StyleDefault.Background(a.theme.SidebarBG).Foreground(fg)
	for y := 0; y < a.height-1; y++ {
		a.screen.SetContent(x, y, '│', nil, style)
	}
}

// drawEmptyEditor paints the placeholder shown when no tabs are open.
func (a *App) drawEmptyEditor() {
	ex, ey, ew, eh := a.editorRect()
	bg := a.theme.BG
	muted := tcell.StyleDefault.Background(bg).Foreground(a.theme.Muted)
	bold := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text).Bold(true)
	for cy := ey; cy < ey+eh; cy++ {
		for cx := ex; cx < ex+ew; cx++ {
			a.screen.SetContent(cx, cy, ' ', nil, muted)
		}
	}
	cy := ey + eh/2
	msg1 := "No file open"
	msg2 := "Click a file in the tree, or press Esc ? for the keys"
	cx1 := ex + (ew-len([]rune(msg1)))/2
	for i, r := range msg1 {
		a.screen.SetContent(cx1+i, cy-1, r, nil, bold)
	}
	cx2 := ex + (ew-len([]rune(msg2)))/2
	for i, r := range msg2 {
		a.screen.SetContent(cx2+i, cy+1, r, nil, muted)
	}
	a.screen.HideCursor()
}

// drawStatusBar paints the bottom status bar.
func (a *App) drawStatusBar() {
	sx, sy, sw, _ := a.statusRect()
	bg := a.theme.StatusBG
	fg := a.theme.StatusFG
	style := tcell.StyleDefault.Background(bg).Foreground(fg).Bold(true)
	for cx := sx; cx < sx+sw; cx++ {
		a.screen.SetContent(cx, sy, ' ', nil, style)
	}

	// Right-side text: current git branch when we're inside a repo. Drawn
	// first so the left-side text can be clipped against it and the two
	// pieces never overlap on a narrow window.
	var rightWidth int
	if a.gitBranch != "" {
		right := " " + a.gitBranch + " "
		rw := len([]rune(right))
		if rw < sw {
			drawAt(a.screen, sx+sw-rw, sy, right, style)
			rightWidth = rw
		}
	}

	// Left-side text: an armed Esc outranks everything else. Without this
	// the leader is invisible state — you press Esc, nothing appears to
	// happen, and whether the next key is a command or a keystroke depends
	// on a timer you cannot see.
	var left string
	if a.leaderArmed() {
		max := sw - rightWidth
		drawStatusText(a.screen, sx, sy, max, trimRunes(leaderHint(), max), style)
		return
	}
	if time.Now().Before(a.statusUntil) && a.statusMsg != "" {
		left = " " + a.statusMsg
	} else if tab := a.activeTabPtr(); tab != nil {
		if tab.IsImage() && tab.Image != nil {
			b := tab.Image.Bounds()
			left = fmt.Sprintf(" %s · %d×%d · %s",
				strings.ToUpper(tab.ImageFmt), b.Dx(), b.Dy(), filepath.Base(tab.Path))
		} else if tab.IsDiff() {
			// The counts are the headline on a diff — "how big is this
			// change" is the first thing a reviewer wants — so they lead,
			// ahead of the position readout a file tab shows.
			added, deleted := tab.DiffStats()
			left = fmt.Sprintf(" Diff · +%d −%d · %s", added, deleted, filepath.Base(tab.Path))
		} else {
			lang := detectLangLabel(tab.Path)
			dirty := ""
			switch {
			case tab.Conflict:
				// Spelled out rather than left to the dot's colour: the
				// flash that announced this expires after three seconds
				// and the state does not.
				dirty = " · ● changed on disk"
			case tab.Dirty:
				dirty = " · ●"
			}
			left = fmt.Sprintf(" %s · Ln %d, Col %d · %d lines%s",
				lang, tab.Cursor.Line+1, tab.Cursor.Col+1, tab.Buffer.LineCount(), dirty)
		}
	} else {
		left = " " + filepath.Base(a.rootDir)
	}
	// One cell of breathing room between left and right text so they
	// don't visually butt up against each other on a tight terminal.
	leftMax := sw - rightWidth
	if rightWidth > 0 {
		leftMax--
	}
	if leftMax < 0 {
		leftMax = 0
	}
	drawStatusText(a.screen, sx, sy, leftMax, left, style)
}

// drawTooSmall paints a centred error message when the terminal window is
// smaller than the editor's minimum supported size.
func (a *App) drawTooSmall() {
	style := tcell.StyleDefault.Background(a.theme.BG).Foreground(a.theme.Error).Bold(true)
	for cy := 0; cy < a.height; cy++ {
		for cx := 0; cx < a.width; cx++ {
			a.screen.SetContent(cx, cy, ' ', nil,
				tcell.StyleDefault.Background(a.theme.BG))
		}
	}
	msg := "Window too small — please resize"
	cy := a.height / 2
	cx := (a.width - len([]rune(msg))) / 2
	if cx < 0 {
		cx = 0
	}
	for i, r := range msg {
		if cx+i >= a.width {
			break
		}
		a.screen.SetContent(cx+i, cy, r, nil, style)
	}
	a.screen.HideCursor()
}

// drawStatusText writes s left-aligned into the status bar at (x, y) with a
// max width of maxW cells. Truncates rather than wraps.
func drawStatusText(scr tcell.Screen, x, y, maxW int, s string, st tcell.Style) {
	col := 0
	for _, r := range s {
		if col >= maxW {
			return
		}
		scr.SetContent(x+col, y, r, nil, st)
		col++
	}
}

// drawAt writes s starting at (x, y) without bounds checking. Callers are
// expected to keep the string within the rectangle they're drawing into.
func drawAt(scr tcell.Screen, x, y int, s string, st tcell.Style) {
	col := 0
	for _, r := range s {
		scr.SetContent(x+col, y, r, nil, st)
		col++
	}
}

// trimRunes shortens s to max visible cells, reserving the final cell for an
// ellipsis when truncation is needed.
func trimRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if runeLen(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	rs := []rune(s)
	return string(rs[:max-1]) + "…"
}

// detectLangLabel returns a short label for the active file's language —
// just the file extension, or "text" when there is no path or extension.
func detectLangLabel(path string) string {
	if path == "" {
		return "text"
	}
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext == "" {
		return "text"
	}
	return ext
}
