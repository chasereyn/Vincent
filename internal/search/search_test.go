// =============================================================================
// File: internal/search/search_test.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mustWrite creates parent directories and writes body to root/rel. Local
// to this package so tests read as the scenario they pin rather than
// mkdir+write boilerplate — internal/finder's index_test.go has the same
// helper for the same reason; the packages don't share test code.
func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// collect drains a Match channel to a slice. Every test either lets
// Search run to completion (so this always terminates) or cancels ctx
// first (ditto, once Search's own goroutines notice).
func collect(ch <-chan Match) []Match {
	var out []Match
	for m := range ch {
		out = append(out, m)
	}
	return out
}

// runSearch runs Search to completion and returns both the matches and
// the Outcome, so most tests can assert on both in one call instead of
// juggling onDone plumbing themselves.
func runSearch(t *testing.T, files []string, root, query string, opts Options) ([]Match, Outcome) {
	t.Helper()
	var outcome Outcome
	done := make(chan struct{})
	ch := Search(context.Background(), files, root, query, opts, func(o Outcome) {
		outcome = o
		close(done)
	})
	matches := collect(ch)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onDone never fired")
	}
	return matches, outcome
}

// TestSearch_SubstringCaseInsensitive pins the default matcher: a plain
// query matches regardless of case, and reports the right line and
// column.
func TestSearch_SubstringCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.go", "package main\nfunc Hello() {}\n")

	matches, outcome := runSearch(t, []string{"a.go"}, dir, "HELLO", Options{})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %+v", len(matches), matches)
	}
	m := matches[0]
	if m.Path != "a.go" || m.Line != 2 {
		t.Fatalf("match: got path=%q line=%d, want a.go line=2", m.Path, m.Line)
	}
	// "func " is 5 runes, so "Hello" starts at column 6 (1-based).
	if m.Col != 6 {
		t.Fatalf("Col: got %d, want 6", m.Col)
	}
	if m.MatchStart != 5 || m.MatchLen != 5 {
		t.Fatalf("MatchStart/MatchLen: got %d/%d, want 5/5 (0-based span of \"Hello\" in %q)", m.MatchStart, m.MatchLen, m.Text)
	}
	gotSpan := []rune(m.Text)[m.MatchStart : m.MatchStart+m.MatchLen]
	if string(gotSpan) != "Hello" {
		t.Fatalf("span from MatchStart/MatchLen: got %q, want %q", string(gotSpan), "Hello")
	}
	if outcome.Matches != 1 || outcome.FilesScanned != 1 || outcome.Capped {
		t.Fatalf("outcome: got %+v", outcome)
	}
}

// TestSearch_Regexp pins the "re:" prefix triggering regexp mode, and
// that the prefix itself is stripped before compiling.
func TestSearch_Regexp(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.go", "foo1\nfoo2\nbar3\n")

	matches, _ := runSearch(t, []string{"a.go"}, dir, "re:foo[0-9]", Options{})
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(matches), matches)
	}
	if matches[0].Line != 1 || matches[1].Line != 2 {
		t.Fatalf("expected matches on lines 1 and 2, got %+v", matches)
	}
}

// TestSearch_RegexpCaseSensitive pins that regexp mode does NOT fold case
// the way the substring path does — that's grep -E's normal behaviour,
// and a query that wants case-insensitivity can ask for it explicitly
// with (?i).
func TestSearch_RegexpCaseSensitive(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.go", "Hello\nhello\n")

	matches, _ := runSearch(t, []string{"a.go"}, dir, "re:hello", Options{})
	if len(matches) != 1 || matches[0].Line != 2 {
		t.Fatalf("expected exactly the lowercase match, got %+v", matches)
	}
}

// TestSearch_InvalidRegexpReturnsEmpty pins that a broken "re:" pattern
// fails soft: a closed empty channel and a zero Outcome, not a panic.
func TestSearch_InvalidRegexpReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.go", "foo\n")

	matches, outcome := runSearch(t, []string{"a.go"}, dir, "re:(unclosed", Options{})
	if len(matches) != 0 {
		t.Fatalf("expected no matches for invalid regexp, got %+v", matches)
	}
	if outcome != (Outcome{}) {
		t.Fatalf("expected zero Outcome, got %+v", outcome)
	}
}

// TestSearch_SkipsBinary pins the NUL-byte sniff: a file with a NUL in
// its first 8KB is never scanned, even though the "text" surrounding the
// NUL contains the query.
func TestSearch_SkipsBinary(t *testing.T) {
	dir := t.TempDir()
	body := "needle\x00moreneedle"
	if err := os.WriteFile(filepath.Join(dir, "bin.dat"), []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustWrite(t, dir, "text.txt", "needle\n")

	matches, outcome := runSearch(t, []string{"bin.dat", "text.txt"}, dir, "needle", Options{})
	if len(matches) != 1 || matches[0].Path != "text.txt" {
		t.Fatalf("expected only the text file to match, got %+v", matches)
	}
	if outcome.FilesScanned != 1 {
		t.Fatalf("FilesScanned: got %d, want 1 (binary file shouldn't count)", outcome.FilesScanned)
	}
}

// TestSearch_SkipsOversizedFiles pins the 1MB size cap: a file over the
// limit is skipped outright, never opened for scanning.
func TestSearch_SkipsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxFileSize+1)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustWrite(t, dir, "small.txt", "x\n")

	matches, outcome := runSearch(t, []string{"big.txt", "small.txt"}, dir, "x", Options{})
	for _, m := range matches {
		if m.Path == "big.txt" {
			t.Fatal("oversized file should never be scanned")
		}
	}
	if outcome.FilesScanned != 1 {
		t.Fatalf("FilesScanned: got %d, want 1", outcome.FilesScanned)
	}
}

// TestSearch_CancellationStopsWorkers pins that cancelling ctx actually
// stops the worker pool rather than letting it run to completion in the
// background: with the context cancelled before Search is even called,
// no file should be reported as scanned.
func TestSearch_CancellationStopsWorkers(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := 0; i < 50; i++ {
		name := "f" + string(rune('a'+i%26)) + ".txt"
		mustWrite(t, dir, name, strings.Repeat("needle\n", 1000))
		files = append(files, name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Search even starts a worker

	var outcome Outcome
	var mu sync.Mutex
	done := make(chan struct{})
	ch := Search(ctx, files, dir, "needle", Options{}, func(o Outcome) {
		mu.Lock()
		outcome = o
		mu.Unlock()
		close(done)
	})
	matches := collect(ch)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onDone never fired after cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(matches) != 0 {
		t.Fatalf("expected no matches after up-front cancellation, got %d", len(matches))
	}
	if outcome.FilesScanned != 0 {
		t.Fatalf("FilesScanned after cancellation: got %d, want 0", outcome.FilesScanned)
	}
}

// TestSearch_CancellationMidRunStopsPromptly starts a real search over
// many matching files and cancels shortly after matches start arriving,
// then asserts the search actually finishes quickly rather than running
// every file to completion — the behavioural point of ctx cancellation,
// as opposed to TestSearch_CancellationStopsWorkers's "never started at
// all" case.
func TestSearch_CancellationMidRunStopsPromptly(t *testing.T) {
	dir := t.TempDir()
	var files []string
	for i := 0; i < 8; i++ {
		name := "f" + string(rune('a'+i)) + ".txt"
		mustWrite(t, dir, name, strings.Repeat("needle\n", 200000))
		files = append(files, name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ch := Search(ctx, files, dir, "needle", Options{Workers: 1}, func(Outcome) { close(done) })

	<-ch // first match
	cancel()
	// Drain the rest; this must complete promptly once cancellation
	// propagates, not run all 8 files' worth of matches to completion.
	go func() {
		for range ch {
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("search did not stop promptly after mid-run cancellation")
	}
}

// TestSearch_CapStopsAtLimit pins the 2000-match cap and the Capped flag:
// a file with far more matches than the cap should still report exactly
// the cap, not the true count, and Capped should be true.
func TestSearch_CapStopsAtLimit(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", strings.Repeat("needle\n", 50))

	matches, outcome := runSearch(t, []string{"a.txt"}, dir, "needle", Options{MaxMatches: 10})
	if len(matches) != 10 {
		t.Fatalf("expected exactly 10 matches (the cap), got %d", len(matches))
	}
	if !outcome.Capped {
		t.Fatal("expected Capped=true")
	}
	if outcome.Matches != 10 {
		t.Fatalf("outcome.Matches: got %d, want 10", outcome.Matches)
	}
}

// TestSearch_EmptyQueryNoOp pins that an empty query returns a
// already-closed channel and a zero Outcome rather than matching every
// line (which an empty substring would do under bytes.Index semantics).
func TestSearch_EmptyQueryNoOp(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, "a.txt", "anything\n")

	matches, outcome := runSearch(t, []string{"a.txt"}, dir, "", Options{})
	if len(matches) != 0 {
		t.Fatalf("expected no matches for empty query, got %+v", matches)
	}
	if outcome != (Outcome{}) {
		t.Fatalf("expected zero Outcome, got %+v", outcome)
	}
}

// TestSearch_TrimsLongLines pins that a match deep inside a very long
// line gets a bounded, ellipsis-marked Text rather than the whole line.
func TestSearch_TrimsLongLines(t *testing.T) {
	dir := t.TempDir()
	line := strings.Repeat("x", 5000) + "needle" + strings.Repeat("y", 5000)
	mustWrite(t, dir, "a.txt", line+"\n")

	matches, _ := runSearch(t, []string{"a.txt"}, dir, "needle", Options{})
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	text := matches[0].Text
	if len([]rune(text)) > maxLineTextLen+2 { // +2 for the two ellipses
		t.Fatalf("Text too long: %d runes", len([]rune(text)))
	}
	if !strings.Contains(text, "needle") {
		t.Fatalf("trimmed text lost the match: %q", text)
	}
	if !strings.HasPrefix(text, "…") || !strings.HasSuffix(text, "…") {
		t.Fatalf("expected ellipses on both ends, got %q", text)
	}
	m := matches[0]
	trunes := []rune(text)
	if m.MatchStart < 0 || m.MatchStart+m.MatchLen > len(trunes) {
		t.Fatalf("MatchStart/MatchLen out of range: %d/%d over %d runes", m.MatchStart, m.MatchLen, len(trunes))
	}
	gotSpan := string(trunes[m.MatchStart : m.MatchStart+m.MatchLen])
	if gotSpan != "needle" {
		t.Fatalf("span from MatchStart/MatchLen: got %q, want %q (full text %q)", gotSpan, "needle", text)
	}
}
