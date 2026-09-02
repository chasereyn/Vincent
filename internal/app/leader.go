// =============================================================================
// File: internal/app/leader.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// leader.go defines Vincent's Esc-leader hotkey table. A single Esc arms
// the leader; one rune within doubleEscMs fires the bound action; a second
// Esc, or any unbound key, cancels. There is no Esc-Esc double-tap any more
// (it used to open the menu — that is Esc m or a click on ≡ now), because
// the person using Vincent never presses Esc by accident and the double-tap
// cost a keystroke on every menu open. We deliberately avoid Ctrl-key
// shortcuts because they fight tmux/zellij prefixes and the terminal's own
// bindings — Esc is the only modifier we trust over SSH.
//
// Reworked 2026-09-02 (Chase Reynolds): f is the file panel, / is find,
// U is redo, a is select-all, m is the menu. r, Enter, y, o, b, c, P, and t
// are reserved for the review composer, git writes, and the tab bar toggle.

package app

// leaderBinding is one Esc-leader entry: the trigger rune and the App method
// that fires when the user presses Esc, <rune> in quick succession. Each method
// already handles its own preconditions — calling menuUndo with no active tab,
// for example, is a safe no-op — so the leader dispatch doesn't need to
// re-check enable predicates.
type leaderBinding struct {
	key    rune
	action func(*App)
}

// leaderBindings is the editor's full Esc-leader table. The order is purely
// presentational: tests iterate it to assert every binding fires, and a
// future help screen can render the table directly. Letter bindings are
// chosen to be mnemonic and avoid collisions; punctuation bindings mirror
// familiar editor gestures where they make sense.
//
// Intentionally not bound:
//   - c / x / v (clipboard) — the host terminal's Cmd+C/V already covers
//     that path; adding a third channel just adds confusion.
//   - rename / delete / revert — destructive enough that we want the
//     menu's confirm dialog to gate the action as a deliberate gesture.
func leaderBindings() []leaderBinding {
	return []leaderBinding{
		{'s', (*App).menuSave},
		{'u', (*App).menuUndo},
		{'U', (*App).menuRedo},
		{'a', (*App).menuSelectAll},
		{'w', (*App).menuClose},
		{'q', (*App).menuQuit},
		{'f', (*App).menuToggleSidebar},
		{'/', (*App).openFind},
		{'p', (*App).openFinder},
		{'d', (*App).menuViewDiff},
		{'g', (*App).menuToggleGitPanel},
		{'m', (*App).openMenu},
		{'t', (*App).menuToggleTabBar},
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
