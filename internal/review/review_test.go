// =============================================================================
// File: internal/review/review_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package review

import (
	"strings"
	"testing"
)

// TestBatchRender_Empty pins that an empty batch renders as the empty
// string rather than as a header with nothing under it. The app uses this
// to decide there is nothing to send.
func TestBatchRender_Empty(t *testing.T) {
	if got := (Batch{}).Render(); got != "" {
		t.Fatalf("empty batch rendered %q, want \"\"", got)
	}
}

// TestBatchRender_UntypedSingle pins the exact bytes of the simplest
// batch: one untyped comment on one new-side line. No legend line, no
// `**[KIND]**` marker, snippet and text both inset three spaces.
func TestBatchRender_UntypedSingle(t *testing.T) {
	b := Batch{Comments: []Comment{{
		File:    "internal/app/app.go",
		Start:   88,
		End:     88,
		Snippet: "+\tif err == nil {",
		Text:    "this inverts the check",
	}}}

	want := strings.Join([]string{
		"Please address these review comments.",
		"",
		"## Comments",
		"",
		"1. `internal/app/app.go:88`",
		"   +\tif err == nil {",
		"   this inverts the check",
	}, "\n")

	if got := b.Render(); got != want {
		t.Fatalf("render mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBatchRender_LegendNamesOnlyUsedKinds pins the legend rule: it appears
// because at least one comment is typed, and it names ISSUE and QUESTION
// only — never SUGGESTION or PRAISE, which this batch does not use.
func TestBatchRender_LegendNamesOnlyUsedKinds(t *testing.T) {
	b := Batch{Comments: []Comment{
		{File: "a.go", Start: 1, End: 1, Kind: KindQuestion, Snippet: " one", Text: "why?"},
		{File: "a.go", Start: 2, End: 2, Kind: KindIssue, Snippet: " two", Text: "fix"},
		{File: "a.go", Start: 3, End: 3, Snippet: " three", Text: "note"},
	}}

	got := b.Render()
	wantLegend := "Comment kinds: ISSUE (must fix), QUESTION (answer before changing)"
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || lines[1] != wantLegend {
		t.Fatalf("legend line = %q, want %q", lines[1], wantLegend)
	}
	if strings.Contains(got, "SUGGESTION") || strings.Contains(got, "PRAISE") {
		t.Errorf("legend named an unused kind:\n%s", got)
	}
}

// TestBatchRender_NoLegendWhenAllUntyped pins the other half of that rule:
// a batch of untyped notes gets no legend, so line two is the blank before
// the `## Comments` header.
func TestBatchRender_NoLegendWhenAllUntyped(t *testing.T) {
	b := Batch{Comments: []Comment{
		{File: "a.go", Start: 1, End: 1, Snippet: " one", Text: "note"},
	}}
	lines := strings.Split(b.Render(), "\n")
	if lines[1] != "" {
		t.Fatalf("line 2 = %q, want a blank line (no legend)", lines[1])
	}
	if strings.Contains(b.Render(), "Comment kinds:") {
		t.Error("untyped batch should carry no legend line")
	}
}

// TestBatchRender_SortsByFileThenStart pins the ordering contract: files
// alphabetically, lines ascending inside a file, regardless of the order
// the reviewer created the notes in.
func TestBatchRender_SortsByFileThenStart(t *testing.T) {
	b := Batch{Comments: []Comment{
		{File: "z.go", Start: 5, End: 5, Text: "third"},
		{File: "a.go", Start: 90, End: 90, Text: "second"},
		{File: "a.go", Start: 12, End: 12, Text: "first"},
	}}

	got := b.Render()
	order := []string{"`a.go:12`", "`a.go:90`", "`z.go:5`"}
	at := -1
	for _, want := range order {
		i := strings.Index(got, want)
		if i < 0 {
			t.Fatalf("%s missing from render:\n%s", want, got)
		}
		if i < at {
			t.Fatalf("%s out of order:\n%s", want, got)
		}
		at = i
	}
	if !strings.Contains(got, "1. `a.go:12`") || !strings.Contains(got, "3. `z.go:5`") {
		t.Errorf("numbering does not follow the sort:\n%s", got)
	}
}

// TestBatchRender_RangesAndOldSide pins the two address shapes that are
// easy to get wrong: an inclusive range, and an old-side range where EVERY
// number carries the ~ prefix.
func TestBatchRender_RangesAndOldSide(t *testing.T) {
	b := Batch{Comments: []Comment{
		{File: "a.go", Side: SideNew, Start: 72, End: 80, Kind: KindSuggestion, Text: "collapse"},
		{File: "b.go", Side: SideOld, Start: 88, End: 94, Text: "why was this dropped"},
		{File: "c.go", Side: SideOld, Start: 5, End: 5, Text: "single old line"},
	}}

	got := b.Render()
	for _, want := range []string{
		"1. **[SUGGESTION]** `a.go:72-80`",
		"2. `b.go:~88-~94`",
		"3. `c.go:~5`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestBatchRender_BlankLineBetweenItems pins the one-blank-line separator,
// and that a multi-line snippet keeps every line indented.
func TestBatchRender_BlankLineBetweenItems(t *testing.T) {
	b := Batch{Comments: []Comment{
		{File: "a.go", Start: 1, End: 2, Snippet: "-old\n+new", Text: "one"},
		{File: "b.go", Start: 1, End: 1, Snippet: " ctx", Text: "two"},
	}}

	want := strings.Join([]string{
		"Please address these review comments.",
		"",
		"## Comments",
		"",
		"1. `a.go:1-2`",
		"   -old",
		"   +new",
		"   one",
		"",
		"2. `b.go:1`",
		"    ctx",
		"   two",
	}, "\n")

	if got := b.Render(); got != want {
		t.Fatalf("render mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBatchRender_NoTrailingNewline pins that the payload ends on real
// content. The batch is pasted into a prompt, where a trailing newline is
// at best noise and at worst a submit in some agent CLIs.
func TestBatchRender_NoTrailingNewline(t *testing.T) {
	b := Batch{Comments: []Comment{{File: "a.go", Start: 1, End: 1, Text: "x"}}}
	if got := b.Render(); strings.HasSuffix(got, "\n") {
		t.Fatalf("render ends with a newline: %q", got)
	}
}

// TestBatchRender_EmptyTextAndSnippetSkipBlocks pins that a comment with no
// snippet (or no text) emits no blank indented row for the missing half.
func TestBatchRender_EmptyTextAndSnippetSkipBlocks(t *testing.T) {
	b := Batch{Comments: []Comment{{File: "a.go", Start: 1, End: 1, Kind: KindPraise}}}
	want := strings.Join([]string{
		"Please address these review comments.",
		"Comment kinds: PRAISE (no action)",
		"",
		"## Comments",
		"",
		"1. **[PRAISE]** `a.go:1`",
	}, "\n")
	if got := b.Render(); got != want {
		t.Fatalf("render mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestKindNext_CyclesThroughAllAndWraps pins the composer's Tab cycle:
// None → Issue → Suggestion → Question → Praise → None.
func TestKindNext_CyclesThroughAllAndWraps(t *testing.T) {
	want := []Kind{KindIssue, KindSuggestion, KindQuestion, KindPraise, KindNone}
	k := KindNone
	for i, expect := range want {
		k = k.Next()
		if k != expect {
			t.Fatalf("step %d: got kind %d, want %d", i, k, expect)
		}
	}
}

// TestKindTagAndLegend pins the two label forms, including that KindNone
// produces neither — that is what keeps an untyped note unmarked.
func TestKindTagAndLegend(t *testing.T) {
	if got := KindNone.Tag(); got != "" {
		t.Errorf("KindNone.Tag() = %q, want empty", got)
	}
	if got := KindNone.Legend(); got != "" {
		t.Errorf("KindNone.Legend() = %q, want empty", got)
	}
	if got := KindIssue.Tag(); got != "ISSUE" {
		t.Errorf("KindIssue.Tag() = %q", got)
	}
	if got := KindQuestion.Legend(); got != "QUESTION (answer before changing)" {
		t.Errorf("KindQuestion.Legend() = %q", got)
	}
}

// TestCommentSummary pins the footer's one-line form, including the
// fallback for a note whose text is empty.
func TestCommentSummary(t *testing.T) {
	c := Comment{File: "a.go", Start: 4, End: 4, Text: "first\nsecond"}
	if got, want := c.Summary(), "a.go:4 · first second"; got != want {
		t.Errorf("Summary() = %q, want %q", got, want)
	}
	bare := Comment{File: "a.go", Start: 4, End: 4}
	if got, want := bare.Summary(), "a.go:4"; got != want {
		t.Errorf("bare Summary() = %q, want %q", got, want)
	}
}

// TestSanitize_StripsTerminator pins the plain case: an embedded
// bracketed-paste terminator is removed and everything around it survives.
func TestSanitize_StripsTerminator(t *testing.T) {
	got := Sanitize("before\x1b[201~after")
	if got != "beforeafter" {
		t.Fatalf("Sanitize = %q, want %q", got, "beforeafter")
	}
}

// TestSanitize_DefeatsSplitTerminator is the security case herdr-reviewr's
// comment names: two partial terminators that would splice into a real one
// if removal were a single pass. Byte-at-a-time rebuild must collapse it.
func TestSanitize_DefeatsSplitTerminator(t *testing.T) {
	got := Sanitize("a\x1b[201\x1b[201~~b")
	if got != "ab" {
		t.Fatalf("Sanitize = %q, want %q", got, "ab")
	}
}

// TestSanitize_LeavesOtherEscapesAlone pins that we strip only the
// terminator. A diff snippet full of escape sequences is still valid
// content and must arrive intact.
func TestSanitize_LeavesOtherEscapesAlone(t *testing.T) {
	in := "\x1b[200~\x1b[31mred\x1b[0m"
	if got := Sanitize(in); got != in {
		t.Fatalf("Sanitize altered unrelated escapes: %q", got)
	}
}

// TestWrap_FramesSanitizedBody pins that Wrap frames the payload and that
// the body it frames is already sanitized — so the result contains exactly
// one terminator, at the very end.
func TestWrap_FramesSanitizedBody(t *testing.T) {
	got := Wrap("a\x1b[201~b")
	want := "\x1b[200~ab\x1b[201~"
	if got != want {
		t.Fatalf("Wrap = %q, want %q", got, want)
	}
	if n := strings.Count(got, "\x1b[201~"); n != 1 {
		t.Fatalf("wrapped payload has %d terminators, want 1", n)
	}
}
