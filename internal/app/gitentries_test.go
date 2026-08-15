// =============================================================================
// File: internal/app/gitentries_test.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chasereyn/vincent/internal/filetree"
)

// z joins records into the NUL-delimited shape git actually emits, with the
// trailing terminator. Spelled as a helper because a literal full of \x00
// is unreadable and a missing terminator would quietly test the wrong thing.
func z(records ...string) string {
	out := ""
	for _, r := range records {
		out += r + "\x00"
	}
	return out
}

// TestParsePorcelainZ_RenameIsTwoRecords is the trap this parser exists to
// avoid. A rename emits `R  <new>\0<old>\0` — two records for one change —
// and a parser that walks records uniformly reads the old path as an
// unrelated file, reporting a phantom change that is not there.
func TestParsePorcelainZ_RenameIsTwoRecords(t *testing.T) {
	got := parsePorcelainZ(z("R  src/new.go", "src/old.go", " M other.go"), "/repo")

	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 — the old path was read as its own file: %+v", len(got), got)
	}
	var rename gitEntry
	for _, e := range got {
		if e.Rel == "src/new.go" {
			rename = e
		}
	}
	if rename.Rel == "" {
		t.Fatalf("rename entry missing: %+v", got)
	}
	if rename.Orig != "src/old.go" {
		t.Errorf("Orig = %q, want the old path", rename.Orig)
	}
	if rename.Kind != filetree.GitChangeRenamed {
		t.Errorf("Kind = %v, want GitChangeRenamed", rename.Kind)
	}
}

// TestParsePorcelainZ_TruncatedRenameDoesNotPanic guards the lookahead. A
// status run cut short mid-rename must degrade, not crash — this is polled
// on a ticker while an agent writes in the repo, so a torn read is a matter
// of time rather than a hypothetical.
func TestParsePorcelainZ_TruncatedRenameDoesNotPanic(t *testing.T) {
	got := parsePorcelainZ("R  src/new.go\x00", "/repo")
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(got), got)
	}
	if got[0].Orig != "" {
		t.Errorf("Orig = %q, want empty when the second record never arrived", got[0].Orig)
	}
}

// TestParsePorcelainZ_KeepsAwkwardPathsRaw is why -z is passed at all.
// Without it git applies C-style quoting to anything with a space or a
// non-ASCII character, and every consumer needs an unquoter.
func TestParsePorcelainZ_KeepsAwkwardPathsRaw(t *testing.T) {
	got := parsePorcelainZ(z(" M my folder/a file.go", "?? tildes/año.txt"), "/repo")

	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	wantRel := map[string]string{
		"my folder/a file.go": "a file.go",
		"tildes/año.txt":      "año.txt",
	}
	for _, e := range got {
		name, ok := wantRel[e.Rel]
		if !ok {
			t.Errorf("unexpected path %q — quoting leaked through", e.Rel)
			continue
		}
		if e.Name != name {
			t.Errorf("Name for %q = %q, want %q", e.Rel, e.Name, name)
		}
	}
}

// TestParsePorcelainZ_Kinds pins the status-column mapping the panel and
// the tree both colour from.
func TestParsePorcelainZ_Kinds(t *testing.T) {
	got := parsePorcelainZ(z(
		" M mod.go",
		"?? new.go",
		" D gone.go",
		"A  added.go",
		"M  staged.go",
	), "/repo")

	want := map[string]struct {
		kind      filetree.GitChangeKind
		untracked bool
		deleted   bool
		staged    bool
	}{
		"mod.go":    {filetree.GitChangeModified, false, false, false},
		"new.go":    {filetree.GitChangeAdded, true, false, false},
		"gone.go":   {filetree.GitChangeDeleted, false, true, false},
		"added.go":  {filetree.GitChangeAdded, false, false, true},
		"staged.go": {filetree.GitChangeModified, false, false, true},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for _, e := range got {
		w, ok := want[e.Rel]
		if !ok {
			t.Errorf("unexpected entry %q", e.Rel)
			continue
		}
		if e.Kind != w.kind || e.Untracked != w.untracked || e.Deleted != w.deleted || e.Staged != w.staged {
			t.Errorf("%s = {kind %v untracked %v deleted %v staged %v}, want {%v %v %v %v}",
				e.Rel, e.Kind, e.Untracked, e.Deleted, e.Staged,
				w.kind, w.untracked, w.deleted, w.staged)
		}
	}
}

// TestParsePorcelainZ_SortsByPath keeps the panel's ordering stable. git
// does not promise an order, and a list that reshuffles between ticks is
// unusable for clicking — the row you aimed at moves out from under you.
func TestParsePorcelainZ_SortsByPath(t *testing.T) {
	got := parsePorcelainZ(z(" M zebra.go", " M alpha.go", " M src/mid.go"), "/repo")

	want := []string{"alpha.go", "src/mid.go", "zebra.go"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Rel != w {
			t.Errorf("entry %d = %q, want %q", i, got[i].Rel, w)
		}
	}
}

// TestParsePorcelainZ_SplitsNameAndDir covers the two fields a panel row is
// built from. Not filepath.Base / filepath.Dir: git always reports forward
// slashes, and on Windows those would leave "a/b/c.go" untouched.
func TestParsePorcelainZ_SplitsNameAndDir(t *testing.T) {
	got := parsePorcelainZ(z(" M deep/nested/file.go", " M root.go"), "/repo")

	for _, e := range got {
		switch e.Rel {
		case "deep/nested/file.go":
			if e.Name != "file.go" || e.Dir != "deep/nested" {
				t.Errorf("nested = {%q, %q}, want {file.go, deep/nested}", e.Name, e.Dir)
			}
		case "root.go":
			if e.Name != "root.go" || e.Dir != "" {
				t.Errorf("root = {%q, %q}, want {root.go, \"\"}", e.Name, e.Dir)
			}
		}
	}
}

// TestParsePorcelainZ_IgnoresShortRecords covers the trailing empty record
// the NUL terminator always leaves, plus any garbage too short to be a
// status line.
func TestParsePorcelainZ_IgnoresShortRecords(t *testing.T) {
	if got := parsePorcelainZ("", "/repo"); len(got) != 0 {
		t.Errorf("empty input produced %d entries", len(got))
	}
	got := parsePorcelainZ(z(" M ok.go", "xx"), "/repo")
	if len(got) != 1 {
		t.Errorf("got %d entries, want the short record dropped: %+v", len(got), got)
	}
}

// TestGitSnapshot_DirtyFilesIncludesBothEndsOfARename proves the file tree
// still tints the vacated path. Showing only the new path would make a move
// look like an unexplained new file, with the old one apparently untouched.
func TestGitSnapshot_DirtyFilesIncludesBothEndsOfARename(t *testing.T) {
	root := filepath.Join("/repo")
	snap := gitSnapshot{
		Root:    root,
		Entries: parsePorcelainZ(z("R  src/new.go", "src/old.go"), root),
	}
	dirty := snap.DirtyFiles()

	newPath := filepath.Join(root, "src", "new.go")
	oldPath := filepath.Join(root, "src", "old.go")
	if dirty[newPath] != filetree.GitChangeRenamed {
		t.Errorf("new path = %v, want GitChangeRenamed", dirty[newPath])
	}
	if dirty[oldPath] != filetree.GitChangeDeleted {
		t.Errorf("old path = %v, want GitChangeDeleted — the vacated row must still be tinted", dirty[oldPath])
	}
}

// TestGitSnapshot_TrackedUntrackedSplit is the panel's sectioning.
func TestGitSnapshot_TrackedUntrackedSplit(t *testing.T) {
	snap := gitSnapshot{
		Entries: parsePorcelainZ(z(" M mod.go", "?? new.go", " D gone.go"), "/repo"),
	}
	tracked, untracked := snap.Tracked(), snap.Untracked()

	if len(tracked) != 2 {
		t.Errorf("tracked = %d entries, want 2", len(tracked))
	}
	if len(untracked) != 1 || untracked[0].Rel != "new.go" {
		t.Errorf("untracked = %+v, want just new.go", untracked)
	}
}

// TestLoadGitSnapshot_RealRepo runs the whole pipeline against git, which is
// the only way to catch a wrong flag name — the parser tests all feed it
// output we wrote ourselves.
func TestLoadGitSnapshot_RealRepo(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "one\n")
	gitRun(t, dir, "add", "tracked.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "two\n")
	writeFileT(t, filepath.Join(dir, "fresh.txt"), "new\n")

	snap := loadGitSnapshot(dir)

	if !snap.IsRepo {
		t.Fatal("IsRepo = false for a real repo")
	}
	if snap.Branch != "main" {
		t.Errorf("Branch = %q, want main", snap.Branch)
	}
	if snap.RepoName != filepath.Base(dir) {
		t.Errorf("RepoName = %q, want %q", snap.RepoName, filepath.Base(dir))
	}
	if len(snap.Entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(snap.Entries), snap.Entries)
	}
	// Abs must be a real, openable path — it is what a panel click hands to
	// openDiff, so a wrong separator here breaks clicking on Windows only.
	for _, e := range snap.Entries {
		if _, err := os.Stat(e.Abs); err != nil {
			t.Errorf("Abs %q does not resolve: %v", e.Abs, err)
		}
	}
}

// TestLoadGitSnapshot_UntrackedDirectoryIsExpanded covers the
// --untracked-files=all flag. The default collapses a new directory to
// "dirname/" and you never see the files in it — which for Vincent is the
// worst case to miss, since a directory an agent just created is exactly
// what you sat down to review.
func TestLoadGitSnapshot_UntrackedDirectoryIsExpanded(t *testing.T) {
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "seed.txt"), "x\n")
	gitRun(t, dir, "add", "seed.txt")
	gitRun(t, dir, "commit", "-q", "-m", "seed")

	sub := filepath.Join(dir, "brandnew")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFileT(t, filepath.Join(sub, "a.go"), "a\n")
	writeFileT(t, filepath.Join(sub, "b.go"), "b\n")

	snap := loadGitSnapshot(dir)

	if len(snap.Entries) != 2 {
		t.Fatalf("got %d entries, want both files inside the new directory: %+v",
			len(snap.Entries), snap.Entries)
	}
	for _, e := range snap.Entries {
		if e.Dir != "brandnew" {
			t.Errorf("entry %q has Dir %q, want brandnew", e.Rel, e.Dir)
		}
	}
}

// TestLoadGitSnapshot_NotARepo degrades to the zero value rather than an
// error, so a non-repo directory renders an empty panel and nothing else.
func TestLoadGitSnapshot_NotARepo(t *testing.T) {
	requireGit(t)
	if snap := loadGitSnapshot(t.TempDir()); snap.IsRepo {
		t.Error("IsRepo = true outside a repository")
	}
	if snap := loadGitSnapshot(""); snap.IsRepo {
		t.Error("IsRepo = true for an empty root")
	}
}
