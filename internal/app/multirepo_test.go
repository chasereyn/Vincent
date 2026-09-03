// =============================================================================
// File: internal/app/multirepo_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// multirepo_test.go pins phase 8a from both ends.
//
// Half of it is the new behaviour — discovery, merged status, panel
// grouping, the writes landing in the right repo. The other half is
// REGRESSION COVER for the single-repo root, which is the case Vincent is
// used in at home and the one that must not have changed: every
// singleRepo* test here asserts that a root which IS a repo answers exactly
// what it answered before the registry existed.

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/editor"
	"github.com/chasereyn/vincent/internal/review"
)

// -----------------------------------------------------------------------------
// Fixtures
// -----------------------------------------------------------------------------

// singleRepoFixture is a root that IS a repository, with one modified
// tracked file and one untracked file. The at-home case.
func singleRepoFixture(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := initRepo(t)
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "one\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFileT(t, filepath.Join(dir, "tracked.txt"), "two\n")
	writeFileT(t, filepath.Join(dir, "fresh.txt"), "new\n")
	return dir
}

// multiRepoFixture is a root that is NOT a repository: it holds two repos,
// "alpha" and "beta", plus a plain folder "notes" and a loose file at the
// top level. The at-work case.
//
// alpha has one modified tracked file and one untracked file and sits on
// "main"; beta has one modified tracked file and sits on "release". The
// two branches differ on purpose — a test that cannot tell one repo's
// branch from the other's cannot catch the footer naming the wrong one.
func multiRepoFixture(t *testing.T) (root, alpha, beta string) {
	t.Helper()
	requireGit(t)
	root = tempDirResolved(t)

	alpha = filepath.Join(root, "alpha")
	mkdirT(t, alpha)
	initRepoAt(t, alpha)
	writeFileT(t, filepath.Join(alpha, "a.txt"), "one\n")
	gitRun(t, alpha, "add", "-A")
	gitRun(t, alpha, "commit", "-q", "-m", "seed alpha")
	writeFileT(t, filepath.Join(alpha, "a.txt"), "two\n")
	writeFileT(t, filepath.Join(alpha, "extra.txt"), "new\n")

	beta = filepath.Join(root, "beta")
	mkdirT(t, beta)
	initRepoAt(t, beta)
	gitRun(t, beta, "checkout", "-q", "-b", "release")
	writeFileT(t, filepath.Join(beta, "b.txt"), "one\n")
	gitRun(t, beta, "add", "-A")
	gitRun(t, beta, "commit", "-q", "-m", "seed beta")
	writeFileT(t, filepath.Join(beta, "b.txt"), "two\n")

	// Neither of these is a repo, and neither may ever be treated as one.
	mkdirT(t, filepath.Join(root, "notes"))
	writeFileT(t, filepath.Join(root, "notes", "todo.md"), "# todo\n")
	writeFileT(t, filepath.Join(root, "loose.txt"), "not in any repo\n")

	return root, alpha, beta
}

// tempDirResolved is t.TempDir with symlinks resolved. On macOS the temp
// dir lives under /var, which is a symlink to /private/var, and git reports
// the resolved form — see initRepo, which does the same for the same reason.
func tempDirResolved(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	return resolved
}

// openTabT opens path as an ordinary editable tab, failing the test on an
// IO error. editor.NewTab reads from disk, which is what these tests want:
// a tab whose Path is real is the only kind the git helpers can answer
// about.
func openTabT(t *testing.T, path string) *editor.Tab {
	t.Helper()
	tab, err := editor.NewTab(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return tab
}

// mkdirT creates dir, failing the test rather than the code under test.
func mkdirT(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// multiRepoApp builds an App over the multi-repo fixture with the Changes
// panel open and one full refresh already applied.
func multiRepoApp(t *testing.T) (root, alpha, beta string, a *App) {
	t.Helper()
	root, alpha, beta = multiRepoFixture(t)
	a = newTestApp(t, root)
	a.gitPanelShown = true
	a.refreshGitStatus()
	return root, alpha, beta, a
}

// panelLines returns the panel's own columns, one trimmed string per screen
// row, after a draw. The panel's job is to be readable, so the assertions
// are against pixels rather than against the model.
//
// Sliced by RUNE, not by byte. The sidebar to the left draws indent guides
// and chevrons, which are multi-byte in UTF-8, so a byte offset into the
// row lands mid-panel and the columns come back shifted.
func panelLines(t *testing.T, a *App) []string {
	t.Helper()
	a.draw()
	a.screen.(tcell.SimulationScreen).Show()
	px, _, pw, h := a.gitPanelRect()
	out := []string{}
	for y := 0; y < h; y++ {
		row := []rune(panelRowText(t, a, y))
		if px >= len(row) {
			out = append(out, "")
			continue
		}
		end := px + pw
		if end > len(row) {
			end = len(row)
		}
		out = append(out, strings.TrimSpace(string(row[px:end])))
	}
	return out
}

// -----------------------------------------------------------------------------
// Discovery and the registry
// -----------------------------------------------------------------------------

// TestRefreshRepos_SingleRepoRoot pins the regression case: a root that is
// itself a repo discovers exactly itself, reports singleRepoMode, and hands
// every git call a.rootDir verbatim — which is what every call site passed
// before phase 8a existed.
func TestRefreshRepos_SingleRepoRoot(t *testing.T) {
	dir := singleRepoFixture(t)
	a := newTestApp(t, dir)
	a.refreshRepos()

	if len(a.repos) != 1 || a.repos[0] != dir {
		t.Fatalf("repos = %v, want just %q", a.repos, dir)
	}
	if !a.rootIsRepo() || !a.singleRepoMode() {
		t.Fatalf("rootIsRepo=%v singleRepoMode=%v, want both true", a.rootIsRepo(), a.singleRepoMode())
	}
	if got := a.repoFor(filepath.Join(dir, "tracked.txt")); got != a.rootDir {
		t.Errorf("repoFor = %q, want rootDir %q", got, a.rootDir)
	}
	// The pathspec must stay the absolute path the old callers passed, so
	// the argv git sees is byte-identical.
	want := filepath.Join(dir, "tracked.txt")
	if repo, spec := a.gitPathFor(want); repo != a.rootDir || spec != want {
		t.Errorf("gitPathFor = (%q, %q), want (%q, %q)", repo, spec, a.rootDir, want)
	}
}

// TestRefreshRepos_FolderOfRepos checks discovery finds each repo under a
// non-repo root and never mistakes a plain folder for one.
func TestRefreshRepos_FolderOfRepos(t *testing.T) {
	root, alpha, beta := multiRepoFixture(t)
	a := newTestApp(t, root)
	a.refreshRepos()

	want := []string{alpha, beta}
	if len(a.repos) != len(want) {
		t.Fatalf("repos = %v, want %v", a.repos, want)
	}
	for i, w := range want {
		if a.repos[i] != w {
			t.Errorf("repos[%d] = %q, want %q", i, a.repos[i], w)
		}
	}
	if a.rootIsRepo() || a.singleRepoMode() {
		t.Error("a folder of repos reported itself as a single repo")
	}
}

// TestRefreshRepos_PicksUpANewClone pins the tick's job: a repo that
// appears in the folder while Vincent is open must show up on the next
// refresh, without a restart.
func TestRefreshRepos_PicksUpANewClone(t *testing.T) {
	root, alpha, beta := multiRepoFixture(t)
	a := newTestApp(t, root)
	a.refreshRepos()
	if len(a.repos) != 2 {
		t.Fatalf("repos = %v, want two", a.repos)
	}

	gamma := filepath.Join(root, "gamma")
	mkdirT(t, gamma)
	initRepoAt(t, gamma)

	a.refreshRepos()
	if len(a.repos) != 3 {
		t.Fatalf("repos after a new clone = %v, want three", a.repos)
	}
	if a.repos[0] != alpha || a.repos[1] != beta || a.repos[2] != gamma {
		t.Errorf("repos = %v, want [%s %s %s]", a.repos, alpha, beta, gamma)
	}
}

// TestRepoFor_MultiRepoOwnershipAndOutsiders checks the owner lookup, and
// the answer for a file that belongs to no repo — which callers act on by
// declining to shell out at all.
func TestRepoFor_MultiRepoOwnershipAndOutsiders(t *testing.T) {
	root, alpha, beta, a := multiRepoApp(t)

	if got := a.repoFor(filepath.Join(alpha, "a.txt")); got != alpha {
		t.Errorf("repoFor(alpha file) = %q, want %q", got, alpha)
	}
	if got := a.repoFor(filepath.Join(beta, "b.txt")); got != beta {
		t.Errorf("repoFor(beta file) = %q, want %q", got, beta)
	}
	for _, outside := range []string{
		filepath.Join(root, "loose.txt"),
		filepath.Join(root, "notes", "todo.md"),
	} {
		if got := a.repoFor(outside); got != "" {
			t.Errorf("repoFor(%q) = %q, want \"\"", outside, got)
		}
		if repo, spec := a.gitPathFor(outside); repo != "" || spec != "" {
			t.Errorf("gitPathFor(%q) = (%q, %q), want empty", outside, repo, spec)
		}
	}
}

// TestGitPathFor_MultiRepoIsRepoRelative pins the pathspec form. A command
// run with `-C <repo>` is only guaranteed to accept a path inside that
// repo's tree, and a repo-relative one is the form that can never be
// outside it.
func TestGitPathFor_MultiRepoIsRepoRelative(t *testing.T) {
	_, alpha, _, a := multiRepoApp(t)
	mkdirT(t, filepath.Join(alpha, "src"))
	path := filepath.Join(alpha, "src", "main.go")

	repo, spec := a.gitPathFor(path)
	if repo != alpha {
		t.Errorf("repo = %q, want %q", repo, alpha)
	}
	if spec != "src/main.go" {
		t.Errorf("spec = %q, want src/main.go (forward slashes on every platform)", spec)
	}
}

// -----------------------------------------------------------------------------
// activeRepo's precedence
// -----------------------------------------------------------------------------

// TestActiveRepo_SingleRepoIsAlwaysTheRoot pins the regression case: in
// single-repo mode activeRepo never consults a tab, a panel row, or the
// snapshot — it is the root, which is what every write used before.
func TestActiveRepo_SingleRepoIsAlwaysTheRoot(t *testing.T) {
	dir := singleRepoFixture(t)
	a := newTestApp(t, dir)
	a.refreshGitStatus()
	if got := a.activeRepo(); got != a.rootDir {
		t.Fatalf("activeRepo = %q, want rootDir %q", got, a.rootDir)
	}
	// Even with a tab open on a file outside the root entirely.
	other := filepath.Join(t.TempDir(), "elsewhere.txt")
	writeFileT(t, other, "x\n")
	a.tabs = append(a.tabs, openTabT(t, other))
	a.activeTab = len(a.tabs) - 1
	if got := a.activeRepo(); got != a.rootDir {
		t.Fatalf("activeRepo with an outside tab = %q, want rootDir %q", got, a.rootDir)
	}
}

// TestActiveRepo_Precedence walks the four multi-repo rules in order,
// removing one signal at a time so each rule is exercised as the one that
// actually decided.
func TestActiveRepo_Precedence(t *testing.T) {
	root, alpha, beta, a := multiRepoApp(t)

	// Rule 4 (last resort): no tab, no clicked row. Both repos have
	// changes, so rule 3 answers first with the first one that does.
	if got := a.activeRepo(); got != alpha {
		t.Fatalf("with no other signal, activeRepo = %q, want the first repo with changes %q", got, alpha)
	}

	// Rule 3: the first repo WITH changes, when the first repo overall is
	// clean. Committing alpha leaves beta as the only dirty one.
	gitRun(t, alpha, "add", "-A")
	gitRun(t, alpha, "commit", "-q", "-m", "clean alpha")
	a.refreshGitStatus()
	if got := a.activeRepo(); got != beta {
		t.Fatalf("with alpha clean, activeRepo = %q, want %q", got, beta)
	}

	// Rule 2: a clicked Changes row wins over "first with changes".
	a.setGitPanelRepo(alpha)
	if got := a.activeRepo(); got != alpha {
		t.Fatalf("after clicking an alpha row, activeRepo = %q, want %q", got, alpha)
	}

	// Rule 1: the active tab wins over everything below it.
	path := filepath.Join(beta, "b.txt")
	a.tabs = append(a.tabs, openTabT(t, path))
	a.activeTab = len(a.tabs) - 1
	if got := a.activeRepo(); got != beta {
		t.Fatalf("with a beta tab focused, activeRepo = %q, want %q", got, beta)
	}

	// A tab on a file in no repo at all falls through rather than
	// answering "" — the writes still need somewhere to go.
	loose := filepath.Join(root, "loose.txt")
	a.tabs = append(a.tabs, openTabT(t, loose))
	a.activeTab = len(a.tabs) - 1
	if got := a.activeRepo(); got != alpha {
		t.Fatalf("with a repo-less tab focused, activeRepo = %q, want the clicked repo %q", got, alpha)
	}
}

// TestActiveRepoSnapshotAndBranch checks the footer's two facts come from
// the active repo, and that the two repos' branches are told apart.
func TestActiveRepoSnapshotAndBranch(t *testing.T) {
	_, alpha, beta, a := multiRepoApp(t)

	a.setGitPanelRepo(alpha)
	if got := a.activeRepoName(); got != "alpha" {
		t.Errorf("activeRepoName = %q, want alpha", got)
	}
	if got := a.branchLabel(); got != "main" {
		t.Errorf("branchLabel = %q, want main", got)
	}

	a.setGitPanelRepo(beta)
	if got := a.activeRepoName(); got != "beta" {
		t.Errorf("activeRepoName = %q, want beta", got)
	}
	if got := a.branchLabel(); got != "release" {
		t.Errorf("branchLabel = %q, want release", got)
	}
}

// -----------------------------------------------------------------------------
// Merged status
// -----------------------------------------------------------------------------

// TestLoadReposSnapshot_SingleRepoIsUnchanged pins that the multi-repo
// entry point collapses to the plain single-repo read: same entries, no
// per-repo members, and the branch still on the top-level snapshot.
func TestLoadReposSnapshot_SingleRepoIsUnchanged(t *testing.T) {
	dir := singleRepoFixture(t)

	plain := loadGitSnapshot(dir)
	merged := loadReposSnapshot(dir, []string{dir})

	if merged.IsRepo != plain.IsRepo || merged.Root != plain.Root || merged.Branch != plain.Branch {
		t.Fatalf("merged = %+v, want the same facts as %+v", merged, plain)
	}
	if len(merged.Repos) != 0 {
		t.Errorf("single-repo snapshot carries %d per-repo members, want none", len(merged.Repos))
	}
	if len(merged.Entries) != len(plain.Entries) {
		t.Fatalf("merged has %d entries, plain has %d", len(merged.Entries), len(plain.Entries))
	}
	// A root INSIDE a repo (discovery finds nothing) must behave the same,
	// because git walking up from the root is the right answer there.
	sub := filepath.Join(dir, "sub")
	mkdirT(t, sub)
	if snap := loadReposSnapshot(sub, nil); !snap.IsRepo {
		t.Error("a subfolder of a repo reported as not a repo")
	}
}

// TestLoadReposSnapshot_MergesEveryRepo is the core of requirement 2: one
// status read per repo, merged, with each repo's own branch preserved and
// every entry keyed by an absolute path.
func TestLoadReposSnapshot_MergesEveryRepo(t *testing.T) {
	root, alpha, beta := multiRepoFixture(t)

	snap := loadReposSnapshot(root, []string{alpha, beta})

	if !snap.IsRepo {
		t.Fatal("a folder holding two repos reported as not a repo")
	}
	if len(snap.Repos) != 2 {
		t.Fatalf("got %d per-repo snapshots, want 2", len(snap.Repos))
	}
	if snap.Repos[0].Root != alpha || snap.Repos[0].Branch != "main" {
		t.Errorf("repos[0] = %q on %q, want %q on main", snap.Repos[0].Root, snap.Repos[0].Branch, alpha)
	}
	if snap.Repos[1].Root != beta || snap.Repos[1].Branch != "release" {
		t.Errorf("repos[1] = %q on %q, want %q on release", snap.Repos[1].Root, snap.Repos[1].Branch, beta)
	}
	// alpha: a.txt modified + extra.txt untracked. beta: b.txt modified.
	if len(snap.Entries) != 3 {
		t.Fatalf("merged entry count = %d, want 3: %+v", len(snap.Entries), snap.Entries)
	}
	// Every entry has to name its own repo, or DirtyFiles cannot resolve a
	// rename's old path and the panel cannot group.
	for _, e := range snap.Entries {
		if e.Repo != alpha && e.Repo != beta {
			t.Errorf("entry %q has Repo %q, want one of the two repos", e.Rel, e.Repo)
		}
		if _, err := os.Stat(e.Abs); err != nil {
			t.Errorf("Abs %q does not resolve: %v", e.Abs, err)
		}
	}
	dirty := snap.DirtyFiles()
	for _, want := range []string{
		filepath.Join(alpha, "a.txt"),
		filepath.Join(alpha, "extra.txt"),
		filepath.Join(beta, "b.txt"),
	} {
		if _, ok := dirty[want]; !ok {
			t.Errorf("DirtyFiles is missing %q: %v", want, sortedKeys(dirty))
		}
	}
}

// TestApplyGitSnapshot_ColoursEveryRepoAndTheRepoFolders is requirement 2
// as the reviewer sees it: a changed file in ANY repo is tinted in the
// tree, and the repo folder holding it is tinted too, so a collapsed repo
// still signals that the agent has been in there.
func TestApplyGitSnapshot_ColoursEveryRepoAndTheRepoFolders(t *testing.T) {
	_, alpha, beta, a := multiRepoApp(t)

	for _, want := range []string{
		filepath.Join(alpha, "a.txt"),
		filepath.Join(alpha, "extra.txt"),
		filepath.Join(beta, "b.txt"),
	} {
		if a.tree.DirtyFiles[want] == 0 {
			t.Errorf("tree does not colour %q: %v", want, sortedKeys(a.tree.DirtyFiles))
		}
	}
	for _, folder := range []string{alpha, beta} {
		if a.tree.DirtyFolders[folder] == 0 {
			t.Errorf("repo folder %q is not coloured: %v", folder, sortedKeys(a.tree.DirtyFolders))
		}
	}
	// The branch map the tree decorates from carries both repos.
	if got := a.tree.RepoBranches[alpha]; got != "main" {
		t.Errorf("RepoBranches[alpha] = %q, want main", got)
	}
	if got := a.tree.RepoBranches[beta]; got != "release" {
		t.Errorf("RepoBranches[beta] = %q, want release", got)
	}
}

// TestApplyGitSnapshot_SingleRepoHasNoBranchMap pins requirement 7's
// exclusion: with one repo the footer already names the branch, so the
// tree draws no decoration at all.
func TestApplyGitSnapshot_SingleRepoHasNoBranchMap(t *testing.T) {
	dir := singleRepoFixture(t)
	a := newTestApp(t, dir)
	a.refreshGitStatus()
	if len(a.tree.RepoBranches) != 0 {
		t.Errorf("single-repo root produced a branch map: %v", a.tree.RepoBranches)
	}
}

// TestRunGitPoll_ReadsEveryRepoOnTheWorker checks the poll worker performs
// the same merged read the synchronous path does, and posts it as one
// result.
func TestRunGitPoll_ReadsEveryRepoOnTheWorker(t *testing.T) {
	root, alpha, beta := multiRepoFixture(t)

	res := runGitPoll(gitPollRequest{rootDir: root, tree: true, repos: []string{alpha, beta}})

	if len(res.snap.Repos) != 2 {
		t.Fatalf("poll produced %d per-repo snapshots, want 2", len(res.snap.Repos))
	}
	if len(res.snap.Entries) != 3 {
		t.Errorf("poll merged %d entries, want 3", len(res.snap.Entries))
	}
}

// -----------------------------------------------------------------------------
// The Changes panel
// -----------------------------------------------------------------------------

// TestGitPanel_SingleRepoLayoutUnchanged is the regression test for
// requirement 3. With one repository the list is Tracked / files /
// Untracked / files and there is NO repo header row anywhere in it.
func TestGitPanel_SingleRepoLayoutUnchanged(t *testing.T) {
	dir, a := panelApp(t)
	lines := panelLines(t, a)

	// Exactly the pre-8a shape: the first four non-blank list rows.
	want := []string{"Tracked", "modified.txt", "removed.txt", "Untracked"}
	got := []string{}
	for _, line := range lines[gitPanelHeaderRows:] {
		if line == "" {
			continue
		}
		got = append(got, strings.Fields(line)[0])
		if len(got) == len(want) {
			break
		}
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("panel list = %v, want it to start %v", got, want)
		}
	}
	// The footer names the repo; no row above it does.
	for _, line := range lines[gitPanelHeaderRows : len(lines)-gitPanelBranchRows-1] {
		if strings.HasPrefix(line, "⑂") {
			t.Errorf("single-repo panel drew a repo header row: %q", line)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "⑂ "+filepath.Base(dir)+" / main") {
		t.Errorf("footer does not name the repo and branch:\n%s", strings.Join(lines, "\n"))
	}
}

// TestGitPanel_GroupsByRepo is requirement 3's new half: a header per repo
// WITH changes, its own sections beneath it, a count spanning the folder,
// and no row at all for a repo that is clean.
func TestGitPanel_GroupsByRepo(t *testing.T) {
	root, alpha, _, a := multiRepoApp(t)

	// A third, clean repo must not be listed.
	gamma := filepath.Join(root, "gamma")
	mkdirT(t, gamma)
	initRepoAt(t, gamma)
	writeFileT(t, filepath.Join(gamma, "g.txt"), "x\n")
	gitRun(t, gamma, "add", "-A")
	gitRun(t, gamma, "commit", "-q", "-m", "seed gamma")
	a.refreshGitStatus()

	lines := panelLines(t, a)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(lines[0], "Changes (3)") {
		t.Errorf("header = %q, want Changes (3) across the whole folder", lines[0])
	}
	for _, want := range []string{"⑂ alpha / main", "⑂ beta / release", "a.txt", "extra.txt", "b.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel is missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "⑂ gamma") {
		t.Errorf("a clean repo was listed:\n%s", joined)
	}
	// alpha's files sit between alpha's header and beta's.
	ia := strings.Index(joined, "⑂ alpha")
	ib := strings.Index(joined, "⑂ beta")
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("headers out of order: alpha at %d, beta at %d", ia, ib)
	}
	if x := strings.Index(joined, "extra.txt"); x < ia || x > ib {
		t.Errorf("alpha's untracked file at %d is not inside alpha's group (%d..%d)", x, ia, ib)
	}
	if x := strings.Index(joined, "b.txt"); x < ib {
		t.Errorf("beta's file at %d rendered above beta's header at %d", x, ib)
	}
	_ = alpha
}

// TestGitPanelClick_OpensTheDiffInTheOwningRepo checks a row click both
// opens the right diff and records the repo it belonged to, which is what
// keeps the footer and the writes pointed at it afterwards.
func TestGitPanelClick_OpensTheDiffInTheOwningRepo(t *testing.T) {
	_, _, beta, a := multiRepoApp(t)
	a.draw()

	target := filepath.Join(beta, "b.txt")
	row := -1
	for _, r := range a.lastGitPanelRows {
		if r.entry.Abs == target {
			row = r.y
		}
	}
	if row < 0 {
		t.Fatalf("no panel row for %q", target)
	}
	a.gitPanelClick(a.gitPanelRect2X(), row)

	if a.gitPanelRepo != beta {
		t.Errorf("clicked row recorded repo %q, want %q", a.gitPanelRepo, beta)
	}
	tab := a.activeTabPtr()
	if tab == nil || !tab.IsDiff() || tab.Path != target {
		t.Fatalf("active tab = %+v, want a diff of %q", tab, target)
	}
	if len(tab.DiffRows) == 0 {
		t.Error("the diff opened with no rows — it was read in the wrong repo")
	}
}

// gitPanelRect2X is the panel's first content column, for a click that has
// to land inside the panel but is not testing horizontal geometry.
func (a *App) gitPanelRect2X() int {
	x, _, _, _ := a.gitPanelRect()
	return x + 1
}

// -----------------------------------------------------------------------------
// Diffs and gutters
// -----------------------------------------------------------------------------

// TestOpenDiff_ReadsInTheOwningRepo checks requirement 5: a file in a repo
// under a non-repo root still gets its diff, which it never did before —
// `git -C <root>` there is not a repository at all.
func TestOpenDiff_ReadsInTheOwningRepo(t *testing.T) {
	_, alpha, beta, a := multiRepoApp(t)

	for _, path := range []string{filepath.Join(alpha, "a.txt"), filepath.Join(beta, "b.txt")} {
		a.openDiff(path)
		tab := a.activeTabPtr()
		if tab == nil || !tab.IsDiff() || tab.Path != path {
			t.Fatalf("openDiff(%q) did not open a diff tab: %+v", path, tab)
		}
		if len(tab.DiffRows) == 0 {
			t.Errorf("diff of %q has no rows", path)
		}
	}
}

// TestOpenDiff_RefusesAFileInNoRepo pins the flash. "No changes" would be a
// lie — the file is not clean, it is simply not in a repository — and the
// distinction is the difference between "nothing to review" and "Vincent
// cannot tell you".
func TestOpenDiff_RefusesAFileInNoRepo(t *testing.T) {
	root, _, _, a := multiRepoApp(t)
	before := len(a.tabs)

	a.openDiff(filepath.Join(root, "loose.txt"))

	if len(a.tabs) != before {
		t.Errorf("a file in no repo opened %d new tabs", len(a.tabs)-before)
	}
	if !strings.Contains(a.statusMsg, "not in a git repository") {
		t.Errorf("flash = %q, want it to say the file is not in a git repository", a.statusMsg)
	}
}

// TestRefreshGitLineChanges_PerRepoGutters checks the editor's git gutter
// is read in the owning repo, and stays blank for a file in none.
func TestRefreshGitLineChanges_PerRepoGutters(t *testing.T) {
	root, alpha, _, a := multiRepoApp(t)

	inRepo := filepath.Join(alpha, "a.txt")
	outside := filepath.Join(root, "loose.txt")
	a.tabs = append(a.tabs, openTabT(t, inRepo), openTabT(t, outside))
	a.refreshGitLineChanges()

	if len(a.tabs[len(a.tabs)-2].GitLines) == 0 {
		t.Error("a modified file inside a repo got no gutter markers")
	}
	if len(a.tabs[len(a.tabs)-1].GitLines) != 0 {
		t.Error("a file in no repo got gutter markers from somewhere")
	}
}

// -----------------------------------------------------------------------------
// The three writes
// -----------------------------------------------------------------------------

// TestCommitBoxTitle checks the border label: unchanged with one repo, and
// naming the target when there are several, so the reviewer never has to
// infer which repo Enter is about to commit.
func TestCommitBoxTitle(t *testing.T) {
	dir := singleRepoFixture(t)
	single := newTestApp(t, dir)
	single.refreshGitStatus()
	if got := single.commitBoxTitle(); got != "Commit all" {
		t.Errorf("single-repo commit box title = %q, want \"Commit all\"", got)
	}

	_, alpha, beta, a := multiRepoApp(t)
	a.setGitPanelRepo(alpha)
	if got := a.commitBoxTitle(); got != "Commit to alpha" {
		t.Errorf("commit box title = %q, want \"Commit to alpha\"", got)
	}
	a.setGitPanelRepo(beta)
	if got := a.commitBoxTitle(); got != "Commit to beta" {
		t.Errorf("commit box title = %q, want \"Commit to beta\"", got)
	}
}

// TestSubmitCommit_RunsInTheActiveRepo is the write that would do real
// damage in the wrong place. The injected runner records the directory it
// was handed, so this asserts on where `add -A` and `commit` actually ran.
func TestSubmitCommit_RunsInTheActiveRepo(t *testing.T) {
	_, _, beta, a := multiRepoApp(t)
	fake := newFakeGit(nil)
	a.gitWriteRunner = fake.run
	a.setGitPanelRepo(beta)

	a.commitOpen = true
	a.commitValue = []rune("tidy beta")
	a.submitCommit()

	if len(fake.dirs) == 0 {
		t.Fatal("no git command ran")
	}
	for i, dir := range fake.dirs {
		if dir != beta {
			t.Errorf("call %d (%s) ran in %q, want %q", i, fake.argv()[i], dir, beta)
		}
	}
}

// TestSubmitCommit_RefusalsAreAboutTheActiveRepo checks the two guards
// narrowed with the writes: "nothing to commit" asks the active repo, and a
// dirty tab in ANOTHER repo does not block this commit.
func TestSubmitCommit_RefusalsAreAboutTheActiveRepo(t *testing.T) {
	_, alpha, beta, a := multiRepoApp(t)
	fake := newFakeGit(nil)
	a.gitWriteRunner = fake.run

	// Commit alpha clean, then aim at it: the folder still has changes (in
	// beta) but alpha has none, and the refusal must be about alpha.
	gitRun(t, alpha, "add", "-A")
	gitRun(t, alpha, "commit", "-q", "-m", "clean alpha")
	a.refreshGitStatus()
	a.setGitPanelRepo(alpha)
	a.openCommitBox()
	if a.commitOpen {
		t.Error("the commit box opened for a repo with no changes")
	}
	if !strings.Contains(a.statusMsg, "Nothing to commit") {
		t.Errorf("flash = %q, want the nothing-to-commit refusal", a.statusMsg)
	}

	// A dirty tab in alpha must not block a commit aimed at beta.
	dirtyTab := openTabT(t, filepath.Join(alpha, "a.txt"))
	dirtyTab.Dirty = true
	a.tabs = append(a.tabs, dirtyTab)
	a.setGitPanelRepo(beta)
	if n := a.dirtyTabCountIn(beta); n != 0 {
		t.Errorf("dirtyTabCountIn(beta) = %d, want 0 — the dirty tab is in alpha", n)
	}
	if n := a.dirtyTabCountIn(alpha); n != 1 {
		t.Errorf("dirtyTabCountIn(alpha) = %d, want 1", n)
	}
}

// TestDirtyTabCountIn_SingleRepoCountsEverything is the regression half:
// with one repo the narrowed guard has to count exactly what the old
// dirtyTabCount counted, a tab outside the root included.
func TestDirtyTabCountIn_SingleRepoCountsEverything(t *testing.T) {
	dir := singleRepoFixture(t)
	a := newTestApp(t, dir)
	a.refreshGitStatus()

	inside := openTabT(t, filepath.Join(dir, "tracked.txt"))
	inside.Dirty = true
	elsewhere := filepath.Join(t.TempDir(), "elsewhere.txt")
	writeFileT(t, elsewhere, "y\n")
	outside := openTabT(t, elsewhere)
	outside.Dirty = true
	untitled := &editor.Tab{Buffer: editor.NewBuffer(""), Dirty: true}
	a.tabs = append(a.tabs, inside, outside, untitled)

	if got, want := a.dirtyTabCountIn(a.activeRepo()), a.dirtyTabCount(); got != want {
		t.Errorf("dirtyTabCountIn = %d, want dirtyTabCount = %d", got, want)
	}
}

// TestCheckoutBranch_RunsInTheActiveRepo checks the second write's
// directory the same way, and that the branch list is read there too.
func TestCheckoutBranch_RunsInTheActiveRepo(t *testing.T) {
	_, _, beta, a := multiRepoApp(t)
	fake := newFakeGit(map[string]fakeGitReply{
		"for-each-ref": {stdout: "release\nmain\n"},
	})
	a.gitWriteRunner = fake.run
	a.setGitPanelRepo(beta)

	a.openBranchPicker()
	if !a.branchPicker.open {
		t.Fatalf("branch picker did not open: %q", a.statusMsg)
	}
	a.checkoutBranch("main")

	if len(fake.dirs) == 0 {
		t.Fatal("no git command ran")
	}
	for i, dir := range fake.dirs {
		if dir != beta {
			t.Errorf("call %d (%s) ran in %q, want %q", i, fake.argv()[i], dir, beta)
		}
	}
}

// TestPushBranch_TargetsTheActiveRepo checks the push worker is handed the
// active repo. The push runs on a goroutine, so this asserts on the
// directory the runner recorded rather than on any UI state.
func TestPushBranch_TargetsTheActiveRepo(t *testing.T) {
	_, alpha, _, a := multiRepoApp(t)
	done := make(chan string, 4)
	a.gitWriteRunner = func(_ context.Context, dir string, args ...string) (string, string, error) {
		done <- dir
		if args[0] == "rev-parse" && len(args) > 1 && args[1] == "--abbrev-ref" {
			return "main\n", "", nil
		}
		return "", "", nil
	}
	a.setGitPanelRepo(alpha)

	a.pushBranch()
	if got := <-done; got != alpha {
		t.Errorf("push ran in %q, want %q", got, alpha)
	}
}

// -----------------------------------------------------------------------------
// Review notes
// -----------------------------------------------------------------------------

// TestReviewPathFor_SingleRepoUnchanged pins the regression case: with a
// repo as the root a note's path is root-relative, exactly as before.
func TestReviewPathFor_SingleRepoUnchanged(t *testing.T) {
	dir := singleRepoFixture(t)
	a := newTestApp(t, dir)
	a.refreshGitStatus()

	if got := a.reviewPathFor(filepath.Join(dir, "tracked.txt")); got != "tracked.txt" {
		t.Errorf("reviewPathFor = %q, want tracked.txt", got)
	}
	if got := a.reviewRepoFor(filepath.Join(dir, "tracked.txt")); got != "" {
		t.Errorf("reviewRepoFor = %q, want \"\" in single-repo mode", got)
	}
}

// TestReviewPathFor_RelativeToTheOwningRepo is requirement 6. The agent
// receiving the batch has the REPO as its working directory, not Vincent's
// root, so a root-relative "alpha/a.txt" would be one segment wrong in
// every path it pastes into a tool call.
func TestReviewPathFor_RelativeToTheOwningRepo(t *testing.T) {
	_, alpha, beta, a := multiRepoApp(t)
	mkdirT(t, filepath.Join(alpha, "src"))

	if got := a.reviewPathFor(filepath.Join(alpha, "src", "main.go")); got != "src/main.go" {
		t.Errorf("reviewPathFor(alpha) = %q, want src/main.go", got)
	}
	if got := a.reviewPathFor(filepath.Join(beta, "b.txt")); got != "b.txt" {
		t.Errorf("reviewPathFor(beta) = %q, want b.txt", got)
	}
	if got := a.reviewRepoFor(filepath.Join(beta, "b.txt")); got != beta {
		t.Errorf("reviewRepoFor = %q, want %q", got, beta)
	}
}

// TestMarkStaleComments_DoesNotConfuseTwoReposSharingAPath is why the
// comment carries its repo. Both repos hold "same.txt"; only alpha's is
// changed, so only alpha's note is fresh.
func TestMarkStaleComments_DoesNotConfuseTwoReposSharingAPath(t *testing.T) {
	_, alpha, beta, a := multiRepoApp(t)
	writeFileT(t, filepath.Join(alpha, "same.txt"), "changed\n")
	writeFileT(t, filepath.Join(beta, "same.txt"), "seed\n")
	gitRun(t, beta, "add", "-A")
	gitRun(t, beta, "commit", "-q", "-m", "commit beta's same.txt")

	a.reviewBatch.Comments = []review.Comment{
		{File: "same.txt", Repo: alpha, Text: "alpha"},
		{File: "same.txt", Repo: beta, Text: "beta"},
	}
	a.refreshGitStatus()

	if a.reviewBatch.Comments[0].Stale {
		t.Error("alpha's note went stale even though alpha's same.txt is changed")
	}
	if !a.reviewBatch.Comments[1].Stale {
		t.Error("beta's note is not stale even though beta's same.txt is committed")
	}
}

// TestOpenDiffForComment_ResolvesAgainstTheNotesRepo checks a saved note
// still finds its file, which needs the repo because the recorded path is
// relative to it and not to the root.
func TestOpenDiffForComment_ResolvesAgainstTheNotesRepo(t *testing.T) {
	_, _, beta, a := multiRepoApp(t)
	a.reviewBatch.Comments = []review.Comment{{File: "b.txt", Repo: beta, Text: "look"}}

	a.openDiffForComment(0)

	tab := a.activeTabPtr()
	want := filepath.Join(beta, "b.txt")
	if tab == nil || !tab.IsDiff() || tab.Path != want {
		t.Fatalf("active tab = %+v, want a diff of %q (flash: %q)", tab, want, a.statusMsg)
	}
}
