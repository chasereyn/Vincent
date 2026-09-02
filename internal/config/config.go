// =============================================================================
// File: internal/config/config.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-30
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Package config loads Vincent's small user-level config from
// ~/.config/vincent/config.json. It's separate from customactions on
// purpose: actions.json is a list of shell-out menu entries, config.json
// is editor preferences. Keeping them apart means a malformed actions
// file can't break editor settings and vice-versa.
//
// Schema today is intentionally tiny — two keys — but the loader is
// already wrapped in a struct so we can grow new top-level fields
// without breaking older configs:
//
//	{"icons": "auto"}    // default; auto-detect Nerd Fonts on startup
//	{"icons": "on"}      // force-on, even if detection would say no
//	{"icons": "off"}     // force-off, even if a Nerd Font is installed
//	{"tabBar": true}     // show the full tab strip; default is false —
//	                     // row 0 shows only the ≡ button and the active
//	                     // tab's name until toggled on (Esc-b)
//	{"recentRoots": [    // folders Vincent has been rooted at, most
//	  "/Users/x/a",      // recent first, capped at maxRecentRoots.
//	  "/Users/x/b"]}     // Rewritten by Save on every root switch.
//
// The loader is best-effort the same way customactions is: missing
// file → defaults, malformed file → error returned for the app to
// flash, but the editor still starts cleanly.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IconsMode is the user's preference for Nerd Font icons in the file
// tree. "auto" means "use them iff a Nerd Font is installed"; the
// other two values bypass detection entirely.
type IconsMode string

const (
	IconsAuto IconsMode = "auto"
	IconsOn   IconsMode = "on"
	IconsOff  IconsMode = "off"
)

// Config is the resolved, validated form of config.json. Callers get a
// fully-populated Config back from Load — defaults are filled in for
// any field the file omitted, so consumers never need to nil-check.
type Config struct {
	Icons IconsMode
	// TabBar shows or hides the tab strip in row 0. Default false: with
	// one tab open (the common case for a review session) a full strip
	// is a wasted row, so row 0 shows just the ≡ button and the active
	// tab's name until the user turns it on (Esc-b, or the ≡ menu).
	TabBar bool

	// RecentRoots is the folders Vincent has been rooted at, most recent
	// first and capped at maxRecentRoots. It is the list the Esc-o root
	// picker shows, which is why it lives in config rather than in
	// memory: the whole point of a recents list is that it survives a
	// restart. Load drops entries that are no longer directories, so a
	// deleted or unmounted project cannot sit in the picker forever.
	RecentRoots []string
}

// maxRecentRoots caps the remembered-roots list. Ten fits in the picker
// without scrolling and is well past the number of projects anyone switches
// between in a session; a short list also keeps the rewrite-on-every-switch
// cheap.
const maxRecentRoots = 10

// Defaults returns a Config populated with the values used when no
// config file is present (or every field in it is blank). Centralised
// so tests and the loader can't drift from each other.
func Defaults() Config {
	return Config{Icons: IconsAuto, TabBar: false, RecentRoots: nil}
}

// fileFormat mirrors the on-disk JSON shape. We decode into this and
// then promote into Config so the public type doesn't have to carry
// JSON tags or pointer fields just for "field was absent" detection.
type fileFormat struct {
	Icons       string   `json:"icons,omitempty"`
	TabBar      bool     `json:"tabBar,omitempty"`
	RecentRoots []string `json:"recentRoots,omitempty"`
}

// DefaultPath returns the canonical config-file location:
// $XDG_CONFIG_HOME/vincent/config.json, falling back to
// ~/.config/vincent/config.json. Returns "" when neither resolves
// — callers should treat that as "use defaults".
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "vincent", "config.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "vincent", "config.json")
}

// Load reads and parses the config file at path, returning a Config
// with defaults filled in for any missing or blank fields.
//
// Contract:
//   - path == ""              → (Defaults(), nil). Treated as "no
//     config configured".
//   - file doesn't exist      → (Defaults(), nil). Same as above.
//   - file unreadable         → (Defaults(), err). Caller can flash a
//     message; editor keeps running on defaults.
//   - file empty / all-blank  → (Defaults(), nil).
//   - unknown icons value     → (Defaults(), err). We'd rather tell
//     the user their config has a typo than silently fall back to
//     defaults and hide the bug.
func Load(path string) (Config, error) {
	cfg := Defaults()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return cfg, nil
	}

	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}

	switch IconsMode(strings.ToLower(strings.TrimSpace(ff.Icons))) {
	case "":
		// field omitted — keep default
	case IconsAuto:
		cfg.Icons = IconsAuto
	case IconsOn:
		cfg.Icons = IconsOn
	case IconsOff:
		cfg.Icons = IconsOff
	default:
		return Defaults(), fmt.Errorf(
			"%s: icons must be %q, %q, or %q (got %q)",
			path, IconsAuto, IconsOn, IconsOff, ff.Icons,
		)
	}
	cfg.TabBar = ff.TabBar
	cfg.RecentRoots = cleanRecentRoots(ff.RecentRoots)
	return cfg, nil
}

// cleanRecentRoots normalises the raw recentRoots array read off disk:
// absolute, cleaned, de-duplicated, capped at maxRecentRoots, and with
// anything that is no longer a directory dropped.
//
// The drop is the interesting half. A recents list is a list of promises —
// every entry claims "clicking me will work" — and a stale entry breaks
// that promise at the worst moment, when the user is trying to get
// somewhere. Filtering on load costs one Stat per entry (ten, once, at
// startup) and means the picker never offers a folder that has been
// deleted, renamed, or unmounted.
func cleanRecentRoots(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if seen[abs] {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
		if len(out) == maxRecentRoots {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AddRecentRoot moves path to the front of c.RecentRoots, de-duplicating and
// capping the list. It is a method on *Config rather than a helper in the app
// package because "most recent first, no repeats, at most ten" is part of the
// field's contract, and a second copy of that rule in the caller is how the
// on-disk list ends up with duplicates.
//
// Unlike Load's cleanRecentRoots this does NOT Stat the path: the caller is
// recording a root it has just successfully switched to, so the directory
// demonstrably exists, and a Stat here would only add a failure mode.
func (c *Config) AddRecentRoot(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	out := make([]string, 0, len(c.RecentRoots)+1)
	out = append(out, path)
	for _, p := range c.RecentRoots {
		if p == path {
			continue
		}
		out = append(out, p)
		if len(out) == maxRecentRoots {
			break
		}
	}
	c.RecentRoots = out
}

// Save writes cfg back to path, creating the parent directory if needed.
//
// The write is atomic — a temp file in the same directory, then a rename —
// because this runs on every root switch, and a half-written config.json is
// one Load will reject with a parse error on the next launch. Same directory
// matters: a rename across filesystems is not atomic, and on Windows not
// even permitted, so the temp file cannot live in TempDir.
//
// path == "" is a silent no-op, matching Load's "no config configured"
// contract — a machine where DefaultPath cannot resolve a home directory
// should not start erroring on every root switch.
//
// Every field is written, including ones the user had omitted. That is
// deliberate: an omitted field and its default are the same config, and
// round-tripping through Config is what keeps Save from having to know
// which keys were present in the file it is replacing.
func Save(path string, cfg Config) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	mode := cfg.Icons
	if mode == "" {
		mode = IconsAuto
	}
	data, err := json.MarshalIndent(fileFormat{
		Icons:       string(mode),
		TabBar:      cfg.TabBar,
		RecentRoots: cfg.RecentRoots,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config.json.*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup: after a successful rename the temp file is gone
	// and the Remove fails harmlessly; after any failure below it is what
	// stops a stray dotfile accumulating next to the config.
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}
