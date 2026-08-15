// =============================================================================
// File: internal/app/pathops.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Derived from internal/app/fileops.go in spice-edit
// (Copyright 2026 Cloudmanic, LLC, MIT licensed). The create / rename /
// delete half of that file was dropped when Vincent became read-only;
// what survives here is the path-to-clipboard family, which a reviewer
// uses constantly to paste a path into an agent conversation.
// =============================================================================

package app

import (
	"fmt"
	"path/filepath"

	"github.com/chasereyn/vincent/internal/clipboard"
	"github.com/chasereyn/vincent/internal/filetree"
)

// copyPathToSystemClipboard pushes path onto the host system clipboard via
// OSC 52 and flashes a confirmation (or the underlying error) so the user
// gets feedback — the OS clipboard is invisible from inside the TUI, and a
// silent action would leave the user wondering whether anything happened.
//
// label is the short word used in the success / error flash ("relative
// path" / "absolute path") so both menu paths share one helper without
// duplicating copy.
func (a *App) copyPathToSystemClipboard(path, label string) {
	if err := clipboard.CopyToSystem(path); err != nil {
		a.flash(fmt.Sprintf("Copy failed: %v", err))
		return
	}
	a.flash(fmt.Sprintf("Copied %s: %s", label, path))
}

// relativePathFor returns path rendered relative to the project root. We
// resolve the root via tree.Root.Path because that is always absolute —
// App.rootDir keeps the user-supplied string verbatim ("." in the common
// case), and filepath.Rel refuses to mix a relative base with an absolute
// target. Tab and tree node paths are always absolute, so basing on the
// absolute root is what gives a clean repo-relative result.
//
// On the rare error path (different volume, etc.) we fall back to the
// absolute path so the user still gets something useful on the clipboard.
func (a *App) relativePathFor(path string) string {
	base := a.rootDir
	if a.tree != nil && a.tree.Root != nil {
		base = a.tree.Root.Path
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

// absolutePathFor resolves path to an absolute filesystem path. Failures
// fall back to the input unchanged — Tab.Path and tree node paths are
// already absolute in normal use, so this is just defence-in-depth.
func absolutePathFor(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// menuCopyRelativePath copies the active tab's path, rendered relative to
// the project root, onto the host system clipboard via OSC 52. Works the
// same locally and over SSH — the terminal emulator is the thing that
// actually receives the clipboard write, regardless of where the editor
// is running.
func (a *App) menuCopyRelativePath() {
	a.closeMenu()
	tab := a.activeTabPtr()
	if tab == nil || tab.Path == "" {
		return
	}
	a.copyPathToSystemClipboard(a.relativePathFor(tab.Path), "relative path")
}

// menuCopyAbsolutePath copies the active tab's absolute path onto the host
// system clipboard. Useful when pasting the path into a shell on the same
// remote machine (e.g. another pane running the agent).
func (a *App) menuCopyAbsolutePath() {
	a.closeMenu()
	tab := a.activeTabPtr()
	if tab == nil || tab.Path == "" {
		return
	}
	a.copyPathToSystemClipboard(absolutePathFor(tab.Path), "absolute path")
}

// ctxCopyRelativePath copies n's path (relative to the project root) onto
// the host system clipboard. Tree-context counterpart to menuCopyRelativePath.
func ctxCopyRelativePath(a *App, n *filetree.Node) {
	a.copyPathToSystemClipboard(a.relativePathFor(n.Path), "relative path")
}

// ctxCopyAbsolutePath copies n's absolute path onto the host system
// clipboard. Tree-context counterpart to menuCopyAbsolutePath.
func ctxCopyAbsolutePath(a *App, n *filetree.Node) {
	a.copyPathToSystemClipboard(absolutePathFor(n.Path), "absolute path")
}
