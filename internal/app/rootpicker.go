// =============================================================================
// File: internal/app/rootpicker.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Derives from internal/app/finder.go (Spicer Matthews / Cloudmanic, MIT):
// the modal's shape — centered box, one-line input on top, ~10 rows below,
// hover-follows-mouse, Enter opens, Esc closes — plus its horizontal
// input-scroll and matched-rune highlighting are that file's, reused so the
// two pickers feel identical rather than merely similar.
// =============================================================================

// rootpicker.go is the Esc-o root switcher: the gesture that changes which
// folder Vincent is pointed at without restarting it.
//
// It has two modes behind one input field, because the two questions a user
// actually asks are different shapes:
//
//   - RECENTS (the query is empty or not path-like). The list is the
//     recentRoots array from config.json minus wherever we are now, drawn
//     with the home directory abbreviated to "~" and fuzzy-filtered with
//     the finder's own scorer. This is the common case — a review session
//     bounces between three or four repos — and it is one keypress deep.
//   - BROWSE (the query starts with /, ~, or .). The list is the
//     subdirectories of whatever the typed path currently names, the way
//     shell completion behaves: Tab completes, Enter descends, and the
//     header says which directory you are standing in.
//
// Two rules worth knowing before changing any of it:
//
//   - In browse mode nothing is highlighted until you move onto it. That
//     is what makes "pick THIS folder" reachable at all: Enter with a
//     highlighted child descends into the child, and Enter with nothing
//     highlighted picks the directory the query already names. If the
//     first row were auto-selected the way it is in recents mode, a
//     folder with subdirectories could never be chosen.
//   - Esc closes the picker AND arms the Esc leader. Vincent's contract is
//     that Esc is always the leader (see leader.go), and the "let me pick
//     a folder off the machine" gesture is literally Esc-o pressed while
//     the picker is already up — which only works if the close leaves the
//     leader armed. openRootPicker notices it was re-invoked inside the
//     leader window and comes back in browse mode at "~/".
//
// setRoot lives here too. It is written to mirror App.New: New is the spec
// for "a valid App rooted at X", so a switch does what the constructor does
// after building the tree and nothing that only makes sense once (screen
// init, signal handlers, the review log).

package app

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/config"
	"github.com/chasereyn/vincent/internal/filetree"
	"github.com/chasereyn/vincent/internal/finder"
	"github.com/chasereyn/vincent/internal/review"
)

const (
	// rootPickerMaxWidth caps the modal. Paths are the content here and
	// they are longer than filenames, so this is wider than the finder's
	// 80-column box would need to be for the same number of rows — but
	// still narrow enough to read in one eye movement.
	rootPickerMaxWidth = 76
	// rootPickerRowsVisible is how many list rows are painted at once.
	// Ten matches the finder, and matches maxRecentRoots, so a full
	// recents list never needs scrolling.
	rootPickerRowsVisible = 10
	// rootPickerNoSelection is the "nothing is highlighted" sentinel. It
	// is a named constant because browse mode's Enter behaviour pivots on
	// it and a bare -1 in that comparison reads like a bug.
	rootPickerNoSelection = -1
)

// rootPickerRow is one drawn row of the picker's list. label is what the
// user sees, path is what gets acted on, and matched carries the rune
// indexes the fuzzy scorer lit up so the renderer can highlight them.
//
// isDir separates a browse-mode subdirectory (Enter descends into it) from
// a recents entry (Enter switches to it). Keeping the distinction on the
// row rather than re-deriving it from the mode means a click handler can
// act on the row it hit without knowing which mode produced it.
type rootPickerRow struct {
	label   string
	path    string
	matched []int
	isDir   bool
}

// rootPickerRowRect is the hit-test snapshot for one drawn row: the screen
// Y it landed on and the index into rows it represents. Recorded during the
// draw and tested against by the click handler, per the house rule — the
// alternative is row arithmetic duplicated in two places that drift the
// first time the layout gains a line.
type rootPickerRowRect struct {
	y     int
	index int
}

// rootPickerState is the whole of the root switcher's UI state, grouped
// into one struct so App grows a single field instead of a dozen.
type rootPickerState struct {
	open bool
	// browse is true when the query is a filesystem path rather than a
	// filter over the recents list. Derived from the query on every
	// refresh — it is cached here only so the renderer and the click
	// handler don't each re-derive it.
	browse bool

	query  []rune
	cursor int
	scroll int // horizontal scroll of the input field

	// recents is the recentRoots list as read from config.json when the
	// picker opened. Snapshotted rather than re-read per keystroke: the
	// list cannot change while the modal owns the keyboard, and a file
	// read per typed character is a waste.
	recents []string

	rows       []rootPickerRow
	selected   int // index into rows, or rootPickerNoSelection
	listScroll int

	// dir is the directory browse mode is currently listing, absolute.
	// Shown in the header so the user always knows where they are, which
	// is the thing that makes typing a path feel navigable instead of
	// blind.
	dir string

	// closedAt is when Esc last dismissed the picker. openRootPicker
	// compares it against the leader window to recognise Esc-o pressed
	// while the picker was up — see the file comment.
	closedAt time.Time

	rowRects []rootPickerRowRect
	// useX/useY/useW is the drawn rect of the "Use this folder" button,
	// browse mode's mouse path to picking the directory it is listing.
	// Recorded during the draw for the same reason rowRects is. useW == 0
	// means the button was not drawn.
	useX, useY, useW int
}

// -----------------------------------------------------------------------------
// Open / close
// -----------------------------------------------------------------------------

// openRootPicker is the Esc-o leader action and the tree's "Change root…"
// context entry. It opens in recents mode, except when it was invoked
// inside the leader window of its own Esc dismissal — that is the Esc-o
// pressed while the picker is already open, which the owner asked for as
// the "select a folder from the machine" gesture, and it opens in browse
// mode at "~/" instead.
func (a *App) openRootPicker() {
	if a.tree == nil {
		// Single-file mode has no project root to change — the same guard
		// openFinder makes, and for the same reason: the leader reaches
		// here directly even when no menu row does.
		a.flash("Change root isn't available in single-file mode")
		return
	}
	toggleToBrowse := a.rootPicker.open ||
		(!a.rootPicker.closedAt.IsZero() && time.Since(a.rootPicker.closedAt) < doubleEscMs)

	a.closeAllModals()
	st := &a.rootPicker
	st.open = true
	st.closedAt = time.Time{}
	st.recents = a.loadRecentRoots()
	st.cursor = 0
	st.scroll = 0
	st.listScroll = 0
	if toggleToBrowse {
		st.query = []rune("~" + string(filepath.Separator))
		st.cursor = len(st.query)
	} else {
		st.query = nil
	}
	a.refreshRootPickerRows()
}

// closeRootPicker dismisses the picker without stamping closedAt, which is
// the close every path except Esc wants: a click outside or a successful
// switch must not leave the "Esc-o again means browse" window armed.
func (a *App) closeRootPicker() {
	a.rootPicker.closedAt = time.Time{}
	a.resetRootPicker()
}

// escRootPicker is the Esc dismissal: it closes the picker, records when,
// and arms the Esc leader so the very next keystroke is a leader key. See
// the file comment for why the arming is load-bearing rather than a
// convenience.
func (a *App) escRootPicker() {
	a.resetRootPicker()
	a.rootPicker.closedAt = time.Now()
	a.lastEscape = time.Now()
}

// resetRootPicker clears every transient field but preserves closedAt, so
// closeAllModals (which cannot know why it was called) can't wipe the
// Esc-o-again window out from under escRootPicker.
func (a *App) resetRootPicker() {
	keep := a.rootPicker.closedAt
	a.rootPicker = rootPickerState{closedAt: keep, selected: rootPickerNoSelection}
}

// loadRecentRoots reads the recents list off disk for the picker to show.
// Returns nil on a missing or malformed config: the picker then shows its
// "type / or ~ to browse" hint, which is a better answer than an error
// modal in front of somebody trying to change folder.
func (a *App) loadRecentRoots() []string {
	if a.configPath == "" {
		return nil
	}
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return nil
	}
	return cfg.RecentRoots
}

// -----------------------------------------------------------------------------
// Rows
// -----------------------------------------------------------------------------

// refreshRootPickerRows rebuilds the list from the current query, picks the
// mode, and resets the selection to that mode's starting point. Called on
// every keystroke and after every descend.
//
// The two modes disagree about the starting selection on purpose: recents
// highlights row 0 so Enter is one keypress, browse highlights nothing so
// Enter means "the folder I have typed". See the file comment.
func (a *App) refreshRootPickerRows() {
	st := &a.rootPicker
	st.browse = rootPathLike(string(st.query))
	st.listScroll = 0
	if st.browse {
		st.rows, st.dir = a.browseRootRows(string(st.query))
		st.selected = rootPickerNoSelection
		return
	}
	st.dir = ""
	st.rows = a.recentRootRows(string(st.query))
	if len(st.rows) == 0 {
		st.selected = rootPickerNoSelection
		return
	}
	st.selected = 0
}

// recentRootRows filters the snapshotted recents list with the finder's
// scorer and returns them as drawable rows, most recent first when the
// query is empty.
//
// The current root is excluded: offering "switch to where you already are"
// wastes the row that would otherwise be a real destination, and it is the
// row muscle memory would land on first.
func (a *App) recentRootRows(query string) []rootPickerRow {
	query = trimSpace(query)
	current := filepath.Clean(a.rootDir)
	if abs, err := filepath.Abs(current); err == nil {
		current = abs
	}

	type scored struct {
		row   rootPickerRow
		score int
	}
	hits := make([]scored, 0, len(a.rootPicker.recents))
	for _, p := range a.rootPicker.recents {
		if filepath.Clean(p) == current {
			continue
		}
		label := displayPath(p)
		score, idx := finder.Score(query, label)
		if score == 0 {
			continue
		}
		hits = append(hits, scored{
			row:   rootPickerRow{label: label, path: p, matched: idx},
			score: score,
		})
	}
	// Ranking only when there is a query to rank by. An empty query scores
	// every entry identically, so the list keeps config.json's order —
	// most recent first, which is the only meaningful ranking without one.
	//
	// Insertion sort, because the list is at most maxRecentRoots long:
	// shorter than pulling in sort for ten items, and stable, so equal
	// scores keep their recency order.
	if query != "" {
		for i := 1; i < len(hits); i++ {
			for j := i; j > 0 && hits[j-1].score < hits[j].score; j-- {
				hits[j], hits[j-1] = hits[j-1], hits[j]
			}
		}
	}
	out := make([]rootPickerRow, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.row)
	}
	return out
}

// browseRootRows lists the subdirectories the typed path currently names,
// and returns them alongside the absolute directory being listed (which
// the header draws).
//
// Hidden directories appear only when the typed fragment itself starts
// with a dot. That is shell-completion behaviour, and it is the right
// default here: a repo root is almost never a dotfile, but ~/.config is
// somewhere people do go deliberately.
func (a *App) browseRootRows(query string) ([]rootPickerRow, string) {
	parent, partial := splitRootQuery(query)
	if parent == "" {
		return nil, ""
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, parent
	}
	wantHidden := strings.HasPrefix(partial, ".")
	lower := strings.ToLower(partial)
	// os.ReadDir returns entries sorted by filename, which is the order we
	// want, so there is no second sort here.
	out := make([]rootPickerRow, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !wantHidden && strings.HasPrefix(name, ".") {
			continue
		}
		if lower != "" && !strings.HasPrefix(strings.ToLower(name), lower) {
			continue
		}
		full := filepath.Join(parent, name)
		if !entryIsDir(full, e) {
			continue
		}
		out = append(out, rootPickerRow{
			label: name + string(filepath.Separator),
			path:  full,
			isDir: true,
		})
	}
	return out, parent
}

// entryIsDir reports whether a directory entry is a directory to browse
// into, following symlinks. A symlinked project directory is common enough
// (worktrees, ~/code pointing at a volume) that treating one as a
// non-directory would quietly hide it from the picker.
func entryIsDir(full string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.IsDir()
}

// -----------------------------------------------------------------------------
// Path helpers
// -----------------------------------------------------------------------------

// rootPathLike reports whether a query should be read as a filesystem path
// rather than as a filter over the recents list. Leading /, ~, or . are the
// three ways a person starts typing a path; a Windows drive letter ("C:")
// is accepted too so the mode switch isn't POSIX-only.
func rootPathLike(query string) bool {
	q := trimSpace(query)
	if q == "" {
		return false
	}
	switch q[0] {
	case '/', '~', '.', '\\':
		return true
	}
	return len(q) >= 2 && q[1] == ':'
}

// expandHome resolves a leading ~ to the user's home directory and
// normalises slashes to the platform separator, preserving a trailing
// separator because that is what tells splitRootQuery "list this directory"
// rather than "complete this name".
//
// os.UserHomeDir is the only way home is ever obtained here — it reads
// USERPROFILE on Windows and HOME elsewhere, and a literal "/Users/..."
// would be wrong on two of the three platforms CI runs.
func expandHome(p string) string {
	p = trimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.FromSlash(p)
	sep := string(filepath.Separator)
	trailing := strings.HasSuffix(p, sep)
	switch {
	case p == "~":
		if h := homeDir(); h != "" {
			p = h
		}
	case strings.HasPrefix(p, "~"+sep):
		if h := homeDir(); h != "" {
			p = filepath.Join(h, p[2:])
		}
	}
	if trailing && !strings.HasSuffix(p, sep) {
		p += sep
	}
	return p
}

// homeDir returns the user's home directory, or "" when it can't be
// resolved. Wrapped in a helper so every caller goes through os.UserHomeDir
// and nobody is tempted to read $HOME directly.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// displayPath abbreviates the home directory to "~" for display. Purely
// cosmetic — nothing resolves a path back out of this — but it is what
// makes a list of ten absolute paths scannable, and it is how Zed's own
// switcher renders the same list.
func displayPath(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	p = filepath.Clean(p)
	home := homeDir()
	if home == "" {
		return p
	}
	home = filepath.Clean(home)
	if p == home {
		return "~"
	}
	sep := string(filepath.Separator)
	if strings.HasPrefix(p, home+sep) {
		return "~" + p[len(home):]
	}
	return p
}

// splitRootQuery breaks a browse-mode query into the directory to list and
// the partial name to filter its children by — the same split shell
// completion makes.
//
// A trailing separator (or a bare "." / "..") means "list this directory,
// no filter". Anything else means "list the parent, keep children starting
// with the last segment", which is why typing "~/Dev" shows ~/Developer
// while "~/Developer/" shows what is inside it.
func splitRootQuery(query string) (parent, partial string) {
	exp := expandHome(query)
	if exp == "" {
		return "", ""
	}
	sep := string(filepath.Separator)
	if strings.HasSuffix(exp, sep) {
		return filepath.Clean(exp), ""
	}
	if exp == "." || exp == ".." {
		return filepath.Clean(exp), ""
	}
	return filepath.Dir(exp), filepath.Base(exp)
}

// rootQueryPrefix is the text a completion should be appended to: the typed
// query's directory part, in display form, with a trailing separator. Built
// from the resolved parent rather than by slicing the raw query so "~",
// "~/", "." and an absolute path all complete the same way.
func rootQueryPrefix(query string) string {
	parent, _ := splitRootQuery(query)
	if parent == "" {
		return ""
	}
	shown := displayPath(parent)
	sep := string(filepath.Separator)
	if !strings.HasSuffix(shown, sep) {
		shown += sep
	}
	return shown
}

// commonPrefixFold returns the longest case-insensitive common prefix of
// names, taken from the first name so the completion keeps real casing.
// Used by Tab: with several candidates, completing as far as they agree is
// more useful than completing nothing and more honest than guessing one.
func commonPrefixFold(names []string) string {
	if len(names) == 0 {
		return ""
	}
	prefix := []rune(names[0])
	for _, n := range names[1:] {
		other := []rune(n)
		if len(other) < len(prefix) {
			prefix = prefix[:len(other)]
		}
		for i := 0; i < len(prefix); i++ {
			if !strings.EqualFold(string(prefix[i]), string(other[i])) {
				prefix = prefix[:i]
				break
			}
		}
	}
	return string(prefix)
}

// -----------------------------------------------------------------------------
// Actions
// -----------------------------------------------------------------------------

// rootPickerActivate is Enter, and it means something different per mode.
//
// Recents: switch to the highlighted root. Browse: descend into the
// highlighted subdirectory if there is one, otherwise pick the directory
// the query already names. That second half is the whole reason browse
// mode starts with nothing highlighted.
func (a *App) rootPickerActivate() {
	st := &a.rootPicker
	if st.selected >= 0 && st.selected < len(st.rows) {
		row := st.rows[st.selected]
		if row.isDir {
			a.rootPickerDescend(row.path)
			return
		}
		a.pickRoot(row.path)
		return
	}
	if !st.browse {
		return
	}
	a.pickRoot(string(st.query))
}

// rootPickerDescend rewrites the query to dir plus a separator and reloads
// the list, which is what makes Enter (and a click) feel like walking into
// a folder. Selection resets to nothing so the next Enter picks dir itself.
func (a *App) rootPickerDescend(dir string) {
	st := &a.rootPicker
	shown := displayPath(dir)
	sep := string(filepath.Separator)
	if !strings.HasSuffix(shown, sep) {
		shown += sep
	}
	st.query = []rune(shown)
	st.cursor = len(st.query)
	st.scroll = 0
	a.refreshRootPickerRows()
}

// pickRoot tries to switch to path and closes the picker only if it worked.
// A failed switch keeps the modal up with the flash explaining why, so a
// typo costs one keystroke rather than the whole gesture.
func (a *App) pickRoot(path string) {
	if a.setRoot(path) {
		a.closeRootPicker()
	}
}

// rootPickerComplete is Tab: fill the query in as far as the candidates
// agree. One candidate completes all the way and descends into it; several
// complete to their common prefix; none does nothing.
//
// Recents mode has nothing to complete — its rows are whole paths, and
// Enter already takes them — so Tab is a no-op there.
func (a *App) rootPickerComplete() {
	st := &a.rootPicker
	if !st.browse || len(st.rows) == 0 {
		return
	}
	if len(st.rows) == 1 {
		a.rootPickerDescend(st.rows[0].path)
		return
	}
	names := make([]string, 0, len(st.rows))
	for _, r := range st.rows {
		names = append(names, filepath.Base(r.path))
	}
	prefix := commonPrefixFold(names)
	if prefix == "" {
		return
	}
	next := rootQueryPrefix(string(st.query)) + prefix
	if next == string(st.query) {
		return
	}
	st.query = []rune(next)
	st.cursor = len(st.query)
	st.scroll = 0
	a.refreshRootPickerRows()
}

// setRoot repoints Vincent at path: canonicalise, rebuild the tree, the
// finder index and the git panel, and remember the folder for next time.
// Returns false (with a flash) when path isn't a directory we can open.
//
// The body is deliberately a mirror of what App.New does after
// filetree.New — New is the definition of "a valid App rooted here", and a
// switch that skips one of its steps produces an App that looks fine until
// the first Esc-p or git refresh. What is NOT repeated is the setup that
// only makes sense once per process: screen and mouse init, the signal
// watcher, the review log, and the tree-refresh ticker all outlive a root
// change and re-running them would leak a goroutine per switch.
//
// Open tabs stay open on purpose. They are files the reviewer chose to
// look at; a folder change is not a reason to throw that away, and a tab
// whose file lives outside the new root still renders and still saves.
func (a *App) setRoot(path string) bool {
	if a.tree == nil {
		a.flash("Change root isn't available in single-file mode")
		return false
	}
	resolved, err := resolveRootPath(path)
	if err != nil {
		a.flash("Not a folder: " + trimSpace(path))
		return false
	}
	tree, err := filetree.New(resolved)
	if err != nil {
		a.flash("Can't open " + displayPath(resolved) + ": " + err.Error())
		return false
	}
	// Carry the resolved Nerd-Font decision across rather than re-running
	// icons.Resolve: loadUserConfig already made that call, and detection
	// is not free.
	tree.IconsEnabled = a.tree.IconsEnabled

	a.rootDir = resolved
	a.tree = tree
	a.sidebarShown = true
	a.setActiveFolder(tree.Root.Path)

	// A finder index is per-project, so the old one is not stale, it is
	// wrong. New() builds and kicks off a rebuild in exactly this shape.
	a.finder = finder.New(resolved)
	scr := a.screen
	a.finder.Rebuild(func() {
		_ = scr.PostEvent(&finderRebuiltEvent{when: time.Now()})
	})

	// The Changes panel's hover row, scroll offset, and click snapshot all
	// index into the OLD repo's entry list. Clearing them before the
	// refresh means no click can land on a row that belonged to a
	// different project.
	a.gitPanelHover = -1
	a.gitPanelScroll = 0
	a.lastGitPanelRows = nil
	a.refreshGitStatus()
	// Re-clamp the layout, but do NOT re-apply the startup panel default.
	// "Open the Changes panel in a repo" is an answer about how a session
	// starts; re-running it here re-opened a panel the user had closed on
	// purpose, one Esc g ago. The panel's visibility is now the user's
	// state and a root switch does not have an opinion about it. See
	// applyStartupPanelDefaults, which is a one-shot for the same reason.
	a.reflowPanels()

	a.recordRecentRoot(resolved)
	a.flash("Root: " + displayPath(resolved))
	return true
}

// resolveRootPath canonicalises a user-typed path into an absolute,
// symlink-free directory, or returns an error.
//
// EvalSymlinks matters more than it looks: git, the finder's git ls-files,
// and every path comparison in the tree all work in real paths, so a root
// reached through a symlink would produce a tree whose rows never match the
// git status output. When it fails we keep the absolute path — that is the
// case on Windows junctions and on paths behind a mount that refuses the
// walk — and let the Stat below be the real gate.
func resolveRootPath(path string) (string, error) {
	if trimSpace(path) == "" {
		return "", os.ErrInvalid
	}
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrInvalid
	}
	return filepath.Clean(abs), nil
}

// recordRecentRoot moves path to the front of config.json's recentRoots
// and writes the file back. Called at startup and on every switch, so the
// picker's list is ordered by when you were last in each folder.
//
// Two guards. A malformed config is left alone rather than overwritten —
// the user has a typo in a file they wrote, and silently replacing it with
// our defaults would delete their icons setting to fix a problem they can
// see. And an already-at-the-front path skips the write entirely, which is
// what stops every launch from rewriting the file for no change.
//
// A failed write goes to the herdr log rather than the status bar. Vincent
// has one log sink (see reviewlog.go) and this is a background bookkeeping
// failure: the root switch itself worked, and its confirmation is the
// message the user needs to see.
func (a *App) recordRecentRoot(path string) {
	if a.configPath == "" || path == "" {
		return
	}
	cfg, err := config.Load(a.configPath)
	if err != nil {
		return
	}
	if len(cfg.RecentRoots) > 0 && cfg.RecentRoots[0] == path {
		return
	}
	cfg.AddRecentRoot(path)
	if err := config.Save(a.configPath, cfg); err != nil {
		review.Logf("recent roots: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Keyboard
// -----------------------------------------------------------------------------

// handleRootPickerKey routes keyboard input while the picker owns the
// screen. Esc / Enter / Tab / Up / Down are the picker's; everything else
// falls through to editRunes, which is the same text field the prompt modal
// and the review composer use.
func (a *App) handleRootPickerKey(ev *tcell.EventKey) {
	st := &a.rootPicker
	switch ev.Key() {
	case tcell.KeyEsc:
		a.escRootPicker()
		return
	case tcell.KeyEnter:
		a.rootPickerActivate()
		return
	case tcell.KeyTab:
		a.rootPickerComplete()
		return
	case tcell.KeyDown:
		a.moveRootPickerSelection(1)
		return
	case tcell.KeyUp:
		a.moveRootPickerSelection(-1)
		return
	}
	before := string(st.query)
	value, cursor, handled := editRunes(st.query, st.cursor, ev)
	if !handled {
		return
	}
	st.query = value
	st.cursor = cursor
	if string(st.query) != before {
		a.refreshRootPickerRows()
	}
}

// moveRootPickerSelection walks the highlight by delta and keeps it in
// view. In browse mode, moving up off the first row lands back on "nothing
// highlighted" rather than sticking at row 0 — that is the keyboard route
// to picking the folder you are standing in, and without it a directory
// with children could only be chosen with the mouse.
func (a *App) moveRootPickerSelection(delta int) {
	st := &a.rootPicker
	if len(st.rows) == 0 {
		st.selected = rootPickerNoSelection
		return
	}
	next := st.selected + delta
	if st.selected == rootPickerNoSelection && delta > 0 {
		next = 0
	}
	if next < 0 {
		if st.browse {
			st.selected = rootPickerNoSelection
			st.listScroll = 0
			return
		}
		next = 0
	}
	if next > len(st.rows)-1 {
		next = len(st.rows) - 1
	}
	st.selected = next
	a.clampRootPickerScroll()
}

// clampRootPickerScroll slides the visible window so the highlighted row is
// inside it. The recents list never needs this (ten rows, ten slots), but a
// browsed directory can hold hundreds.
func (a *App) clampRootPickerScroll() {
	st := &a.rootPicker
	if st.selected < 0 {
		st.listScroll = 0
		return
	}
	if st.selected < st.listScroll {
		st.listScroll = st.selected
	}
	if st.selected >= st.listScroll+rootPickerRowsVisible {
		st.listScroll = st.selected - rootPickerRowsVisible + 1
	}
	max := len(st.rows) - rootPickerRowsVisible
	if max < 0 {
		max = 0
	}
	if st.listScroll > max {
		st.listScroll = max
	}
	if st.listScroll < 0 {
		st.listScroll = 0
	}
}

// -----------------------------------------------------------------------------
// Mouse
// -----------------------------------------------------------------------------

// handleRootPickerMouse routes mouse input while the picker is open: hover
// follows the pointer, a click on a row activates it, a click on "Use this
// folder" picks the browsed directory, the wheel scrolls the list, and a
// click outside dismisses.
//
// Every hit test reads the rects recorded during the draw. Nothing here
// recomputes a row's Y.
func (a *App) handleRootPickerMouse(x, y int, btn tcell.ButtonMask) {
	st := &a.rootPicker
	mx, my, mw, mh := a.rootPickerModalRect()
	inside := x >= mx && x < mx+mw && y >= my && y < my+mh

	if btn&tcell.WheelUp != 0 {
		a.scrollRootPickerList(-1)
		return
	}
	if btn&tcell.WheelDown != 0 {
		a.scrollRootPickerList(1)
		return
	}

	if idx, ok := a.rootPickerRowAt(x, y); ok {
		// Hover tracks the pointer the way the finder's does, so the user
		// can scrub the list without clicking.
		st.selected = idx
	}
	if btn&tcell.Button1 == 0 {
		return
	}
	if !inside {
		a.closeRootPicker()
		return
	}
	if st.useW > 0 && y == st.useY && x >= st.useX && x < st.useX+st.useW {
		a.pickRoot(string(st.query))
		return
	}
	if idx, ok := a.rootPickerRowAt(x, y); ok {
		st.selected = idx
		a.rootPickerActivate()
	}
}

// rootPickerRowAt maps a screen point to a row index using the draw-time
// snapshot. Returns ok=false for anything that isn't a drawn row.
func (a *App) rootPickerRowAt(x, y int) (int, bool) {
	mx, _, mw, _ := a.rootPickerModalRect()
	if x < mx || x >= mx+mw {
		return 0, false
	}
	for _, r := range a.rootPicker.rowRects {
		if r.y == y {
			return r.index, true
		}
	}
	return 0, false
}

// scrollRootPickerList moves the visible window by delta rows without
// moving the highlight — the wheel is for looking, the click is for
// choosing.
func (a *App) scrollRootPickerList(delta int) {
	st := &a.rootPicker
	max := len(st.rows) - rootPickerRowsVisible
	if max < 0 {
		max = 0
	}
	st.listScroll += delta
	if st.listScroll > max {
		st.listScroll = max
	}
	if st.listScroll < 0 {
		st.listScroll = 0
	}
}

// -----------------------------------------------------------------------------
// Draw
// -----------------------------------------------------------------------------

// rootPickerModalRect returns the modal's on-screen rectangle. Same
// proportions and upper-third anchor as the finder so the two pickers land
// in the same place on screen.
//
// Layout: 1 border + 1 title + 1 divider + 1 input + N rows + 1 footer +
// 1 border = N+6 rows.
func (a *App) rootPickerModalRect() (x, y, w, h int) {
	w = rootPickerMaxWidth
	if w > a.width-4 {
		w = a.width - 4
	}
	if w < 30 {
		w = 30
	}
	h = rootPickerRowsVisible + 6
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

// drawRootPicker paints the modal and records the hit-test rects the click
// handler reads.
//
// Layout (relY):
//
//	0     top border
//	1     title — "Change root" + the browsed directory + "esc"
//	2     divider
//	3     input
//	4..N  rows
//	N+1   footer — the browse-mode button, or a mode hint
//	N+2   bottom border
func (a *App) drawRootPicker() {
	st := &a.rootPicker
	st.rowRects = nil
	st.useX, st.useY, st.useW = 0, 0, 0

	mx, my, mw, mh := a.rootPickerModalRect()
	bg := a.theme.LineHL
	bgStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text)
	borderStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Subtle)
	titleStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Accent).Bold(true)
	mutedStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Muted)
	hitStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.FindCurrent).Bold(true)

	fillRect(a.screen, mx, my, mw, mh, bgStyle)
	drawBorder(a.screen, mx, my, mw, mh, borderStyle)
	drawHDivider(a.screen, mx, my+2, mw, borderStyle)

	// Title, then the directory browse mode is standing in. The path is
	// the header's real content in browse mode — it is how the user knows
	// what Enter-with-nothing-highlighted would pick.
	title := " Change root"
	drawAt(a.screen, mx+1, my+1, title, titleStyle)
	hint := "esc "
	drawAt(a.screen, mx+mw-1-runeLen(hint), my+1, hint, mutedStyle)
	if st.browse && st.dir != "" {
		where := displayPath(st.dir)
		avail := mw - 2 - runeLen(title) - 2 - runeLen(hint)
		if avail > 3 {
			drawAt(a.screen, mx+1+runeLen(title)+1, my+1, truncateLeft(where, avail), mutedStyle)
		}
	}

	// Input row — the finder's field, including its horizontal scroll.
	inputStyle := tcell.StyleDefault.Background(a.theme.BG).Foreground(a.theme.Text)
	fieldStart := mx + 3
	fieldEnd := mx + mw - 4
	fieldWidth := fieldEnd - fieldStart
	st.scroll = scrollWindow(st.cursor, st.scroll, fieldWidth)
	for cx := fieldStart - 1; cx <= fieldEnd; cx++ {
		a.screen.SetContent(cx, my+3, ' ', nil, inputStyle)
	}
	for i := 0; i < fieldWidth; i++ {
		idx := st.scroll + i
		if idx >= len(st.query) {
			break
		}
		a.screen.SetContent(fieldStart+i, my+3, st.query[idx], nil, inputStyle)
	}
	caret := fieldStart + (st.cursor - st.scroll)
	if caret >= fieldStart && caret <= fieldEnd {
		a.screen.ShowCursor(caret, my+3)
	}

	// Rows.
	rowsStart := my + 4
	rowsCap := mh - 6
	if rowsCap > rootPickerRowsVisible {
		rowsCap = rootPickerRowsVisible
	}
	for i := 0; i < rowsCap; i++ {
		ry := rowsStart + i
		idx := st.listScroll + i
		if idx >= len(st.rows) {
			for cx := mx + 1; cx < mx+mw-1; cx++ {
				a.screen.SetContent(cx, ry, ' ', nil, bgStyle)
			}
			continue
		}
		st.rowRects = append(st.rowRects, rootPickerRowRect{y: ry, index: idx})
		a.drawRootPickerRow(mx, ry, mw, st.rows[idx], idx == st.selected, hitStyle, bg)
	}

	a.drawRootPickerFooter(mx, my+mh-2, mw, bg, mutedStyle)
}

// drawRootPickerRow paints one list row, with the row background flipped on
// the highlighted one and the fuzzy scorer's matched runes lit. Mirrors
// drawFinderRow so the two lists read identically, with one deliberate
// difference: a row too long for the modal is clipped from the LEFT, not the
// right.
//
// That difference is the whole point of the row. These are paths, and the
// deepest segment — "vincent", "sarita" — is what identifies the folder;
// clipping the right end (which is what the finder does, because a filename
// starts at its left) leaves ten rows of identical temp-directory prefix and
// nothing to tell them apart. The matched-rune highlights are shifted to
// follow the clip so the scorer's hits stay on the characters they scored.
func (a *App) drawRootPickerRow(mx, ry, mw int, row rootPickerRow, selected bool, hitStyle tcell.Style, modalBG tcell.Color) {
	rowBG := modalBG
	if selected {
		rowBG = a.theme.BG
	}
	rowStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.Text)
	hitOnRow := hitStyle.Background(rowBG)
	dirStyle := tcell.StyleDefault.Background(rowBG).Foreground(a.theme.Accent)

	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, ry, ' ', nil, rowStyle)
	}

	matchSet := make(map[int]bool, len(row.matched))
	for _, m := range row.matched {
		matchSet[m] = true
	}
	base := rowStyle
	if row.isDir {
		// Browse-mode rows are folders, and colouring them the accent is
		// the cheapest way to say so without a glyph the terminal might
		// not have.
		base = dirStyle
	}
	startCol := mx + 2
	maxCols := mw - 4
	if maxCols <= 0 {
		return
	}
	runes := []rune(row.label)
	// shift maps a drawn column back to its index in row.label, so
	// matchSet still lines up after a left clip. clipped marks column 0 as
	// the ellipsis, which is never a match however the arithmetic lands.
	shift, clipped := 0, false
	if len(runes) > maxCols && maxCols > 1 {
		start := len(runes) - (maxCols - 1)
		trimmed := make([]rune, 0, maxCols)
		trimmed = append(trimmed, '…')
		trimmed = append(trimmed, runes[start:]...)
		runes = trimmed
		shift = start - 1
		clipped = true
	}
	for i, ch := range runes {
		if i >= maxCols {
			break
		}
		st := base
		if !(clipped && i == 0) && matchSet[i+shift] {
			st = hitOnRow
		}
		a.screen.SetContent(startCol+i, ry, ch, nil, st)
	}
}

// drawRootPickerFooter paints the row under the list and records the "Use
// this folder" button's rect when it draws one.
//
// The button is browse mode's mouse path to picking the directory being
// listed. Without it a mouse-only user could descend forever and never
// choose, because a click on a row means "go in" — and mouse-first is a
// non-negotiable here, not a nicety.
func (a *App) drawRootPickerFooter(mx, fy, mw int, bg tcell.Color, mutedStyle tcell.Style) {
	st := &a.rootPicker
	bgStyle := tcell.StyleDefault.Background(bg).Foreground(a.theme.Text)
	for cx := mx + 1; cx < mx+mw-1; cx++ {
		a.screen.SetContent(cx, fy, ' ', nil, bgStyle)
	}
	if !st.browse {
		msg := " type / or ~ to browse the machine"
		if len(st.rows) == 0 {
			msg = " no recent folders — type / or ~ to browse"
		}
		drawAt(a.screen, mx+1, fy, truncateRight(msg, mw-2), mutedStyle)
		return
	}
	label := "[ Use this folder ]"
	bx := mx + mw - 1 - runeLen(label) - 1
	if bx < mx+1 {
		bx = mx + 1
	}
	focused := st.selected == rootPickerNoSelection
	drawButton(a.screen, bx, fy, label, bg, a.theme.Accent, focused)
	st.useX, st.useY, st.useW = bx, fy, runeLen(label)
	tabHint := " tab completes · enter descends"
	if runeLen(tabHint) < bx-mx-1 {
		drawAt(a.screen, mx+1, fy, tabHint, mutedStyle)
	}
}

// truncateRight clips s to at most w runes off the end. Plain clip, no
// ellipsis: it is only used on the footer hints, where the tail is the
// least important part. Paths use gitpanel.go's truncateLeft instead —
// there the deepest segment is the informative end.
func truncateRight(s string, w int) string {
	if w <= 0 {
		return ""
	}
	out := []rune(s)
	if len(out) <= w {
		return s
	}
	return string(out[:w])
}

// -----------------------------------------------------------------------------
// Tree context-menu entries
// -----------------------------------------------------------------------------

// ctxChangeRoot is the tree context menu's "Change root…" row. It opens the
// picker in recents mode regardless of which node was right-clicked — the
// gesture is "take me somewhere else", and the node is incidental.
//
// The Esc-o leader is the redundant keyboard path this needs under the
// house rule that no action lives only behind a right-click.
func ctxChangeRoot(a *App, _ *filetree.Node) {
	a.openRootPicker()
}

// ctxSetAsRoot is the folder-only "Set as root" row: the one-click version
// of the whole picker, for the case the user can already see the folder
// they want in the tree.
func ctxSetAsRoot(a *App, n *filetree.Node) {
	if n == nil || !n.IsDir {
		return
	}
	a.setRoot(n.Path)
}
