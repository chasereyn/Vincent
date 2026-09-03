// =============================================================================
// File: internal/app/saveas.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// New file for Vincent's phase 6b. spice-edit has no Save As at all, so
// there is nothing upstream to derive this from — see CLAUDE.md.
// =============================================================================

package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// menuSaveAs opens the Esc-S prompt, prefilled with the active tab's
// current path, for writing its buffer to a different location. Refused
// on a read-only tab with a flash — a diff carries the real file's Path,
// so "saving" one would put diff text over the user's source, the same
// reason every other mutator on Tab checks ReadOnly().
func (a *App) menuSaveAs() {
	tab := a.activeTabPtr()
	if tab == nil {
		return
	}
	if tab.ReadOnly() {
		a.flash("Can't save a read-only tab")
		return
	}
	current := tab.Path
	if current == "" {
		current = a.activeFolder
		if current == "" {
			current = a.rootDir
		}
	}
	a.openPrompt("Save as", "path to write", current, func(a *App, value string) {
		a.saveActiveTabAs(value)
	})
}

// saveActiveTabAs resolves the prompt's value against the tab's own
// directory (or the active folder, for an untitled tab) and, if the
// resolved target already exists, asks before overwriting it — Save As
// never clobbers silently. idx is captured up front so the confirm
// callback (which runs after the prompt/confirm modals have cycled) still
// targets the right tab even if the active tab somehow changed in
// between.
func (a *App) saveActiveTabAs(value string) {
	idx := a.activeTab
	tab := a.activeTabPtr()
	if tab == nil || tab.ReadOnly() {
		return
	}
	base := filepath.Dir(tab.Path)
	if tab.Path == "" {
		base = a.activeFolder
		if base == "" {
			base = a.rootDir
		}
	}
	target := resolveNewFilePath(base, value)
	if _, err := os.Stat(target); err == nil {
		a.openConfirm("Save as", fmt.Sprintf("%s already exists. Overwrite?", filepath.Base(target)), func(a *App) {
			a.writeTabAs(idx, target)
		})
		return
	}
	a.writeTabAs(idx, target)
}

// writeTabAs performs the write: Tab.SaveAs handles retargeting Path,
// Title, Mtime and the undo/conflict snapshot to the new file and marking
// it clean; this just wraps that in the UI refresh (tree, git status,
// finder, reveal) every other file-touching action does, and a status
// flash.
func (a *App) writeTabAs(idx int, target string) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	tab := a.tabs[idx]
	if err := tab.SaveAs(target); err != nil {
		a.flash(fmt.Sprintf("Save failed: %v", err))
		return
	}
	a.refreshTree()
	a.refreshGitStatus()
	a.invalidateFinder()
	a.revealTreePath(target)
	a.setActiveFolder(filepath.Dir(target))
	if idx == a.activeTab {
		a.syncActiveTreeFile()
	}
	a.flash(fmt.Sprintf("Saved as %s", filepath.Base(target)))
}

// revealTreePath expands the tree to path's location and scrolls it into
// view. Shared by Save As and (via openFile) every other place a file is
// opened, so a freshly written file is never left un-scrolled-to in a
// collapsed sidebar. listH mirrors the sidebar's own list-area height —
// see the identical computation in openFile.
func (a *App) revealTreePath(path string) {
	if a.tree == nil {
		return
	}
	a.tree.ActiveFile = path
	_, _, _, sh := a.sidebarRect()
	listH := sh - 2
	if listH < 0 {
		listH = 0
	}
	a.tree.Reveal(path, listH)
}
