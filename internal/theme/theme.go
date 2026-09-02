// =============================================================================
// File: internal/theme/theme.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-29
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Package theme defines the editor's curated color palette. The editor
// intentionally ships one opinionated dark theme — there is no runtime
// configuration, no theme file, no JSON. To restyle the editor, edit this
// file and recompile. The palette is Zed's Ayu Darker extension, tuned to
// the way Chase actually runs it (see Default's doc comment for the exact
// sources), and the syntax colors stay legible against that chrome.
package theme

import "github.com/gdamore/tcell/v2"

// Theme bundles every color the editor renders. UI surfaces, accents, and
// syntax-highlight colors all live in one struct so that adjusting one
// element of the palette can be balanced against the others.
type Theme struct {
	// --- Surfaces ---
	BG        tcell.Color // Editor background. See Default's doc comment.
	SidebarBG tcell.Color // File tree / inactive tab background. Same ground as BG.
	StatusBG  tcell.Color // Status bar background. Same ground as BG.
	StatusFG  tcell.Color // Status bar text, drawn on StatusBG.
	LineHL    tcell.Color // Active line highlight.

	// --- Foregrounds & accents ---
	Text        tcell.Color // Primary editor text.
	Muted       tcell.Color // Line numbers, inactive tabs, secondary UI text.
	Subtle      tcell.Color // Even more subtle (separators, hints).
	Accent      tcell.Color // Active tab accent, root label, important UI.
	AccentSoft  tcell.Color // Softer accent (active line number).
	Selection   tcell.Color // Selection background.
	Modified    tcell.Color // Dirty indicator (unsaved changes).
	Error       tcell.Color // Error messages.
	GitModified tcell.Color
	GitAdded    tcell.Color
	GitDeleted  tcell.Color
	GitRenamed  tcell.Color
	GitMixed    tcell.Color

	// --- Diff viewer ---
	// Row tints for the inline diff. The Add/Del pair paints the whole row;
	// the *Word pair paints only the changed middle of a line that was
	// edited in place, which is what makes a one-character change findable
	// in a wall of red and green. The *Mark pair colours the ± column.
	//
	// These are backgrounds, not foregrounds: they sit UNDER syntax
	// highlighting, so they have to stay dark enough that SynComment (the
	// dimmest syntax colour) still reads on top of the word tint.
	DiffAddBG     tcell.Color
	DiffAddWordBG tcell.Color
	DiffAddMark   tcell.Color
	DiffDelBG     tcell.Color
	DiffDelWordBG tcell.Color
	DiffDelMark   tcell.Color

	// FindMatch / FindCurrent paint search hits in the editor body.
	// FindMatch is a soft tint applied to every match in the viewport;
	// FindCurrent is the louder color drawn under the "active" match
	// (the one Enter/Esc-g will jump past) so the user can find their
	// place at a glance.
	FindMatch   tcell.Color
	FindCurrent tcell.Color

	// --- File tree ---
	FolderColor tcell.Color
	FileColor   tcell.Color

	// --- Syntax highlighting ---
	SynKeyword  tcell.Color
	SynString   tcell.Color
	SynNumber   tcell.Color
	SynComment  tcell.Color
	SynFunction tcell.Color
	SynType     tcell.Color
	SynBuiltin  tcell.Color
	SynVariable tcell.Color
	SynOperator tcell.Color
	SynPunct    tcell.Color
	SynConstant tcell.Color

	// --- New in the phase-5 chrome pass. Appended, never inserted, so a
	// sibling agent appending its own fields to this struct at the same
	// time merges cleanly. ---

	// LineNumber is the gutter color for a line that is NOT the cursor's
	// line (AccentSoft above covers the cursor's own row). Split out from
	// Muted deliberately: Muted's other consumers (tree rows, inactive
	// tabs, secondary UI text) read at a brighter value than a gutter of
	// numbers should — a column of eleven Muted digits down the left edge
	// competes with the code next to it. internal/editor/tab.go and
	// internal/editor/diffview.go both read this instead of Muted for the
	// non-cursor gutter style.
	LineNumber tcell.Color

	// RowHover / RowSelected are full-width row background fills for list
	// UIs (today: the file tree). RowHover is deliberately subtle — it
	// says "this is clickable", not "this is selected" (see the Changes
	// panel's own hover, which historically reused LineHL for the same
	// job before this pass gave the tree a dedicated pair). RowSelected is
	// the stronger fill for the active/selected row; the row's git-status
	// or active foreground colour still wins on top of it.
	RowHover    tcell.Color
	RowSelected tcell.Color

	// SynProperty covers struct fields, object properties, HTML/JSX
	// attributes, and tag names — chroma.NameProperty / NameAttribute /
	// NameTag. It didn't have its own field before this pass; those token
	// types fell back to SynType or SynVariable depending on the lexer.
	SynProperty tcell.Color
}

// Default returns Vincent's palette. It is the only theme shipped —
// calling code can tweak fields on the returned value if it really needs
// to, but there is no theme-loading machinery on purpose.
//
// The ground is #030405, not pure black. That's deliberate: it matches
// Chase's actual Ghostty background, and every other value in this
// palette was picked (or, for the handful pulled from a spec, verified)
// against that ground rather than against #000000 — a terminal cell has
// no alpha, so a color tuned for a literal-black background can read
// slightly wrong once it's sitting on #030405 instead. The palette itself
// is Zed's Ayu Darker theme extension plus the overrides Chase actually
// runs, read on 2026-09-02 from:
//
//   - ~/Library/Application Support/Zed/extensions/installed/ayu-darker/themes/ayu-darker.json
//   - ~/.config/zed/settings.json
//
// Two consequences worth knowing before you "fix" this:
//
//   - BG and SidebarBG are intentionally identical. The invariant that
//     replaced "these must differ" is "Subtle must contrast with BG" —
//     the splitter is now the only thing dividing the panes, so if it
//     stops being visible the layout genuinely breaks.
//   - The ground is set explicitly via tcell.NewRGBColor rather than
//     tcell.ColorDefault, which would inherit whatever the host terminal
//     is using and would not reliably match Ghostty's #030405.
func Default() Theme {
	return Theme{
		// Surfaces — Ghostty's ground. See the note above.
		BG:        tcell.NewRGBColor(0x03, 0x04, 0x05),
		SidebarBG: tcell.NewRGBColor(0x03, 0x04, 0x05),
		StatusBG:  tcell.NewRGBColor(0x03, 0x04, 0x05),
		// The active line's background, from Chase's Zed settings.
		LineHL: tcell.NewRGBColor(0x18, 0x1a, 0x1e),

		// Foregrounds & accents.
		Text:   tcell.NewRGBColor(0xdf, 0xde, 0xda),
		Muted:  tcell.NewRGBColor(0xc3, 0xc2, 0xbe),
		Subtle: tcell.NewRGBColor(0x2d, 0x2f, 0x34),
		// StatusFG replaces the old inverted status bar. spice-edit drew
		// theme.BG on a solid blue StatusBG; with a near-black StatusBG
		// that would be nearly black-on-black, so the bar draws accent
		// text on the ground instead of a coloured slab.
		StatusFG:    tcell.NewRGBColor(0x5a, 0xc1, 0xfe),
		Accent:      tcell.NewRGBColor(0x5a, 0xc1, 0xfe),
		AccentSoft:  tcell.NewRGBColor(0xdf, 0xde, 0xda),
		Selection:   tcell.NewRGBColor(0x18, 0x31, 0x41),
		Modified:    tcell.NewRGBColor(0xfe, 0xb4, 0x54),
		Error:       tcell.NewRGBColor(0xef, 0x71, 0x77),
		GitModified: tcell.NewRGBColor(0xfe, 0xb4, 0x54),
		GitAdded:    tcell.NewRGBColor(0xaa, 0xd8, 0x4c),
		GitDeleted:  tcell.NewRGBColor(0xef, 0x71, 0x77),
		GitRenamed:  tcell.NewRGBColor(0x5a, 0xc1, 0xfe),
		GitMixed:    tcell.NewRGBColor(0xfe, 0xb4, 0x54),

		// Diff. Ayu Darker's add/del tints, blended over #030405 rather
		// than eyeballed against pure black.
		DiffAddBG:     tcell.NewRGBColor(0x1e, 0x26, 0x10),
		DiffAddWordBG: tcell.NewRGBColor(0x35, 0x44, 0x1a),
		DiffAddMark:   tcell.NewRGBColor(0xaa, 0xd8, 0x4c),
		DiffDelBG:     tcell.NewRGBColor(0x29, 0x15, 0x17),
		DiffDelWordBG: tcell.NewRGBColor(0x4a, 0x25, 0x27),
		DiffDelMark:   tcell.NewRGBColor(0xef, 0x71, 0x77),

		// Find. FindMatch is a desaturated amber so it reads as "all
		// hits" without competing with the syntax palette. FindCurrent
		// is full amber — the same shade the dirty indicator uses —
		// so the active match jumps off the page.
		FindMatch:   tcell.NewRGBColor(0x6f, 0x52, 0x1f),
		FindCurrent: tcell.NewRGBColor(0xfe, 0xb4, 0x54),

		// Tree. FileColor deliberately matches Text rather than Muted — a
		// plain file name is primary content, not secondary UI chrome, and
		// it has to stay visually distinct from Muted (dotfiles, line
		// numbers) or the dotfile-dimming cue disappears.
		FolderColor: tcell.NewRGBColor(0x5a, 0xc1, 0xfe),
		FileColor:   tcell.NewRGBColor(0xdf, 0xde, 0xda),

		// Syntax — Ayu Darker.
		SynKeyword:  tcell.NewRGBColor(0xff, 0x77, 0x33), // orange
		SynString:   tcell.NewRGBColor(0xa9, 0xd9, 0x4b), // green
		SynNumber:   tcell.NewRGBColor(0xd2, 0xa6, 0xff), // purple (shared with boolean, see SynBuiltin)
		SynComment:  tcell.NewRGBColor(0x5c, 0x67, 0x73), // slate
		SynFunction: tcell.NewRGBColor(0xff, 0xb4, 0x54), // amber
		SynType:     tcell.NewRGBColor(0x59, 0xb4, 0xc2), // cyan
		// SynBuiltin doubles as "boolean" — chroma tags true/false/nil as
		// NameBuiltinPseudo, and the source spec gives number and boolean
		// literals the same purple, so this and SynNumber share a value.
		SynBuiltin:  tcell.NewRGBColor(0xd2, 0xa6, 0xff), // purple
		SynVariable: tcell.NewRGBColor(0xdf, 0xde, 0xda), // text-like
		SynOperator: tcell.NewRGBColor(0xfe, 0x8f, 0x40), // orange
		SynPunct:    tcell.NewRGBColor(0xa6, 0xa5, 0xa0), // soft text
		SynConstant: tcell.NewRGBColor(0xff, 0xee, 0x99), // yellow

		// Phase-5 additions — see the field doc comments.
		LineNumber:  tcell.NewRGBColor(0x45, 0x45, 0x43),
		RowHover:    tcell.NewRGBColor(0x2d, 0x2f, 0x34),
		RowSelected: tcell.NewRGBColor(0x3e, 0x40, 0x43),
		SynProperty: tcell.NewRGBColor(0x5a, 0xc1, 0xfe),
	}
}
