// =============================================================================
// File: internal/app/markdownview.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// markdownview.go is the app-side half of Vincent's phase 7 markdown
// viewer: the Esc-m leader entry point, and keeping a rendered tab fresh
// while an agent keeps writing to the file underneath — the markdown
// counterpart to diffview.go's reconcileDiffTab.
//
// Opening a .md/.markdown file at all is NOT here: editor.NewTab already
// dispatches to editor.NewMarkdownTab for those extensions (mirroring how
// it already dispatches image extensions to newImageTab), so openFile in
// app.go needs no changes to open one rendered by default.

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chasereyn/vincent/internal/editor"
)

// menuToggleMarkdownView is Esc m: swap the active tab between its
// rendered markdown view and the raw editable text view. Tab itself is
// the one that knows what counts as markdown, so this is a thin
// dispatcher — a nil or non-markdown tab is a silent no-op, same as
// every other leader action's own precondition handling.
func (a *App) menuToggleMarkdownView() {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	tab.ToggleMarkdownView()
}

// reconcileMarkdownTab re-reads and re-renders a rendered markdown tab
// when the file underneath it changes on disk — the same "the agent
// rewrote it" trigger reconcileDiffTab reacts to, off the exact same poll
// result. A markdown tab carries no dirty state of its own (it is
// read-only), so unlike a plain text tab there is no conflict to detect:
// the disk version always wins, the way a diff tab's does.
//
// A raw-mode tab on a .md path (Mode == "", ReadOnly false) is NOT this
// function's concern — it's an ordinary text tab at that point, and
// reconcileOpenTabsWithDisk's normal dirty/conflict path already handles
// it correctly.
func (a *App) reconcileMarkdownTab(t *editor.Tab, polled gitPollFile) {
	switch {
	case polled.statErr:
		return // transient stat error; try again next tick.
	case polled.missing:
		if t.DiskGone {
			return // already reconciled this deletion.
		}
		t.DiskGone = true
		a.flash(fmt.Sprintf("%s deleted on disk", filepath.Base(t.Path)))
		return
	default:
		if !polled.mtime.After(t.Mtime) {
			return // unchanged on disk.
		}
	}
	t.DiskGone = false
	t.Mtime = polled.mtime
	data, err := os.ReadFile(t.Path)
	if err != nil {
		return // transient read error; try again next tick.
	}
	t.SetMarkdownSource(string(data))
	// No flash on success, matching reconcileDiffTab: a clean rendered
	// tab picking up the agent's latest write is the normal case in this
	// workflow, not something worth announcing every time it happens.
}
