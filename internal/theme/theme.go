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
	SidebarBG tcell.Color // File tree / Changes panel background. One shade above BG.
	StatusBG  tcell.Color // Status bar background. Same ground as BG.
	StatusFG  tcell.Color // Status bar text, drawn on StatusBG.
	LineHL    tcell.Color // Active line highlight.

	// --- Foregrounds & accents ---
	Text  tcell.Color // Primary editor text.
	Muted tcell.Color // Line numbers, inactive tabs, secondary UI text.
	// Subtle is structure, not words: splitters, modal frames, rule lines,
	// indent guides, the gap glyph in a diff. It stays dark on purpose so
	// the chrome recedes. Anything a person is meant to READ in a dim tone
	// uses DimText below — the two were one field until 2026-09-02, when
	// raising it for the Changes panel's parent-directory text would have
	// lit up every border in the app.
	Subtle tcell.Color
	// DimText is dim-but-readable words: the Changes panel's parent-directory
	// suffix and its "⋯ more" hint, the review footer's placeholder and
	// "+N more". Above 7:1 on #030405 so it never disappears.
	DimText     tcell.Color
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

	// Conflict is the dirty dot's colour when the tab is not merely dirty
	// but conflicted — the file changed on disk while the buffer had
	// unsaved edits. Deliberately not Modified: at a glance an amber dot
	// and a slightly different amber dot are the same dot, and this state
	// is the one that can lose an agent's work. Red reads as "stop".
	Conflict tcell.Color
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
	// --- Review notes (phase 3) ---
	// Appended at the END of the struct on purpose: a palette rewrite
	// landing at the same time as this feature then merges cleanly
	// instead of colliding in the middle of the type.
	//
	// ReviewBoxBG is the surface behind the inline composer and behind a
	// saved note's marker row — a shade up from LineHL so the box reads as
	// something sitting IN the diff rather than as another highlighted
	// line of it. ReviewBorder draws its frame, ReviewText its content,
	// and ReviewMarker the ▍ bar that leads a saved note. ReviewStale is
	// what a note whose file has left the changeset fades to; it has to
	// stay legible, because a stale note is still the reviewer's words.
	ReviewBoxBG  tcell.Color
	ReviewBorder tcell.Color
	ReviewText   tcell.Color
	ReviewMarker tcell.Color
	ReviewStale  tcell.Color
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
//   - SidebarBG sits one shade above BG (#090a0d over #030405) since
//     2026-09-03, when Chase asked for the side panes to be "very
//     slightly" lighter after seeing 0.6.0. Before that the two were
//     identical and the splitter alone divided the panes. Subtle must
//     still contrast with both grounds, because it is the guides and
//     rules on the sidebar as well as the splitter on the editor.
//   - Every UI blue became lavender on 2026-09-03. The status bar went
//     first (#d2a6ff, Ayu's number colour); Chase then asked for the rest
//     of the blue chrome — Accent, FolderColor, GitRenamed — to follow, and
//     for the purple to be less saturated. All four are #bfaee0 now. Only
//     SynProperty keeps Ayu's blue, because it is syntax, not chrome, and
//     SynNumber/SynBuiltin keep the saturated #d2a6ff for the same reason.
//     Hidden files and folders both dim to Muted.
//   - The ground is set explicitly via tcell.NewRGBColor rather than
//     tcell.ColorDefault, which would inherit whatever the host terminal
//     is using and would not reliably match Ghostty's #030405.
//
// Muted was raised #c3c2be -> #d2d1cd on 2026-09-02: Chase, reading a real
// Ghostty window, found the tree and Changes-panel text too dark. The dim
// words in the Changes panel and review footer moved off Subtle onto the
// new DimText (#969aa0, 7.7:1 on #030405) at the same time; Subtle itself
// stays at #2d2f34 because it is borders and guides, which should recede.
// FileColor, syntax colours, git-status colours, and every background
// stayed put.
func Default() Theme {
	return Theme{
		// Surfaces — Ghostty's ground. See the note above.
		BG:        tcell.NewRGBColor(0x03, 0x04, 0x05),
		SidebarBG: tcell.NewRGBColor(0x09, 0x0a, 0x0d),
		StatusBG:  tcell.NewRGBColor(0x03, 0x04, 0x05),
		// The active line's background, from Chase's Zed settings.
		LineHL: tcell.NewRGBColor(0x18, 0x1a, 0x1e),

		// Foregrounds & accents. Muted raised 2026-09-02 — see the doc
		// comment above.
		Text:    tcell.NewRGBColor(0xdf, 0xde, 0xda),
		Muted:   tcell.NewRGBColor(0xd2, 0xd1, 0xcd),
		Subtle:  tcell.NewRGBColor(0x2d, 0x2f, 0x34),
		DimText: tcell.NewRGBColor(0x96, 0x9a, 0xa0),
		// StatusFG replaces the old inverted status bar. spice-edit drew
		// theme.BG on a solid blue StatusBG; with a near-black StatusBG
		// that would be nearly black-on-black, so the bar draws accent
		// text on the ground instead of a coloured slab.
		StatusFG:    tcell.NewRGBColor(0xbf, 0xae, 0xe0), // lavender, see the note above
		Accent:      tcell.NewRGBColor(0xbf, 0xae, 0xe0), // lavender, was Ayu blue #5ac1fe until 2026-09-03
		AccentSoft:  tcell.NewRGBColor(0xdf, 0xde, 0xda),
		Selection:   tcell.NewRGBColor(0x18, 0x31, 0x41),
		Modified:    tcell.NewRGBColor(0xfe, 0xb4, 0x54),
		Error:       tcell.NewRGBColor(0xef, 0x71, 0x77),
		GitModified: tcell.NewRGBColor(0xfe, 0xb4, 0x54),
		GitAdded:    tcell.NewRGBColor(0xaa, 0xd8, 0x4c),
		GitDeleted:  tcell.NewRGBColor(0xef, 0x71, 0x77),
		GitRenamed:  tcell.NewRGBColor(0xbf, 0xae, 0xe0),
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
		FolderColor: tcell.NewRGBColor(0xbf, 0xae, 0xe0), // folders follow Accent
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

		// Conflict — see the field comment.
		Conflict: tcell.NewRGBColor(0xef, 0x71, 0x77),

		// Review notes (phase 3), recoloured onto Ayu Darker. ReviewBoxBG
		// sits a shade above LineHL so the composer reads as a thing IN the
		// diff rather than another highlighted row, and still clears the
		// red and green row tints it opens on top of.
		ReviewBoxBG:  tcell.NewRGBColor(0x1f, 0x21, 0x27),
		ReviewBorder: tcell.NewRGBColor(0x3e, 0x40, 0x43),
		ReviewText:   tcell.NewRGBColor(0xdf, 0xde, 0xda),
		ReviewMarker: tcell.NewRGBColor(0xfe, 0xb4, 0x54),
		ReviewStale:  tcell.NewRGBColor(0x69, 0x6a, 0x6a),
	}
}
