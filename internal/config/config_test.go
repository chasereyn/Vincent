// =============================================================================
// File: internal/config/config_test.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestDefaults pins the documented default — icons mode "auto" — so a
// future refactor of the Defaults helper can't silently flip user-
// visible behaviour for everyone who has no config file.
func TestDefaults(t *testing.T) {
	got := Defaults()
	if got.Icons != IconsAuto {
		t.Fatalf("Defaults().Icons = %q, want %q", got.Icons, IconsAuto)
	}
	if got.TabBar != false {
		t.Fatalf("Defaults().TabBar = %v, want false", got.TabBar)
	}
}

// TestLoadTabBar pins the boolean tabBar key: present-and-true turns the
// strip on, omitted (or present-and-false) leaves the documented default
// of off.
func TestLoadTabBar(t *testing.T) {
	cases := map[string]bool{
		`{}`:               false,
		`{"tabBar":false}`: false,
		`{"tabBar":true}`:  true,
		`{"icons":"on"}`:   false, // unrelated key present, tabBar still defaults off
	}
	for body, want := range cases {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			cfg, err := Load(p)
			if err != nil {
				t.Fatalf("Load(%s): %v", body, err)
			}
			if cfg.TabBar != want {
				t.Fatalf("Load(%s).TabBar = %v, want %v", body, cfg.TabBar, want)
			}
		})
	}
}

// TestLoadEmptyPath verifies that calling Load with no path resolves
// to defaults rather than an error — the editor uses this when
// XDG_CONFIG_HOME is unset and the user has no home directory (CI,
// containers without HOME, etc.).
func TestLoadEmptyPath(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): unexpected error: %v", err)
	}
	if cfg.Icons != IconsAuto {
		t.Fatalf("Load(\"\").Icons = %q, want %q", cfg.Icons, IconsAuto)
	}
}

// TestLoadMissingFile verifies a non-existent config file is treated
// as "no preferences set" — the common case for fresh installs.
func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load(missing): unexpected error: %v", err)
	}
	if cfg.Icons != IconsAuto {
		t.Fatalf("missing file should yield default IconsAuto, got %q", cfg.Icons)
	}
}

// TestLoadEmptyFile covers the "user touched the file but didn't
// write anything" edge case — should be indistinguishable from no
// file at all.
func TestLoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load(empty): %v", err)
	}
	if cfg.Icons != IconsAuto {
		t.Fatalf("empty file should yield default, got %q", cfg.Icons)
	}
}

// TestLoadHappyValues exercises every recognised icons mode exactly
// once so a typo in the switch arms shows up immediately.
func TestLoadHappyValues(t *testing.T) {
	cases := map[string]IconsMode{
		`{"icons":"auto"}`: IconsAuto,
		`{"icons":"on"}`:   IconsOn,
		`{"icons":"off"}`:  IconsOff,
		`{"icons":"AUTO"}`: IconsAuto, // case-insensitive
		`{"icons":" On "}`: IconsOn,   // whitespace-tolerant
		`{}`:               IconsAuto, // omitted field uses default
	}
	for body, want := range cases {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "config.json")
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			cfg, err := Load(p)
			if err != nil {
				t.Fatalf("Load(%s): %v", body, err)
			}
			if cfg.Icons != want {
				t.Fatalf("Load(%s).Icons = %q, want %q", body, cfg.Icons, want)
			}
		})
	}
}

// TestLoadUnknownValue verifies a typo in the icons field surfaces as
// a clear error rather than silently reverting to defaults — that's
// the bug we want users to notice and fix in their config file.
func TestLoadUnknownValue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"icons":"yes-please"}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg, err := Load(p)
	if err == nil {
		t.Fatalf("expected error for unknown value, got nil")
	}
	if cfg.Icons != IconsAuto {
		t.Fatalf("on error should still return safe defaults, got %q", cfg.Icons)
	}
}

// TestLoadMalformedJSON verifies a syntactically broken config doesn't
// crash the editor — the user gets an error and the editor uses
// defaults until they fix the file.
func TestLoadMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{not json`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Load(p); err == nil {
		t.Fatalf("expected error for malformed JSON, got nil")
	}
}

// TestLoadForwardCompat verifies the loader ignores top-level fields
// it doesn't recognise — so a future config.json with new keys keeps
// working on older binaries instead of erroring out.
func TestLoadForwardCompat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	body := `{"icons":"on","theme":"future-feature","unknown_block":{"a":1}}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("forward-compat config should not error, got %v", err)
	}
	if cfg.Icons != IconsOn {
		t.Fatalf("recognised field still expected: got %q", cfg.Icons)
	}
}

// TestDefaultPathHonoursXDG verifies XDG_CONFIG_HOME wins over the
// fallback when set — important for nix-style setups that move every
// dotfile out of $HOME.
func TestDefaultPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	got := DefaultPath()
	want := filepath.Join("/tmp/xdg-test", "vincent", "config.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

// TestDefaultPathFallsBackToHome verifies the ~/.config fallback when
// XDG_CONFIG_HOME isn't set — the common path on macOS/Linux without
// XDG configured.
func TestDefaultPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	// os.UserHomeDir reads USERPROFILE on Windows and HOME everywhere
	// else. Setting only HOME made this test pass on macOS/Linux and
	// fail on Windows against the real profile directory.
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}
	got := DefaultPath()
	want := filepath.Join(home, ".config", "vincent", "config.json")
	if got != want {
		t.Fatalf("DefaultPath() = %q, want %q", got, want)
	}
}

// TestLoadRecentRootsDropsMissing pins the promise cleanRecentRoots makes:
// an entry that is no longer a directory never reaches the picker. One real
// directory, one deleted path, and one plain file go in; only the directory
// comes back.
func TestLoadRecentRootsDropsMissing(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	notADir := filepath.Join(dir, "afile")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	gone := filepath.Join(dir, "gone")

	p := filepath.Join(dir, "config.json")
	if err := Save(p, Config{Icons: IconsOn, RecentRoots: []string{gone, live, notADir}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.RecentRoots) != 1 || got.RecentRoots[0] != live {
		t.Fatalf("RecentRoots = %v, want just %q", got.RecentRoots, live)
	}
}

// TestSaveLoadRoundTrip verifies Save writes a file Load reads back
// unchanged, including the fields the root switcher does not touch — a root
// switch must not silently reset the user's icons or tab-bar preference.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	// Nested path: Save must create the directory, since the very first
	// write happens before ~/.config/vincent exists.
	p := filepath.Join(dir, "nested", "vincent", "config.json")

	want := Config{Icons: IconsOff, TabBar: true, RecentRoots: []string{a, b}}
	if err := Save(p, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Icons != want.Icons {
		t.Errorf("Icons = %q, want %q", got.Icons, want.Icons)
	}
	if got.TabBar != want.TabBar {
		t.Errorf("TabBar = %v, want %v", got.TabBar, want.TabBar)
	}
	if len(got.RecentRoots) != 2 || got.RecentRoots[0] != a || got.RecentRoots[1] != b {
		t.Errorf("RecentRoots = %v, want %v", got.RecentRoots, want.RecentRoots)
	}
}

// TestSaveLeavesNoTempFile verifies the atomic write cleans up after
// itself: after Save the directory holds config.json and nothing else. A
// leftover ".config.json.*" would accumulate one file per root switch.
func TestSaveLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := Save(p, Defaults()); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want just config.json", names)
	}
}

// TestSaveEmptyPathIsNoop verifies Save mirrors Load's "" contract — a
// machine with no resolvable home directory must not error on every root
// switch.
func TestSaveEmptyPathIsNoop(t *testing.T) {
	if err := Save("", Defaults()); err != nil {
		t.Fatalf(`Save("") = %v, want nil`, err)
	}
}

// TestAddRecentRootPromotesAndDedupes verifies the front-of-list contract:
// re-adding a root already in the list moves it to the front rather than
// appending a duplicate.
func TestAddRecentRootPromotesAndDedupes(t *testing.T) {
	sep := string(filepath.Separator)
	cfg := Config{RecentRoots: []string{
		filepath.Join(sep, "one"),
		filepath.Join(sep, "two"),
		filepath.Join(sep, "three"),
	}}
	cfg.AddRecentRoot(filepath.Join(sep, "three"))
	want := []string{
		filepath.Join(sep, "three"),
		filepath.Join(sep, "one"),
		filepath.Join(sep, "two"),
	}
	if len(cfg.RecentRoots) != len(want) {
		t.Fatalf("RecentRoots = %v, want %v", cfg.RecentRoots, want)
	}
	for i := range want {
		if cfg.RecentRoots[i] != want[i] {
			t.Fatalf("RecentRoots = %v, want %v", cfg.RecentRoots, want)
		}
	}
}

// TestAddRecentRootCaps verifies the list never grows past maxRecentRoots,
// so config.json can't balloon over a long-lived install.
func TestAddRecentRootCaps(t *testing.T) {
	sep := string(filepath.Separator)
	var cfg Config
	for i := 0; i < maxRecentRoots*2; i++ {
		cfg.AddRecentRoot(filepath.Join(sep, "p"+strconv.Itoa(i)))
	}
	if len(cfg.RecentRoots) != maxRecentRoots {
		t.Fatalf("len(RecentRoots) = %d, want %d", len(cfg.RecentRoots), maxRecentRoots)
	}
	// Most recent first: the last one added sits at the head.
	head := filepath.Join(sep, "p"+strconv.Itoa(maxRecentRoots*2-1))
	if cfg.RecentRoots[0] != head {
		t.Fatalf("RecentRoots[0] = %q, want %q", cfg.RecentRoots[0], head)
	}
}

// TestAddRecentRootIgnoresBlank verifies a blank path is dropped rather than
// recorded as "" — an empty entry would render as a nameless picker row and
// resolve to the process working directory if clicked.
func TestAddRecentRootIgnoresBlank(t *testing.T) {
	var cfg Config
	cfg.AddRecentRoot("   ")
	if len(cfg.RecentRoots) != 0 {
		t.Fatalf("RecentRoots = %v, want empty", cfg.RecentRoots)
	}
}
