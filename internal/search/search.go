// =============================================================================
// File: internal/search/search.go
// Author: Chase Reynolds
// Created: 2026-09-03
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// No upstream equivalent. Phase 8b: content search across the files
// under the root, for the Esc F modal in internal/app/search.go.
// =============================================================================

// Package search grep's a list of files for a query and streams the hits
// back on a channel. It is pure — no tcell import — the same split
// internal/diff and internal/markdown use: this package knows nothing
// about the terminal, and internal/app/search.go is the only thing that
// turns its output into pixels.
//
// The file list search runs over is not this package's business either —
// it comes from the caller, which in Vincent's case is
// internal/finder.Finder.Paths(), the same multi-root index the Esc-p
// finder uses. That keeps content search honouring the same gitignore /
// repo-boundary rules as the filename finder for free, with one index
// instead of two.
package search

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// maxFileSize is the largest file Search will open and read. A content
// search across a work root can otherwise land on a vendored data dump or
// a build artifact and spend its whole budget on one file nobody wants
// grepped.
const maxFileSize = 1 << 20 // 1 MiB

// binarySniffLen is how many leading bytes Search inspects for a NUL byte
// before deciding a file is binary and skipping it rather than scanning
// it line by line (which would otherwise happily "match" garbage inside
// an image or a compiled binary).
const binarySniffLen = 8 << 10 // 8 KiB

// DefaultMaxMatches is the match cap applied when Options.MaxMatches is
// zero or negative. 2000 is comfortably more than a reviewer will ever
// page through in the modal, and small enough that a one-character query
// over a huge multi-repo root still returns before the reviewer gets
// impatient.
const DefaultMaxMatches = 2000

// maxWorkers caps the worker pool regardless of core count or how many
// files there are. Content search is I/O bound well before eight
// concurrent readers saturate a disk (or, the case that actually matters
// at Chase's work root, a VPN-mounted network share), so more workers
// than that just adds scheduling overhead without moving results any
// faster.
const maxWorkers = 8

// maxLineTextLen bounds how many runes of a matching line end up in
// Match.Text. A minified JS bundle can have a single "line" tens of
// thousands of characters long; without a cap that one match would blow
// the result modal's row width past anything sane.
const maxLineTextLen = 200

// regexPrefix triggers regexp mode when it opens a query, per the spec:
// "a query starting with a literal `re:` is a regexp." Anything else is
// matched as a case-insensitive substring.
const regexPrefix = "re:"

// Match is one hit: which file (relative to the root Search was given),
// the 1-based line and rune column the match starts at (Col, into the
// ORIGINAL line — stable regardless of how Text below gets trimmed), and
// the line's display text.
//
// Text is trimmed to maxLineTextLen runes, centred on the match, when the
// line runs long — a minified bundle's "line" can be tens of thousands of
// runes, and Vincent's search modal renders one row per match. MatchStart
// and MatchLen are rune offsets INTO TEXT (not into the original line)
// delimiting the matched span, so the renderer can highlight it without
// re-deriving trimLine's centering math itself.
type Match struct {
	Path       string
	Line       int
	Col        int
	Text       string
	MatchStart int
	MatchLen   int
}

// Options configures a Search run. The zero value is valid: MaxMatches
// falls back to DefaultMaxMatches and Workers falls back to
// runtime.NumCPU() capped at maxWorkers.
type Options struct {
	MaxMatches int
	Workers    int
}

// Outcome is the summary Search hands to onDone once every worker has
// exited (or ctx was cancelled). FilesScanned counts only files that
// actually got read — a file skipped for size or binary content is not
// counted, because it was never really searched.
type Outcome struct {
	Matches      int
	FilesScanned int
	Capped       bool
}

// matcher is what one worker runs against a line's raw bytes. It returns
// every match as a [start,end) byte-offset pair within that line so the
// caller can turn the start into a rune column and slice out the display
// text.
type matcher func(line []byte) [][]int

// newMatcher builds the matcher for query: a compiled regexp when query
// has the "re:" prefix (the prefix itself is stripped before compiling
// and the pattern's own case sensitivity applies — this is grep -E
// semantics, not the substring path's folding), or a case-insensitive
// substring search otherwise. Returns an error only for an invalid
// regexp; an empty (non-regexp) query yields a matcher that never
// matches, since Search treats an empty query as "nothing to look for"
// before this is even called.
func newMatcher(query string) (matcher, error) {
	if rest, ok := strings.CutPrefix(query, regexPrefix); ok {
		re, err := regexp.Compile(rest)
		if err != nil {
			return nil, err
		}
		return func(line []byte) [][]int {
			return re.FindAllIndex(line, -1)
		}, nil
	}
	// Byte-wise ASCII-ish case folding via bytes.ToLower on both sides.
	// This is deliberately not full Unicode case folding (that would need
	// per-rune comparison and a slower scan) — a query with non-ASCII
	// letters still works, it just won't fold accented case variants onto
	// each other. Good enough for source code and log lines, which is
	// what a code reviewer is grepping.
	needle := bytes.ToLower([]byte(query))
	return func(line []byte) [][]int {
		if len(needle) == 0 {
			return nil
		}
		lower := bytes.ToLower(line)
		var out [][]int
		start := 0
		for {
			i := bytes.Index(lower[start:], needle)
			if i < 0 {
				break
			}
			from := start + i
			to := from + len(needle)
			out = append(out, []int{from, to})
			start = to
		}
		return out
	}, nil
}

// Search starts a bounded worker pool over files (paths relative to
// root) and streams every match for query on the returned channel. The
// channel closes once every worker has finished — whether that's because
// every file was scanned, ctx was cancelled, or the match cap was hit
// (which cancels the search's own internal context so every worker stops
// promptly rather than finishing the file it happens to be on).
//
// onDone, if non-nil, is called exactly once, after the channel closes,
// with the run's Outcome. It may run on a different goroutine than the
// caller's read loop, so onDone must not touch anything that isn't safe
// for that — internal/app/search.go's use is to PostEvent a tcell event,
// the same contract every other background worker in Vincent follows
// (see gitpoll.go, finder.go's Rebuild).
//
// An empty query, an empty file list, or an invalid "re:" pattern all
// return a channel that is already closed and call onDone with a zero
// Outcome — there is nothing to search, so there is nothing to report.
func Search(ctx context.Context, files []string, root, query string, opts Options, onDone func(Outcome)) <-chan Match {
	out := make(chan Match)
	if query == "" || len(files) == 0 {
		close(out)
		if onDone != nil {
			onDone(Outcome{})
		}
		return out
	}

	match, err := newMatcher(query)
	if err != nil {
		close(out)
		if onDone != nil {
			onDone(Outcome{})
		}
		return out
	}

	maxMatches := opts.MaxMatches
	if maxMatches <= 0 {
		maxMatches = DefaultMaxMatches
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers > maxWorkers {
		workers = maxWorkers
	}
	if workers > len(files) {
		workers = len(files)
	}
	if workers < 1 {
		workers = 1
	}

	// searchCtx is cancelled by the caller's ctx OR by hitting the match
	// cap, so every worker stops on the same signal either way instead of
	// the cap needing its own separate plumbing through searchFile.
	searchCtx, cancel := context.WithCancel(ctx)

	var matchCount int64
	var filesScanned int64
	var capped atomic.Bool

	jobs := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if searchCtx.Err() != nil {
					return
				}
				if searchFile(searchCtx, root, path, match, out, &matchCount, int64(maxMatches), &capped, cancel) {
					atomic.AddInt64(&filesScanned, 1)
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, f := range files {
			select {
			case <-searchCtx.Done():
				return
			case jobs <- f:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(out)
		// Every worker has exited, including the one that may have
		// called cancel() itself on hitting the cap — cancel again here
		// unconditionally so ctx's own resources are released even on
		// the plain "ran to completion" path (context.WithCancel's
		// contract: the cancel func must be called to avoid a leak).
		cancel()
		if onDone != nil {
			// matchCount can overshoot maxMatches by exactly one: the
			// worker that hits the cap increments before checking, so
			// the counter itself briefly reads one past what was
			// actually emitted on out. Clamp rather than report a count
			// that doesn't match len(matches) from the caller's own
			// channel read.
			total := atomic.LoadInt64(&matchCount)
			if total > int64(maxMatches) {
				total = int64(maxMatches)
			}
			onDone(Outcome{
				Matches:      int(total),
				FilesScanned: int(atomic.LoadInt64(&filesScanned)),
				Capped:       capped.Load(),
			})
		}
	}()

	return out
}

// searchFile scans one file for match, emitting each hit on out. Returns
// whether the file was actually scanned — false for a stat failure, a
// directory, an oversized file, or a binary one — so the caller's
// FilesScanned count reflects files really searched rather than files
// merely considered.
//
// Hitting the cap sets capped and calls cancel so every other worker
// notices on their next job or next line and stops too, rather than each
// worker independently running its current file to completion.
func searchFile(ctx context.Context, root, relPath string, match matcher, out chan<- Match, matchCount *int64, maxMatches int64, capped *atomic.Bool, cancel context.CancelFunc) bool {
	full := filepath.Join(root, relPath)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() || info.Size() > maxFileSize {
		return false
	}
	f, err := os.Open(full)
	if err != nil {
		return false
	}
	defer f.Close()

	head := make([]byte, binarySniffLen)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}

	scanner := bufio.NewScanner(f)
	// The default 64KB scan buffer chokes on a genuinely long line (a
	// minified bundle, a data file) with bufio.ErrTooLong. Raise the cap
	// to 1MB — matching maxFileSize, so nothing this function already
	// agreed to open can trip it — rather than silently truncating or
	// erroring out of an otherwise-fine file.
	scanner.Buffer(make([]byte, 64*1024), maxFileSize)
	line := 0
	for scanner.Scan() {
		line++
		if ctx.Err() != nil {
			return true
		}
		text := scanner.Bytes()
		for _, r := range match(text) {
			n := atomic.AddInt64(matchCount, 1)
			if n > maxMatches {
				capped.Store(true)
				cancel()
				return true
			}
			displayText, matchStart, matchLen := trimLine(text, r[0], r[1])
			m := Match{
				Path:       relPath,
				Line:       line,
				Col:        utf8.RuneCount(text[:r[0]]) + 1,
				Text:       displayText,
				MatchStart: matchStart,
				MatchLen:   matchLen,
			}
			select {
			case out <- m:
			case <-ctx.Done():
				return true
			}
		}
	}
	return true
}

// trimLine renders line as display text: trimmed to maxLineTextLen runes
// centred on the [byteFrom, byteTo) match span when the line is longer
// than that, with an ellipsis marking whichever end got cut, and a
// trailing \r (CRLF files) stripped either way. Alongside the text it
// returns the match's rune offset and length WITHIN that returned text,
// so the caller doesn't have to redo the centering/ellipsis arithmetic to
// know where to highlight.
func trimLine(line []byte, byteFrom, byteTo int) (text string, matchStart, matchLen int) {
	s := strings.TrimRight(string(line), "\r")
	runes := []rune(s)
	fromRune := utf8.RuneCount(line[:byteFrom])
	toRune := utf8.RuneCount(line[:byteTo])
	matchLen = toRune - fromRune

	if len(runes) <= maxLineTextLen {
		return s, fromRune, matchLen
	}

	half := maxLineTextLen / 2
	start := fromRune - half
	if start < 0 {
		start = 0
	}
	end := start + maxLineTextLen
	if end > len(runes) {
		end = len(runes)
		start = end - maxLineTextLen
		if start < 0 {
			start = 0
		}
	}
	out := string(runes[start:end])
	matchStart = fromRune - start
	if start > 0 {
		out = "…" + out
		matchStart++ // the ellipsis consumed one rune ahead of the match
	}
	if end < len(runes) {
		out = out + "…"
	}
	// A match that starts before `start` (possible only when the match
	// itself is longer than the whole display window, e.g. a needle
	// bigger than maxLineTextLen) can't be pointed at meaningfully within
	// the trimmed text — clamp rather than hand back a negative offset
	// the renderer would slice out of bounds with.
	if matchStart < 0 {
		matchStart = 0
	}
	if matchStart+matchLen > len([]rune(out)) {
		matchLen = len([]rune(out)) - matchStart
		if matchLen < 0 {
			matchLen = 0
		}
	}
	return out, matchStart, matchLen
}
