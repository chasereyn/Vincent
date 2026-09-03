// =============================================================================
// File: internal/app/newfile_test.go
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveNewFilePath_Relative resolves a bare filename against base.
func TestResolveNewFilePath_Relative(t *testing.T) {
	base := filepath.Join(string(os.PathSeparator), "repo", "src")
	got := resolveNewFilePath(base, "foo.go")
	want := filepath.Join(base, "foo.go")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestResolveNewFilePath_RelativeWithSubdir resolves a value that itself
// contains path separators against base, landing in a subdirectory.
func TestResolveNewFilePath_RelativeWithSubdir(t *testing.T) {
	base := filepath.Join(string(os.PathSeparator), "repo", "src")
	got := resolveNewFilePath(base, filepath.Join("pkg", "foo.go"))
	want := filepath.Join(base, "pkg", "foo.go")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestResolveNewFilePath_AbsoluteIgnoresBase pins the "absolute paths
// accepted too" contract: an absolute value is used as-is regardless of
// base.
func TestResolveNewFilePath_AbsoluteIgnoresBase(t *testing.T) {
	base := filepath.Join(string(os.PathSeparator), "repo", "src")
	abs := filepath.Join(string(os.PathSeparator), "elsewhere", "foo.go")
	got := resolveNewFilePath(base, abs)
	if got != filepath.Clean(abs) {
		t.Fatalf("got %q, want %q", got, filepath.Clean(abs))
	}
}

// TestMenuNewFile_OpensPromptAgainstActiveFolder checks the prompt is
// wired to whatever the active folder is, not always the project root.
func TestMenuNewFile_OpensPromptAgainstActiveFolder(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	a := newTestApp(t, dir)
	a.setActiveFolder(sub)

	a.menuNewFile()
	if !a.promptOpen {
		t.Fatal("menuNewFile should open the prompt")
	}
	if !strings.Contains(a.promptHint, sub) {
		t.Fatalf("prompt hint = %q, want it to mention %q", a.promptHint, sub)
	}
}

// TestCreateNewFile_CreatesEmptyFileAndOpensTab is the headline path: a
// bare relative name creates an empty file next to base and opens it.
func TestCreateNewFile_CreatesEmptyFileAndOpensTab(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t, dir)

	a.createNewFile(dir, "new.txt")

	target := filepath.Join(dir, "new.txt")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("file was not created: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("new file should be empty, got %d bytes", info.Size())
	}
	tab := a.activeTabPtr()
	if tab == nil || tab.Path != target {
		t.Fatalf("new file should be open as the active tab, got %+v", tab)
	}
}

// TestCreateNewFile_CreatesMissingParentDirectories pins the deliberate
// divergence from spice-edit's fileops.go, which refused to create
// parent directories. Vincent's New File does create them — see
// newfile.go's header comment.
func TestCreateNewFile_CreatesMissingParentDirectories(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t, dir)

	a.createNewFile(dir, filepath.Join("a", "b", "c.txt"))

	target := filepath.Join(dir, "a", "b", "c.txt")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("file (and its parents) should have been created: %v", err)
	}
}

// TestCreateNewFile_ExistingFileOpensInsteadOfClobbering pins the never-
// clobber contract: pointing New File at a path that already has content
// must not touch that content, and just opens it.
func TestCreateNewFile_ExistingFileOpensInsteadOfClobbering(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(target, []byte("do not touch"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := newTestApp(t, dir)

	a.createNewFile(dir, "existing.txt")

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "do not touch" {
		t.Fatalf("existing content was clobbered: %q", got)
	}
	tab := a.activeTabPtr()
	if tab == nil || tab.Path != target {
		t.Fatalf("the existing file should have been opened, got %+v", tab)
	}
	if !strings.Contains(a.statusMsg, "already exists") {
		t.Fatalf("status flash = %q, want it to mention the file already existing", a.statusMsg)
	}
}

// TestCreateNewFile_AbsolutePathIgnoresBase checks that an absolute value
// typed into the prompt is honored verbatim, not joined onto base.
func TestCreateNewFile_AbsolutePathIgnoresBase(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	a := newTestApp(t, dir)

	target := filepath.Join(elsewhere, "abs.txt")
	a.createNewFile(dir, target)

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("absolute target should have been created: %v", err)
	}
}

// TestCreateNewFile_RevealsInTree checks the tree learns about the new
// node and the reveal call doesn't panic when there is a real tree —
// refreshTree runs before openFile precisely so Reveal has something to
// find.
func TestCreateNewFile_RevealsInTree(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t, dir)

	a.createNewFile(dir, "brand-new.txt")

	target := filepath.Join(dir, "brand-new.txt")
	if a.tree.ActiveFile != target {
		t.Fatalf("tree.ActiveFile = %q, want %q", a.tree.ActiveFile, target)
	}
}
