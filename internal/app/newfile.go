// =============================================================================
// File: internal/app/newfile.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// New file for Vincent's phase 6b. Not a port of anything: spice-edit's
// create-file half of internal/app/fileops.go (deleted here — see
// CLAUDE.md) deliberately refused to create parent directories. Vincent's
// New File does the opposite on purpose — an agent-reviewing tool commonly
// wants a file inside a subdirectory that doesn't exist yet — while
// keeping the same never-clobber, never-rename, never-delete boundary the
// rest of the file actions hold to.
// =============================================================================

package app

import (
	"fmt"
	"os"
	"path/filepath"
)

// menuNewFile opens the Esc-n prompt asking for a path to create. The
// prompt's hint names the directory a bare filename will land in — the
// active folder when there is one (the directory of whatever's open, or
// whatever's selected in the tree), the project root otherwise.
func (a *App) menuNewFile() {
	dir := a.activeFolder
	if dir == "" {
		dir = a.rootDir
	}
	a.openPrompt("New file", "relative to "+dir, "", func(a *App, value string) {
		a.createNewFile(dir, value)
	})
}

// resolveNewFilePath turns a user-typed value into an absolute path. An
// absolute value is used as-is (Clean'd); anything else is resolved
// against base. Shared with Save As, which faces the identical "relative
// or absolute, either is fine" input.
func resolveNewFilePath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

// createNewFile is the New File action proper: resolve value against
// base, create any missing parent directories, create the file empty,
// open it as a text tab, and refresh the tree/git-status/finder so the
// sidebar shows it immediately rather than waiting on the ten-second
// poller.
//
// A target that already exists is not an error — this flow never
// clobbers, renames, or deletes anything, so hitting an existing path
// just opens it, exactly as if the user had clicked it in the tree.
func (a *App) createNewFile(base, value string) {
	target := resolveNewFilePath(base, value)
	if _, err := os.Stat(target); err == nil {
		// openFile flashes its own "Opened ..." message; ours replaces it
		// so the final status line is the more useful "already exists"
		// one, not a message that reads like a brand-new file was made.
		a.openFile(target)
		a.flash(fmt.Sprintf("%s already exists, opening it", filepath.Base(target)))
		return
	}
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		a.flash(fmt.Sprintf("Create failed: %v", err))
		return
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		a.flash(fmt.Sprintf("Create failed: %v", err))
		return
	}
	f.Close()
	// Refresh before opening: openFile calls tree.Reveal(target, ...),
	// which can only find the row if the tree already knows the node
	// exists.
	a.refreshTree()
	a.refreshGitStatus()
	a.invalidateFinder()
	a.openFile(target)
	a.flash(fmt.Sprintf("Created %s", filepath.Base(target)))
}
