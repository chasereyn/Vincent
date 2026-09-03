// =============================================================================
// File: internal/app/leader.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// leader.go defines Vincent's Esc-leader hotkey table. A single Esc arms
// the leader; one rune within doubleEscMs fires the bound action; a second
// Esc, or any unbound key, cancels. We deliberately avoid Ctrl-key
// shortcuts because they fight tmux/zellij prefixes and the terminal's own
// bindings — Esc is the only modifier we trust over SSH.
//
// Reworked 2026-09-02 (Chase Reynolds): f is the file panel, / is find,
// U is redo, a is select-all. r, Enter, y, o, and t belong to the review
// composer, the root switcher, and the tab bar toggle; c, P and b were
// reserved for the git writes and landed in phase 3b.
//
// Reworked again 2026-09-02, after the owner's first real session: the ≡
// action menu is gone (see cheatsheet.go) and with it the Esc m binding
// that opened it. Esc ? shows the key table instead, and the table itself
// grew two presentation fields — a short hint and a group — because the
// leader table is now the ONLY list of what Vincent's keys are. The status
// bar's armed-leader line and the cheatsheet are both generated from it,
// so a new binding cannot ship undocumented.

package app

import "github.com/gdamore/tcell/v2"

// Leader groups. These are the divider boundaries in the Esc-? cheatsheet
// and nothing else — they are never printed — so they exist to keep the
// grouping in one place rather than duplicated in the painter.
const (
	leaderGroupReview  = "Review"
	leaderGroupGit     = "Git"
	leaderGroupSearch  = "Search"
	leaderGroupView    = "View"
	leaderGroupEdit    = "Edit"
	leaderGroupSession = "Session"
)

// leaderBinding is one Esc-leader entry: the trigger rune, a short hint
// label, the group it presents under, and the App method that fires when
// the user presses Esc, <rune> in quick succession. Each method already
// handles its own preconditions — calling menuUndo with no active tab, for
// example, is a safe no-op — so the leader dispatch doesn't need to
// re-check enable predicates.
//
// hint is deliberately terse ("copy review", not "Copy review to
// clipboard"): it has to survive truncation in a one-row status bar, and
// the cheatsheet reads better as a column of short labels than as a column
// of sentences.
type leaderBinding struct {
	key    rune
	hint   string
	group  string
	action func(*App)
}

// leaderBindings is the editor's full Esc-leader table. The order is
// presentational and it matters: the status bar's armed-leader line is
// generated from this table in this order and truncated to fit, so the
// review bindings lead and the housekeeping ones trail. Letter bindings are
// chosen to be mnemonic and avoid collisions; punctuation bindings mirror
// familiar editor gestures where they make sense.
//
// Intentionally not bound:
//   - x / v (clipboard) — the host terminal's own Cmd+C/Cmd+V already
//     covers that path, a mouse selection is what you copy from, and
//     Backspace deletes a selection. A third channel just adds confusion.
//     'c' was on that list until phase 3b spent it on commit, which is the
//     same argument from the other side: the key is free precisely because
//     copy does not need it.
//   - revert / toggle line comment — destructive or fiddly enough that the
//     editor's right-click menu is a better gate than a one-key reflex.
func leaderBindings() []leaderBinding {
	return []leaderBinding{
		// Review — the reason Vincent exists, so first in the table and
		// therefore last to be truncated out of the status hint.
		{'d', "diff", leaderGroupReview, (*App).menuViewDiff},
		// 'r' is the review composer, not Redo. Vincent is a review
		// client: writing a note is the second most common thing anyone
		// does in it, and redo is inherited machinery.
		{'r', "note", leaderGroupReview, (*App).openReviewComposer},
		{'y', "copy review", leaderGroupReview, (*App).copyReview},
		{'g', "changes", leaderGroupReview, (*App).menuToggleGitPanel},
		// Git — the three blunt writes, phase 3b. 'P' is capitalised on
		// purpose: 'p' is the file finder, and pushing to a remote is not
		// a key anyone should hit reaching for it.
		{'c', "commit", leaderGroupGit, (*App).openCommitBox},
		{'P', "push", leaderGroupGit, (*App).pushBranch},
		{'b', "branch", leaderGroupGit, (*App).openBranchPicker},
		// Search
		{'p', "find file", leaderGroupSearch, (*App).openFinder},
		{'/', "find", leaderGroupSearch, (*App).openFind},
		{'F', "find in files", leaderGroupSearch, (*App).openSearch},
		// View
		{'f', "explorer", leaderGroupView, (*App).menuToggleSidebar},
		{'t', "tab bar", leaderGroupView, (*App).menuToggleTabBar},
		{'z', "fold all", leaderGroupView, (*App).menuCollapseTree},
		// 'm' toggles the active .md tab between its rendered and raw
		// views. A no-op on any other tab — see Tab.ToggleMarkdownView.
		{'m', "markdown", leaderGroupView, (*App).menuToggleMarkdownView},
		// 'o' is "open a different root" — the folder switcher. Pressed a
		// second time while the picker is up it flips to browsing the
		// filesystem, which is the two-gesture shape the owner asked for.
		{'o', "root", leaderGroupView, (*App).openRootPicker},
		// Edit
		{'s', "save", leaderGroupEdit, (*App).menuSave},
		{'S', "save as", leaderGroupEdit, (*App).menuSaveAs},
		{'n', "new file", leaderGroupEdit, (*App).menuNewFile},
		{'u', "undo", leaderGroupEdit, (*App).menuUndo},
		{'U', "redo", leaderGroupEdit, (*App).menuRedo},
		{'a', "select all", leaderGroupEdit, (*App).menuSelectAll},
		// Session
		{'w', "close", leaderGroupSession, (*App).menuClose},
		{'q', "quit", leaderGroupSession, (*App).menuQuit},
		{'?', "keys", leaderGroupSession, (*App).openCheatsheet},
	}
}

// leaderActionFor looks up the App method bound to r in the leader table,
// or returns nil when r isn't bound. Returning nil rather than a no-op
// lets the caller distinguish "leader fired" from "key was unbound — fall
// through to normal handling", which matters for typing flow: pressing
// Esc then a non-leader letter must still let that letter reach the
// editor's normal key handler.
func leaderActionFor(r rune) func(*App) {
	for _, b := range leaderBindings() {
		if b.key == r {
			return b.action
		}
	}
	return nil
}

// leaderKeyBinding is an Esc-leader entry triggered by a NAMED key rather
// than by a rune — Esc then Enter, today. A second table rather than a
// wider one because tcell reports Enter as its own key, not as '\r', and
// folding a tcell.Key into the rune table would mean every consumer
// growing a special case for the entry with no printable rune. label is
// the glyph the cheatsheet and status bar print in the rune's place.
type leaderKeyBinding struct {
	key    tcell.Key
	label  string
	hint   string
	group  string
	action func(*App)
}

// leaderKeyBindings is the named-key half of the leader table.
//
// Esc-Enter sends the review batch. Enter is the right key for it: it is
// the gesture for "commit what I have written" everywhere else in the UI,
// and the batch IS what the reviewer has written.
func leaderKeyBindings() []leaderKeyBinding {
	return []leaderKeyBinding{
		{tcell.KeyEnter, "⏎", "send", leaderGroupReview, (*App).sendReview},
	}
}

// leaderActionForKey looks up the App method bound to a named key in the
// leader table, or nil when it isn't bound. Same nil-means-fall-through
// contract as leaderActionFor.
func leaderActionForKey(k tcell.Key) func(*App) {
	for _, b := range leaderKeyBindings() {
		if b.key == k {
			return b.action
		}
	}
	return nil
}

// leaderRow is one leader binding rendered for a human: the key label the
// user presses ("d", "?", "⏎"), its short hint, and the group it sits in.
type leaderRow struct {
	group string
	key   string
	hint  string
}

// leaderRows merges the rune and named-key leader tables into one ordered,
// grouped list — the single source both the status bar's armed-leader line
// and the Esc-? cheatsheet read from. Two hand-written lists of the same
// bindings is exactly how a key table goes stale, and Vincent no longer
// has a menu to fall back on when it does.
//
// Groups appear in the order they first show up in the rune table, and
// every binding of a group is gathered under it. That lets the named-key
// table (Esc-Enter) present inside the Review group without either table
// having to be ordered around the other.
func leaderRows() []leaderRow {
	var order []string
	byGroup := map[string][]leaderRow{}
	add := func(group, key, hint string) {
		if _, seen := byGroup[group]; !seen {
			order = append(order, group)
		}
		byGroup[group] = append(byGroup[group], leaderRow{group: group, key: key, hint: hint})
	}
	for _, b := range leaderBindings() {
		add(b.group, string(b.key), b.hint)
	}
	for _, b := range leaderKeyBindings() {
		add(b.group, b.label, b.hint)
	}
	out := make([]leaderRow, 0, len(order))
	for _, g := range order {
		out = append(out, byGroup[g]...)
	}
	return out
}
