// =============================================================================
// File: internal/review/review.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// The wire format is modelled on tuicr's src/output/markdown.rs
// (generate_markdown plus its line-address formatter). Nothing was copied:
// tuicr is Rust, carries session / PR / commit scoping Vincent has no
// concept of, and makes every section configurable. What crossed over is
// the shape a coding agent parses well — a numbered list of `file:line`
// addresses, each with its code and its note.
// =============================================================================

// Package review is Vincent's review-note model: the comments a reviewer
// writes against a diff, and the plain-text batch that gets handed back to
// the agent that wrote the code.
//
// Pure by design — no tcell, no git, no shell-outs. The app layer owns
// where a comment came from on screen; this package owns what a comment IS
// and how a batch reads once it lands in an agent's prompt. That split is
// what makes the wire format testable to the byte, which matters: it is the
// only part of Vincent another program has to parse.
//
// The one rule that shapes everything here: a comment's anchor is the
// frozen diff Snippet, never a live line number. When the agent rewrites
// the file the line numbers in the batch are already historical — the
// snippet is the durable evidence of what was being talked about. Nothing
// in this package rebases a line number, ever.
package review

import (
	"sort"
	"strconv"
	"strings"
)

// Kind classifies what a reviewer wants done about a comment. The zero
// value is KindNone — an untyped note, which renders with no marker at all
// rather than with a default label the reviewer never chose.
//
// The four types are tuicr's shipped set, deliberately: it is a small
// vocabulary that has already been through real reviews, and an agent can
// hard-code semantics for four words far more reliably than for free text.
type Kind int

const (
	// KindNone is an untyped note. No marker is emitted for it.
	KindNone Kind = iota
	// KindIssue is a blocking problem: the agent must change something.
	KindIssue
	// KindSuggestion is optional — consider it, or decline it with a reason.
	KindSuggestion
	// KindQuestion wants an answer before any code moves.
	KindQuestion
	// KindPraise wants nothing at all. It exists because a review that
	// only ever says "wrong" trains an agent to treat the whole file as
	// suspect.
	KindPraise
)

// Side says which half of a diff a comment's line numbers belong to. The
// zero value is SideNew, which is the overwhelmingly common case — you
// review the code that now exists.
type Side int

const (
	// SideNew addresses lines of the new file (additions and context).
	SideNew Side = iota
	// SideOld addresses lines of the old file (deletions).
	SideOld
)

// kindOrder is the canonical order kinds appear in the legend, independent
// of the order the reviewer happened to create comments in. A legend whose
// order changes between batches is harder to skim than one that doesn't.
var kindOrder = []Kind{KindIssue, KindSuggestion, KindQuestion, KindPraise}

// Tag is the bare uppercase name used in a comment's `**[TAG]**` marker,
// or "" for KindNone.
func (k Kind) Tag() string {
	switch k {
	case KindIssue:
		return "ISSUE"
	case KindSuggestion:
		return "SUGGESTION"
	case KindQuestion:
		return "QUESTION"
	case KindPraise:
		return "PRAISE"
	default:
		return ""
	}
}

// Legend is the tag plus the one-phrase instruction that tells the agent
// what to do about it, as it appears in the batch's `Comment kinds:` line.
// Empty for KindNone, which needs no explanation.
func (k Kind) Legend() string {
	switch k {
	case KindIssue:
		return "ISSUE (must fix)"
	case KindSuggestion:
		return "SUGGESTION (consider)"
	case KindQuestion:
		return "QUESTION (answer before changing)"
	case KindPraise:
		return "PRAISE (no action)"
	default:
		return ""
	}
}

// Next cycles to the following kind, wrapping past KindPraise back to
// KindNone. This is what Tab does in the composer, so the reviewer reaches
// every kind without learning four separate keys.
func (k Kind) Next() Kind {
	if k >= KindPraise || k < KindNone {
		return KindNone
	}
	return k + 1
}

// Comment is one review note anchored to a range of diff lines.
//
// Snippet is the anchor, and the reason the rest of this struct can afford
// to be dumb: it holds the verbatim diff lines (each keeping its leading
// +, -, or space) as they read at the moment the note was written. Start,
// End and Hunk are recorded for the reader's benefit and are never
// recomputed — see the package comment.
type Comment struct {
	// File is repo-relative with forward slashes on every platform, so a
	// batch reads the same whether it was written on Windows or macOS and
	// the agent can paste the path straight into a tool call.
	File string

	// Repo is the absolute root of the repository File is relative TO, and
	// it is never rendered into the batch — the agent receiving the batch
	// already has that repo as its working directory.
	//
	// It exists because File alone stopped being unique in phase 8a: with a
	// folder-of-repos root two repos can both hold "src/main.go", and
	// without this the app could not tell which one a note belonged to when
	// reopening it or when deciding whether it had gone stale. Empty means
	// "the one repo", which is how every single-repo note is recorded and
	// why the wire format is unchanged.
	Repo string

	Side  Side
	Start int // 1-based line on Side.
	End   int // == Start for a single-line comment.

	// Hunk is the index of the diff hunk that owned the range when the
	// note was written. Informational: it survives so a later view can
	// say "this was about the third hunk" without re-deriving it from
	// line numbers that may since have moved.
	Hunk int

	// Snippet is the frozen diff text for Start..End, newline-separated,
	// each line keeping its diff prefix.
	Snippet string

	Kind Kind
	Text string

	// Stale is set by the app when File leaves the changeset — the agent
	// reverted it, committed it, or renamed it. The comment is kept and
	// flagged rather than dropped: the reviewer wrote it, so it is theirs
	// to delete.
	Stale bool
}

// Address renders the comment's line reference the way the batch shows it:
// `file:88`, `file:88-94`, and old-side lines with a `~` on every number
// (`file:~88-~94`) so a deleted line can never be mistaken for a line the
// agent can still go and look at.
func (c Comment) Address() string {
	mark := ""
	if c.Side == SideOld {
		mark = "~"
	}
	if c.End > c.Start {
		return c.File + ":" + mark + strconv.Itoa(c.Start) + "-" + mark + strconv.Itoa(c.End)
	}
	return c.File + ":" + mark + strconv.Itoa(c.Start)
}

// Summary is the one-line form the git panel's footer shows, so the
// reviewer can see what they already said without reopening each diff.
func (c Comment) Summary() string {
	text := strings.TrimSpace(strings.ReplaceAll(c.Text, "\n", " "))
	if text == "" {
		return c.Address()
	}
	return c.Address() + " · " + text
}

// Batch is the set of notes waiting to go back to an agent. A slice rather
// than a map because order only becomes meaningful at render time, where it
// is sorted — the reviewer's clicking order is not something the agent
// should have to reason about.
type Batch struct {
	Comments []Comment
}

// Len reports how many notes are pending. Used for the `Review (N)` header
// and for the "Sent N notes" flash.
func (b Batch) Len() int { return len(b.Comments) }

// itemIndent is how far a comment's snippet and text are inset under its
// numbered heading. Three spaces aligns the body under the `1. ` marker
// for single-digit lists and stays readable past nine.
const itemIndent = "   "

// intro is the batch's first line. It is an instruction rather than a
// greeting because the text arrives in the middle of an agent's prompt
// with no other framing.
const intro = "Please address these review comments."

// Render produces the batch exactly as it lands in the agent's prompt.
//
// The legend line is emitted only when at least one comment is typed, and
// names only the kinds actually used: a legend explaining SUGGESTION and
// PRAISE in a batch of three ISSUEs is noise, and noise in the first two
// lines is where an agent's attention is most expensive.
//
// Comments sort by file then by start line, so a batch reads in the order
// the agent will work through the files rather than in the order the
// reviewer happened to click. The output carries no trailing newline — it
// is pasted into a prompt, not written to a file.
func (b Batch) Render() string {
	if len(b.Comments) == 0 {
		return ""
	}
	items := make([]Comment, len(b.Comments))
	copy(items, b.Comments)
	sort.SliceStable(items, func(i, j int) bool {
		// Repo first so a cross-repo batch does not interleave two files
		// that happen to share a relative path. Empty in the single-repo
		// case, where this leaves the order exactly as it was.
		if items[i].Repo != items[j].Repo {
			return items[i].Repo < items[j].Repo
		}
		if items[i].File != items[j].File {
			return items[i].File < items[j].File
		}
		return items[i].Start < items[j].Start
	})

	out := []string{intro}
	if legend := renderLegend(items); legend != "" {
		out = append(out, legend)
	}
	out = append(out, "", "## Comments", "")

	for i, c := range items {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, renderHeading(i+1, c))
		out = append(out, indentLines(c.Snippet)...)
		out = append(out, indentLines(c.Text)...)
	}
	return strings.Join(out, "\n")
}

// renderLegend builds the `Comment kinds:` line, or "" when every comment
// is untyped.
func renderLegend(items []Comment) string {
	used := map[Kind]bool{}
	for _, c := range items {
		if c.Kind != KindNone {
			used[c.Kind] = true
		}
	}
	if len(used) == 0 {
		return ""
	}
	parts := []string{}
	for _, k := range kindOrder {
		if used[k] {
			parts = append(parts, k.Legend())
		}
	}
	return "Comment kinds: " + strings.Join(parts, ", ")
}

// renderHeading builds one comment's numbered heading: its position in the
// list, its kind marker when it has one, and its backticked address.
func renderHeading(n int, c Comment) string {
	head := strconv.Itoa(n) + "."
	if tag := c.Kind.Tag(); tag != "" {
		head += " **[" + tag + "]**"
	}
	return head + " `" + c.Address() + "`"
}

// indentLines insets every line of s by itemIndent, dropping an empty
// block entirely so a comment with no snippet doesn't leave a blank row
// that reads as a paragraph break.
func indentLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, itemIndent+l)
	}
	return out
}

// pasteStart and pasteEnd frame a bracketed-paste payload.
const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// Sanitize removes every occurrence of the bracketed-paste terminator from
// s, rebuilt byte by byte so a split terminator cannot reassemble itself.
//
// This matters because a review batch carries raw file content: a snippet
// from a file that legitimately contains \x1b[201~ would otherwise close
// the paste frame early, and the rest of the batch would arrive as
// keystrokes in an agent CLI that may be sitting in a vim-style normal mode
// where keystrokes are commands. Scanning for whole terminators in one pass
// would also miss the two-call construction "a\x1b[201\x1b[201~~b", where
// deleting the inner match splices a fresh terminator together; the
// byte-at-a-time rebuild here re-checks the tail after every removal, so
// that case collapses to "ab".
//
// Ported in behaviour (not in code) from herdr-reviewr's pasted(),
// src/herdr.rs:310-323.
func Sanitize(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b = append(b, s[i])
		if len(b) >= len(pasteEnd) && string(b[len(b)-len(pasteEnd):]) == pasteEnd {
			b = b[:len(b)-len(pasteEnd)]
		}
	}
	return string(b)
}

// Wrap frames text as a bracketed paste, sanitized.
//
// Bracketed paste rather than raw bytes because a paste is inserted
// verbatim in every input mode. Raw bytes execute as commands in an agent
// CLI resting in a modal input — the exact failure herdr-reviewr's own
// comment documents.
func Wrap(text string) string {
	return pasteStart + Sanitize(text) + pasteEnd
}
