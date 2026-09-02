// =============================================================================
// File: internal/theme/theme.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-29
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Package theme defines the editor's curated color palette. The editor
// intentionally ships one opinionated dark theme — there is no runtime
// configuration, no theme file, no JSON. To restyle the editor, edit this
// file and recompile. The palette is inspired by Tokyo Night and tuned so
// the syntax colors stay legible against the chrome.
package theme

import "github.com/gdamore/tcell/v2"

// Theme bundles every color the editor renders. UI surfaces, accents, and
// syntax-highlight colors all live in one struct so that adjusting one
// element of the palette can be balanced against the others.
type Theme struct {
	// --- Surfaces ---
	BG        tcell.Color // Editor background. Pure black.
	SidebarBG tcell.Color // File tree / inactive tab background. Also pure black — see Default.
	StatusBG  tcell.Color // Status bar background. Also pure black.
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

	// Conflict is the dirty dot's colour when the tab is not merely dirty
	// but conflicted — the file changed on disk while the buffer had
	// unsaved edits. Deliberately not Modified: at a glance an amber dot
	// and a slightly different amber dot are the same dot, and this state
	// is the one that can lose an agent's work. Red reads as "stop".
	Conflict tcell.Color
}

// Default returns Vincent's palette. It is the only theme shipped —
// calling code can tweak fields on the returned value if it really needs
// to, but there is no theme-loading machinery on purpose.
//
// Every surface is pure black, deliberately. spice-edit shipped a Tokyo
// Night charcoal (0x1a1b26) with a slightly darker sidebar, which is the
// conventional choice; Vincent instead paints black and lets the splitter
// and borders carry the panel separation. Two consequences worth knowing
// before you "fix" this:
//
//   - BG and SidebarBG are intentionally identical. The invariant that
//     replaced "these must differ" is "Subtle must contrast with BG" —
//     the splitter is now the only thing dividing the panes, so if it
//     stops being visible the layout genuinely breaks.
//   - Black is set explicitly rather than via tcell.ColorDefault, which
//     would inherit whatever the host terminal is using and would not
//     reliably be black.
func Default() Theme {
	return Theme{
		// Surfaces — all pure black. See the note above.
		BG:        tcell.NewRGBColor(0x00, 0x00, 0x00),
		SidebarBG: tcell.NewRGBColor(0x00, 0x00, 0x00),
		StatusBG:  tcell.NewRGBColor(0x00, 0x00, 0x00),
		// Raised just enough to read as a highlight against black without
		// becoming a grey slab.
		LineHL: tcell.NewRGBColor(0x12, 0x12, 0x16),

		// Foregrounds & accents.
		Text:  tcell.NewRGBColor(0xc0, 0xca, 0xf5),
		Muted: tcell.NewRGBColor(0x56, 0x5f, 0x89),
		// Lifted from spice-edit's 0x32344a: borders and the splitter now
		// sit on black rather than on charcoal, and at the old value they
		// were close to invisible.
		Subtle: tcell.NewRGBColor(0x3a, 0x3d, 0x55),
		// StatusFG replaces the old inverted status bar. spice-edit drew
		// theme.BG on a solid blue StatusBG; with a black StatusBG that
		// would be black-on-black, so the bar now draws accent text on
		// black instead of a coloured slab.
		StatusFG:    tcell.NewRGBColor(0x7a, 0xa2, 0xf7),
		Accent:      tcell.NewRGBColor(0x7a, 0xa2, 0xf7),
		AccentSoft:  tcell.NewRGBColor(0xbb, 0x9a, 0xf7),
		Selection:   tcell.NewRGBColor(0x33, 0x46, 0x7c),
		Modified:    tcell.NewRGBColor(0xe0, 0xaf, 0x68),
		Error:       tcell.NewRGBColor(0xf7, 0x76, 0x8e),
		GitModified: tcell.NewRGBColor(0xff, 0x9e, 0x64),
		GitAdded:    tcell.NewRGBColor(0x9e, 0xce, 0x6a),
		GitDeleted:  tcell.NewRGBColor(0xf7, 0x76, 0x8e),
		GitRenamed:  tcell.NewRGBColor(0x7d, 0xcf, 0xf7),
		GitMixed:    tcell.NewRGBColor(0xbb, 0x9a, 0xf7),

		// Diff. These are VS Code's dark diff-editor tints, by way of
		// herdr-sidebar — the look Vincent was asked to match. They are
		// deliberately NOT derived from GitAdded / GitDeleted above:
		// those are foreground colours picked to be legible on black,
		// and using them as row backgrounds would drown the code.
		DiffAddBG:     tcell.NewRGBColor(0x20, 0x39, 0x28),
		DiffAddWordBG: tcell.NewRGBColor(0x35, 0x59, 0x3d),
		DiffAddMark:   tcell.NewRGBColor(0x8c, 0xc9, 0x8f),
		DiffDelBG:     tcell.NewRGBColor(0x42, 0x22, 0x26),
		DiffDelWordBG: tcell.NewRGBColor(0x6f, 0x30, 0x36),
		DiffDelMark:   tcell.NewRGBColor(0xd1, 0x6d, 0x76),

		// Find. FindMatch is a desaturated amber so it reads as "all
		// hits" without competing with the syntax palette. FindCurrent
		// is full amber — the same shade the dirty indicator uses —
		// so the active match jumps off the page.
		FindMatch:   tcell.NewRGBColor(0x6f, 0x52, 0x1f),
		FindCurrent: tcell.NewRGBColor(0xe0, 0xaf, 0x68),

		// Tree.
		FolderColor: tcell.NewRGBColor(0x7a, 0xa2, 0xf7),
		FileColor:   tcell.NewRGBColor(0xa9, 0xb1, 0xd6),

		// Syntax — Tokyo Night-ish.
		SynKeyword:  tcell.NewRGBColor(0xbb, 0x9a, 0xf7), // purple
		SynString:   tcell.NewRGBColor(0x9e, 0xce, 0x6a), // green
		SynNumber:   tcell.NewRGBColor(0xff, 0x9e, 0x64), // orange
		SynComment:  tcell.NewRGBColor(0x56, 0x5f, 0x89), // muted slate
		SynFunction: tcell.NewRGBColor(0x7a, 0xa2, 0xf7), // blue
		SynType:     tcell.NewRGBColor(0x2a, 0xc3, 0xde), // cyan
		SynBuiltin:  tcell.NewRGBColor(0xf7, 0x76, 0x8e), // red
		SynVariable: tcell.NewRGBColor(0xc0, 0xca, 0xf5), // text-like
		SynOperator: tcell.NewRGBColor(0x89, 0xdd, 0xff), // light cyan
		SynPunct:    tcell.NewRGBColor(0xa9, 0xb1, 0xd6), // soft text
		SynConstant: tcell.NewRGBColor(0xff, 0x9e, 0x64), // orange

		// Conflict — see the field comment.
		Conflict: tcell.NewRGBColor(0xef, 0x71, 0x77),
	}
}
