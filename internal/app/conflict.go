// =============================================================================
// File: internal/app/conflict.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream equivalent. spice-edit warned that a save would overwrite an
// external change and then let it happen; this file is the prompt that
// stands in the way.
// =============================================================================

// conflict.go is what happens when a save runs into a file that changed on
// disk. In Vincent's workflow that is not an accident — an agent rewriting
// the file you are correcting is the normal case — so the model is "refuse
// and show me", never "merge and hope":
//
//   - Overwrite   keep my buffer, lose the version on disk.
//   - Reload      keep the version on disk, lose my edits.
//   - Show diff   compare the two before deciding.
//   - Cancel      go away and leave both alone.
//
// Show diff is the reason this is worth building rather than borrowing
// Zed's two-button prompt. The user's actual question at that moment is
// "did the agent touch the lines I touched?", and Vincent already has a
// diff parser and a diff renderer in-process, so answering it costs one
// temp file and one git shell-out.
//
// The prompt reuses the dirty-close modal's button machinery (see
// modals.go) rather than introducing a second modal: same keyboard
// routing, same hit-testing, same painter, different labels.

package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chasereyn/vincent/internal/diff"
	"github.com/chasereyn/vincent/internal/editor"
)

// openConflictPrompt puts the Overwrite / Reload / Show diff / Cancel
// decision in front of the user for the tab at idx. Called from saveTabAt,
// which refuses to write a conflicted tab.
//
// Cancel sits first because that is where focus starts: of the four, it is
// the only one that cannot lose work.
func (a *App) openConflictPrompt(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	name := filepath.Base(a.tabs[idx].Path)
	a.openDirtyButtons(
		"Changed on disk",
		name+" changed on disk since you opened it.",
		[]dirtyButton{
			{label: "Cancel", tone: dirtyToneNeutral},
			{label: "Show diff", tone: dirtyToneAccent, action: func(app *App) {
				app.openBufferVsDisk(idx)
			}},
			{label: "Reload", tone: dirtyToneDanger, action: func(app *App) {
				app.conflictReload(idx)
			}},
			{label: "Overwrite", tone: dirtyToneDanger, action: func(app *App) {
				app.conflictOverwrite(idx)
			}},
		},
	)
}

// conflictOverwrite writes the buffer over the on-disk version and clears
// the conflict. It goes through SaveOverwrite rather than Save because Save
// is the method that refuses — the explicit call is the record that the
// user chose this.
func (a *App) conflictOverwrite(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	tab := a.tabs[idx]
	if err := tab.SaveOverwrite(); err != nil {
		a.flash(fmt.Sprintf("Save failed: %v", err))
		return
	}
	a.refreshGitStatus()
	a.flash(fmt.Sprintf("Overwrote %s with your version", filepath.Base(tab.Path)))
}

// conflictReload takes the on-disk version and drops the user's edits.
// Tab.Reload resets the undo stacks, so there is no getting them back —
// which is correct for a reload the user asked for by name, and is why the
// flash says so out loud.
func (a *App) conflictReload(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	tab := a.tabs[idx]
	if err := tab.Reload(); err != nil {
		a.flash(fmt.Sprintf("Reload failed: %v", err))
		return
	}
	a.flash(fmt.Sprintf("Reloaded %s from disk — your edits were discarded",
		filepath.Base(tab.Path)))
}

// openBufferVsDisk opens a diff of the unsaved buffer against what is on
// disk, so the user can see whether the agent's write and their edits even
// touch the same lines before choosing between them.
//
// The tab is marked DiffFrozen: it is not the file's git diff, and the
// reconcile loop must not re-run git over it and silently swap the
// comparison out from under the reviewer.
func (a *App) openBufferVsDisk(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	tab := a.tabs[idx]
	if tab.Path == "" || tab.Buffer == nil {
		return
	}
	rows, ok := bufferVsDiskRows(a.rootDir, tab.Path, tab.Buffer.String())
	if !ok {
		a.flash(fmt.Sprintf("%s matches the version on disk", filepath.Base(tab.Path)))
		return
	}

	title := filepath.Base(tab.Path) + " (buffer vs disk)"
	for i, t := range a.tabs {
		if t.IsDiff() && t.DiffFrozen && t.Path == tab.Path {
			t.SetDiffRows(rows)
			a.activeTab = i
			return
		}
	}
	t := editor.NewDiffTab(tab.Path, rows)
	t.Title = title
	t.DiffFrozen = true
	_, h := a.editorSize()
	t.ScrollToRow(diff.FirstChangedRow(rows), h)
	a.tabs = append(a.tabs, t)
	a.activeTab = len(a.tabs) - 1
}

// bufferVsDiskRows diffs buffer (the in-memory text) against the file at
// path, returning parsed rows and whether the two differ at all.
//
// The buffer goes to a temp file under os.TempDir() so git can diff two
// real paths — the same `git diff --no-index` shell-out diffview.go already
// uses for untracked files, so there is one diff code path and not two.
// The temp file is removed as soon as git has read it: it exists for the
// length of one shell-out and holding on to a copy of unsaved work is not
// something to do quietly.
//
// The temp copy keeps the file's basename inside a temp DIRECTORY rather
// than being a mangled temp filename, so the diff's --- / +++ header rows
// read as the file the user is looking at.
func bufferVsDiskRows(rootDir, path, buffer string) ([]diff.Row, bool) {
	if path == "" {
		return nil, false
	}
	dir, err := os.MkdirTemp("", "vincent-buffer-")
	if err != nil {
		return nil, false
	}
	defer os.RemoveAll(dir)

	tmp := filepath.Join(dir, filepath.Base(path))
	if err := os.WriteFile(tmp, []byte(buffer), 0o600); err != nil {
		return nil, false
	}
	// tmp first, path second: additions are what the disk has and the
	// buffer does not (the agent's write), deletions are the user's edits.
	// That is the order "buffer vs disk" in the tab title promises.
	out := gitDiffOutput(rootDir, "diff", "--no-index", "--", tmp, path)
	if out == "" {
		return nil, false
	}
	return diff.Parse(out), true
}
