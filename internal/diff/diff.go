// =============================================================================
// File: internal/diff/diff.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Ported from herdr-sidebar's src/diffview.rs (parse_events + word_ranges).
// That file is Rust and renders through ratatui; only the parsing model
// crossed over. The rendering half lives in internal/editor/diffview.go.
// =============================================================================

// Package diff turns plain `git diff` output into a flat list of rows a
// renderer can walk without knowing anything about unified-diff syntax.
//
// The shape is deliberately dumb: one Row per displayed line, each already
// carrying its old and new line numbers and the rune range of the part that
// actually changed. Everything interesting — pairing deletions with the
// additions that replaced them, working out which characters differ — is
// resolved here, at parse time, so the draw loop stays a straight walk over
// a slice. That matters because the draw loop runs on every mouse move.
//
// We parse plain `git diff` rather than reading git's own colours because
// the look then belongs to Vincent's theme, and because shelling out to
// delta or diff-so-fancy would break the single-static-binary rule.
package diff

import "strings"

// Kind classifies a row for the renderer. The zero value is KindContext,
// which is the safe default: an unrecognised row renders as plain context
// rather than as a spurious addition or deletion.
type Kind int

const (
	// KindContext is an unchanged line, present in both old and new.
	KindContext Kind = iota
	// KindAdded is a line present only in the new file.
	KindAdded
	// KindDeleted is a line present only in the old file.
	KindDeleted
	// KindGap is the elision between two hunks. It carries no text and
	// exists so the renderer can draw a separator instead of silently
	// butting two unrelated regions of the file against each other.
	KindGap
	// KindMeta is a diff line we understood well enough to keep but not
	// to interpret — in practice "Binary files a/x and b/x differ".
	// Rendering it verbatim beats dropping it and showing an empty pane.
	KindMeta
)

// Row is one displayed line of a diff.
//
// Old and New are one-based line numbers in the old and new file, or zero
// when the row does not exist on that side (a deletion has no new number, an
// addition has no old one). Zero rather than -1 because git's own line
// numbers are one-based, so zero is already unambiguously "none".
//
// WordStart and WordEnd bound the changed middle of the line in RUNE
// indices, half-open. They are only set on deletions and additions that were
// paired with each other and that share some material; when there is nothing
// worth tinting they are both zero, which HasWordTint reads as "off".
type Row struct {
	Kind      Kind
	Old       int
	New       int
	Text      string
	WordStart int
	WordEnd   int
}

// HasWordTint reports whether this row has a changed middle worth painting
// in the darker word-level colour. Callers should use this rather than
// comparing the two fields, so the "both zero means off" encoding stays in
// one place.
func (r Row) HasWordTint() bool {
	return r.WordEnd > r.WordStart
}

// skipPrefixes are the diff header lines Vincent never displays. They tell
// the reader nothing they don't already know from the tab title, and
// dropping them means a one-file diff opens on the first real hunk instead
// of on four lines of git plumbing.
var skipPrefixes = []string{
	"diff --git",
	"index ",
	"--- ",
	"+++ ",
	"old mode",
	"new mode",
	"similarity",
	"dissimilarity",
	"rename ",
	"copy ",
	"new file mode",
	"deleted file mode",
	"GIT binary patch",
	// "\ No newline at end of file" — real, but noise in a review.
	`\`,
}

// Parse converts the output of `git diff` into rows ready to render.
//
// It is tolerant by design. Anything it cannot classify becomes a KindMeta
// row rather than an error, and a malformed or absent hunk header leaves the
// line counters where they were instead of aborting the parse. A diff viewer
// that renders four-fifths of a weird diff is far more useful than one that
// refuses the whole thing, and there is no user action that could fix a
// parse failure anyway.
func Parse(out string) []Row {
	rows := []Row{}
	oldNo, newNo := 0, 0
	seenHunk := false

	lines := strings.Split(out, "\n")
	// git diff output ends with a newline, so the split leaves one empty
	// trailing element that is the line TERMINATOR, not a line. Left in, it
	// becomes a phantom blank context row at the end of every diff — and,
	// worse, advances both line counters past the end of the file.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	for _, line := range lines {
		// A CRLF file yields diff lines ending in \r, since git reports the
		// content verbatim and we split on \n. Left in, every row of every
		// CRLF file — most of a .NET or JS repo on Windows — would carry a
		// stray control character at its end, both as a rendered glyph and
		// as a character the word-level tint has to reason about.
		line = strings.TrimSuffix(line, "\r")
		if skipLine(line) {
			continue
		}
		if strings.HasPrefix(line, "@@") {
			oldNo, newNo = applyHunkHeader(line, oldNo, newNo)
			// Only emit a gap BETWEEN hunks — a leading separator above
			// the first hunk would read as "content elided above" when
			// there isn't any.
			if seenHunk {
				rows = append(rows, Row{Kind: KindGap})
			}
			seenHunk = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "-"):
			rows = append(rows, Row{Kind: KindDeleted, Old: oldNo, Text: line[1:]})
			oldNo++
		case strings.HasPrefix(line, "+"):
			rows = append(rows, Row{Kind: KindAdded, New: newNo, Text: line[1:]})
			newNo++
		case strings.HasPrefix(line, " "):
			rows = append(rows, Row{Kind: KindContext, Old: oldNo, New: newNo, Text: line[1:]})
			oldNo++
			newNo++
		case line == "":
			// A blank context line loses its leading space to some tools
			// (and to any pipeline that trims trailing whitespace), so an
			// empty line inside a hunk is context, not a terminator. Only
			// treat it that way once we're actually inside a hunk —
			// otherwise the blank line between two files' headers would
			// inject a phantom row.
			if seenHunk {
				rows = append(rows, Row{Kind: KindContext, Old: oldNo, New: newNo})
				oldNo++
				newNo++
			}
		default:
			rows = append(rows, Row{Kind: KindMeta, Text: line})
		}
	}

	assignWordRanges(rows)
	return rows
}

// skipLine reports whether a raw diff line is header noise we drop.
func skipLine(line string) bool {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// applyHunkHeader reads the line counters out of an "@@ -a,b +c,d @@" header
// and returns the updated old/new cursors. Unparseable fields leave their
// counter untouched rather than resetting it to zero, so a mangled header
// costs the numbers on one hunk instead of every hunk after it.
//
// This is a looser parse than gitstatus.go's parseHunkHeader, on purpose:
// that one needs the counts to build gutter markers and so must reject a
// header it can't fully understand, whereas here we only want the two start
// numbers and can happily ignore the rest of the line.
func applyHunkHeader(line string, oldNo, newNo int) (int, int) {
	for _, tok := range strings.Fields(line) {
		if len(tok) < 2 {
			continue
		}
		sign, rest := tok[0], tok[1:]
		if sign != '-' && sign != '+' {
			continue
		}
		start, ok := atoiPrefix(rest)
		if !ok {
			continue
		}
		if sign == '-' {
			oldNo = start
		} else {
			newNo = start
		}
	}
	return oldNo, newNo
}

// atoiPrefix parses the digits before the first comma of a hunk range field
// ("12,4" -> 12, "7" -> 7). Returns ok=false for anything that isn't a run
// of digits, which is how a malformed header gets ignored rather than
// silently read as line zero.
func atoiPrefix(s string) (int, bool) {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}

// assignWordRanges finds, for each changed line, the span of runes that
// actually differs, and records it on the row in place.
//
// The pairing rule is the one VS Code uses: a run of consecutive deletions
// is matched one-for-one against the run of additions immediately following
// it, and each pair is compared by common prefix and common suffix. It is
// not a real diff algorithm — a line inserted in the middle of a changed
// block will mis-pair the rest of that block. That is an acceptable trade: a
// character-level Myers diff per line costs more than it buys when the
// common case is one line edited in place, and a wrong tint is cosmetic
// while the +/- markers and line numbers stay authoritative.
//
// Pairs that share nothing (empty prefix and suffix) are left untinted:
// highlighting the entire line as "the changed part" says nothing the row
// tint hasn't already said.
func assignWordRanges(rows []Row) {
	i := 0
	for i < len(rows) {
		delStart := i
		for i < len(rows) && rows[i].Kind == KindDeleted {
			i++
		}
		addStart := i
		for i < len(rows) && rows[i].Kind == KindAdded {
			i++
		}
		// Either run empty means there's nothing to pair. Advance past the
		// current row so we can't spin here on a context line.
		if delStart == addStart || addStart == i {
			if i == delStart {
				i++
			}
			continue
		}
		pairs := min(addStart-delStart, i-addStart)
		for k := 0; k < pairs; k++ {
			del := &rows[delStart+k]
			add := &rows[addStart+k]
			oldRunes := []rune(del.Text)
			newRunes := []rune(add.Text)

			prefix := 0
			for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
				prefix++
			}
			suffix := 0
			for suffix < len(oldRunes)-prefix && suffix < len(newRunes)-prefix &&
				oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
				suffix++
			}
			if prefix+suffix == 0 {
				continue
			}
			del.WordStart, del.WordEnd = prefix, len(oldRunes)-suffix
			add.WordStart, add.WordEnd = prefix, len(newRunes)-suffix
		}
	}
}

// Stats counts added and deleted lines, for the status bar's "+12 −3".
func Stats(rows []Row) (added, deleted int) {
	for _, r := range rows {
		switch r.Kind {
		case KindAdded:
			added++
		case KindDeleted:
			deleted++
		}
	}
	return added, deleted
}

// MaxLineNo returns the largest line number appearing on either side, which
// is what the renderer sizes its two gutter columns from. Zero for a diff
// with no numbered rows (a binary-file diff, or an empty one).
func MaxLineNo(rows []Row) int {
	max := 0
	for _, r := range rows {
		if r.Old > max {
			max = r.Old
		}
		if r.New > max {
			max = r.New
		}
	}
	return max
}

// RowForNewLine returns the index of the row showing the given one-based
// line of the NEW file, or -1 when that line isn't in the diff.
//
// This is how a click on the editor's git gutter lands on the right row of
// the diff view. Additions match exactly; context lines match too, so
// clicking just outside a hunk still gets you close. Deletions are skipped
// because they have no new-file line to match against — landing on the
// nearest surviving row is the useful answer there.
func RowForNewLine(rows []Row, line int) int {
	for i, r := range rows {
		if r.New == line && (r.Kind == KindAdded || r.Kind == KindContext) {
			return i
		}
	}
	return -1
}

// FirstChangedRow returns the index of the first addition or deletion, or 0
// when the diff has neither. Used to open a diff on its first real change
// rather than on three lines of leading context.
func FirstChangedRow(rows []Row) int {
	for i, r := range rows {
		if r.Kind == KindAdded || r.Kind == KindDeleted {
			return i
		}
	}
	return 0
}

// Texts returns just the row texts, in order. The renderer feeds this to the
// syntax highlighter and uses it as the diff tab's backing buffer, so the
// existing scroll, clamp, and hit-test code works on a diff unchanged.
func Texts(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out
}
