// =============================================================================
// File: internal/app/frame.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// frame.go is the "should we repaint at all" half of the event loop. It has
// no upstream ancestor — spice-edit redrew unconditionally, once per event.
//
// The reason it exists: Vincent asks the terminal for any-motion mouse
// reporting (xterm mode 1003, see EnableMouse in app.go), so the pointer
// crossing the editor pane produces one event per cell — around 60 a second.
// Every one of those used to run draw() + Show(), and because every Vincent
// painter fills its rect with spaces and then writes glyphs on top, tcell's
// dirty tracking is defeated and the whole screen is re-transmitted. Measured
// against tcell v2.13.9 at 200x50 that is ~56 KB of escape sequences for a
// frame that is byte-identical to the one before it. Inside herdr — whose PTY
// reader takes 8 KB at a time and whose renderer runs on a 16 ms clock — those
// oversized frames get sampled mid-write and the pane visibly flickers.
//
// The fix is not to make the frame cheaper (that is frame B, the painters) but
// to not send it: a pure-motion event that changed nothing observable does not
// earn a repaint.
//
// Two rules make that safe:
//
//   - Only a PURE MOTION event can be skipped. Anything else — key, resize,
//     wheel, click, drag, or one of our own posted events — always repaints.
//     That keeps the skip logic from having to know about every state change
//     in the app, which is where a "did anything change" test rots.
//
//   - The comparison is against the state as of the LAST PAINTED FRAME, not
//     against the state just before this event. That is what makes
//     time-dependent content work: the status bar's flash and the armed-Esc
//     leader hint both expire on a clock nothing posts an event for, so the
//     first motion event after they lapse must still repaint. Snapshotting
//     "before" and "after" the event would find both sides already expired and
//     leave stale text on screen.

package app

import (
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/chasereyn/vincent/internal/editor"
)

// frameKey is a comparable snapshot of everything a pure-motion event is
// allowed to change on screen, plus the two pieces of status-bar content that
// change on a timer.
//
// It is a struct rather than a scatter of "dirty" flags on App so that adding
// state is one field here and one line in frameKey() — a flag has to be set at
// every mutation site and is silently wrong the day someone forgets one.
// Everything in it must be comparable with ==: no slices, no maps. Fields that
// a motion event cannot reach (width, the modal-open bools) are included
// anyway; they cost one word each and they mean the guard fails safe if a
// future handler starts touching them.
type frameKey struct {
	width, height int

	sidebarShown bool
	sidebarWidth int

	gitPanelShown  bool
	gitPanelWidth  int
	gitPanelHover  int
	gitPanelScroll int
	gitEntryCount  int
	gitBranch      string

	cheatsheetOpen bool

	contextOpen  bool
	contextHover int

	confirmOpen       bool
	confirmHover      int
	confirmInfoScroll int

	dirtyOpen  bool
	dirtyHover int

	promptOpen   bool
	promptCursor int
	promptScroll int

	finderOpen     bool
	finderSelected int
	finderScroll   int
	finderCount    int

	// Root picker. Its hover highlight follows the pointer, so a motion
	// event that moves the highlight has to cost a frame — that is the
	// whole contract of this struct.
	rootPickerOpen       bool
	rootPickerSelected   int
	rootPickerScroll     int
	rootPickerListScroll int
	rootPickerCount      int
	rootPickerQuery      string

	findOpen           bool
	findCursor         int
	findScroll         int
	findReplaceVisible bool
	findReplaceFocus   bool
	findReplaceCursor  int
	findReplaceScroll  int

	dragMode  string
	activeTab int
	tabCount  int

	// Active-tab state. The status bar reads the cursor position and the
	// dirty dot off it, and the editor pane repaints on any scroll.
	tabPath   string
	tabMode   string
	tabDirty  bool
	cursor    editor.Position
	anchor    editor.Position
	scrollX   int
	scrollY   int
	lineCount int
	findIndex int

	// Time-dependent status-bar content. See the file comment.
	leaderArmed bool
	flashLive   bool
	statusMsg   string
}

// frameKey samples the current observable state. Called once per painted
// frame (recorded as the baseline) and again after each pure-motion event
// (compared against that baseline).
func (a *App) frameKey() frameKey {
	k := frameKey{
		width:             a.width,
		height:            a.height,
		sidebarShown:      a.sidebarShown,
		sidebarWidth:      a.sidebarWidth,
		gitPanelShown:     a.gitPanelShown,
		gitPanelWidth:     a.gitPanelWidth,
		gitPanelHover:     a.gitPanelHover,
		gitPanelScroll:    a.gitPanelScroll,
		gitEntryCount:     len(a.gitSnap.Entries),
		gitBranch:         a.gitBranch,
		cheatsheetOpen:    a.cheatsheetOpen,
		contextOpen:       a.contextOpen,
		contextHover:      a.contextHover,
		confirmOpen:       a.confirmOpen,
		confirmHover:      a.confirmHover,
		confirmInfoScroll: a.confirmInfoScroll,
		dirtyOpen:         a.dirtyOpen,
		dirtyHover:        a.dirtyHover,
		promptOpen:        a.promptOpen,
		promptCursor:      a.promptCursor,
		promptScroll:      a.promptScroll,
		finderOpen:        a.finderOpen,
		finderSelected:    a.finderSelected,
		finderScroll:      a.finderScroll,
		finderCount:       len(a.finderResults),

		rootPickerOpen:       a.rootPicker.open,
		rootPickerSelected:   a.rootPicker.selected,
		rootPickerScroll:     a.rootPicker.scroll,
		rootPickerListScroll: a.rootPicker.listScroll,
		rootPickerCount:      len(a.rootPicker.rows),
		rootPickerQuery:      string(a.rootPicker.query),

		findOpen:           a.findOpen,
		findCursor:         a.findCursor,
		findScroll:         a.findScroll,
		findReplaceVisible: a.findReplaceVisible,
		findReplaceFocus:   a.findReplaceFocus,
		findReplaceCursor:  a.findReplaceCursor,
		findReplaceScroll:  a.findReplaceScroll,
		dragMode:           a.dragMode,
		activeTab:          a.activeTab,
		tabCount:           len(a.tabs),
		leaderArmed:        a.leaderArmed(),
		statusMsg:          a.statusMsg,
		flashLive:          a.statusMsg != "" && time.Now().Before(a.statusUntil),
	}
	if tab := a.activeTabPtr(); tab != nil {
		k.tabPath = tab.Path
		k.tabMode = tab.Mode
		k.tabDirty = tab.Dirty
		k.cursor = tab.Cursor
		k.anchor = tab.Anchor
		k.scrollX = tab.ScrollX
		k.scrollY = tab.ScrollY
		k.findIndex = tab.FindIndex
		if tab.Buffer != nil {
			k.lineCount = tab.Buffer.LineCount()
		}
	}
	return k
}

// isPureMotion reports whether ev is a mouse event that only moved the
// pointer: no button held, no wheel notch.
//
// tcell folds the wheel into the button mask, so ButtonNone already excludes
// WheelUp/Down/Left/Right — the explicit wheel test below is belt and braces
// against a future tcell that reports them separately, since letting a wheel
// event through the "nothing changed" path would eat a scroll.
func isPureMotion(ev tcell.Event) bool {
	me, ok := ev.(*tcell.EventMouse)
	if !ok {
		return false
	}
	btn := me.Buttons()
	if btn&(tcell.WheelUp|tcell.WheelDown|tcell.WheelLeft|tcell.WheelRight) != 0 {
		return false
	}
	return btn == tcell.ButtonNone
}

// handleEventForFrame dispatches one event and reports whether the screen
// needs repainting because of it.
//
// Non-motion events always answer true: see the file comment for why the
// cheap test is deliberately limited to motion.
func (a *App) handleEventForFrame(ev tcell.Event) bool {
	motion := isPureMotion(ev)
	a.handleEvent(ev)
	if !motion {
		return true
	}
	return a.frameKey() != a.lastFrame
}

// paint draws the screen and flushes it. draw() itself records the frame key
// and bumps the frame counter, so a test that calls draw() directly still
// leaves the baseline matching what is on screen.
func (a *App) paint() {
	a.draw()
	a.screen.Show()
}
