// =============================================================================
// File: internal/review/herdr.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// The call sequence and the candidate filter follow herdr-reviewr's
// src/herdr.rs (send_target / candidates / send_text / focus). Its Rust is
// not reproduced here; what is reproduced is the protocol — which commands,
// in which order, with which filter — because that part is load-bearing and
// was verified against a live herdr 0.8.2.
// =============================================================================

// herdr.go is the one place Vincent talks to another program at run time
// besides git. It finds the agent pane that wrote the code under review and
// stages the review batch in that pane's input, without pressing Enter.
//
// Three calls, in order:
//
//	herdr agent list                     — who is out there
//	herdr pane send-text <pane> <text>   — stage the batch, no Enter
//	herdr agent focus <pane>             — bring it into view
//
// `herdr agent prompt` is deliberately NOT used. It appends an encoded
// Enter after a short delay, which would submit the review unattended;
// Vincent's entire premise is a human in the loop, and the reviewer wants
// to add a sentence of context before hitting Enter themselves.
//
// Two rules the rest of the app depends on:
//
//   - Never surface herdr's own error text. Its JSON envelope names pane
//     ids the reviewer has never seen and does not fit a status bar. Every
//     failure here is logged verbatim and returned as one plain sentence.
//   - Bound every call. A wedged herdr daemon must degrade to the clipboard
//     fallback, not hang the UI thread. Two seconds, from herdr-reviewr's
//     ANSWER_BOUND.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"time"
)

// answerBound is how long any single herdr call gets before we give up on
// it. herdr answers in milliseconds when it is healthy, so two seconds is
// generous; the point is only that the number is finite.
const answerBound = 2 * time.Second

// herdrBin is the executable name. Resolved through PATH rather than
// through HERDR_BIN_PATH so a user who has herdr on PATH but no herdr
// environment still works, and so tests never depend on either.
const herdrBin = "herdr"

// Runner runs one external command and returns its stdout. Injected so the
// send path can be tested by asserting on the argv Vincent builds, without
// a herdr daemon and — critically — without ever staging text in a real
// agent's pane during a test run.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Run is the Runner every function here goes through. Overridable in tests;
// production never reassigns it.
var Run Runner = execRunner

// Logf is where the verbatim herdr failure goes. A package-level var so the
// app can point it at a real log file later without this file learning
// anything about where logs live.
var Logf = log.Printf

// execRunner is the production Runner: run the binary, return stdout, and
// fold stderr into the error so the log line carries herdr's own JSON
// envelope rather than just "exit status 1".
func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return out, errors.New(err.Error() + ": " + string(ee.Stderr))
		}
		return out, err
	}
	return out, nil
}

// ErrNoAgent is the sentinel for "herdr answered, and there is nobody here
// to send to". The app treats it exactly like a herdr failure — fall back
// to the clipboard — but the message it flashes differs, and the caller
// should not have to string-match to tell them apart.
var ErrNoAgent = errors.New("no agent in this workspace — copied to clipboard instead")

// ErrNoAnswer is the sentinel for every other herdr failure: not
// installed, not running, wedged, or a JSON shape we don't recognise.
var ErrNoAnswer = errors.New("herdr did not answer")

// Available reports whether we are running inside a herdr pane at all.
//
// HERDR_ENV is the flag herdr sets in every pane it owns. Outside one there
// is no workspace to scope candidates to and no reason to pay for a
// shell-out that will find nothing, so the app goes straight to the
// clipboard.
func Available() bool {
	return os.Getenv("HERDR_ENV") == "1"
}

// Target is one agent pane the batch could go to.
//
// Name is the label a picker row shows. Status is herdr's own agent_status
// ("working", "idle", "done", …), shown beside the name because "which of
// these two Claudes is the one I was watching" is usually answered by which
// one is still working.
type Target struct {
	PaneID string
	Name   string
	Kind   string
	Status string
	TabID  string
}

// agentListResponse mirrors just enough of `herdr agent list` to pick a
// pane. Field names verified against a live herdr 0.8.2 rather than guessed
// from documentation: the envelope is {"id":…,"result":{"agents":[…]}} and
// each agent carries agent / agent_status / pane_id / tab_id /
// workspace_id / cwd, with name and display_agent optional.
//
// Agent is a *string on purpose. herdr lists panes that are not running an
// agent with a null there, and those are exactly the panes we must not
// paste a code review into.
type agentListResponse struct {
	Result struct {
		Agents []struct {
			Agent        *string `json:"agent"`
			AgentStatus  string  `json:"agent_status"`
			PaneID       string  `json:"pane_id"`
			TabID        string  `json:"tab_id"`
			WorkspaceID  string  `json:"workspace_id"`
			Cwd          string  `json:"cwd"`
			Name         string  `json:"name"`
			DisplayAgent string  `json:"display_agent"`
		} `json:"agents"`
	} `json:"result"`
}

// ListTargets returns the agent panes in this workspace that could receive
// a review, excluding the pane Vincent itself is running in.
//
// Workspace-scoped, not tab-scoped: herdr-reviewr established that a
// reviewer keeps the reviewer and the agent in one workspace but not
// necessarily in one tab, and widening to the whole herdr session would
// offer agents working on unrelated repos.
//
// An empty result is not an error — it is ErrNoAgent, which the app renders
// as a reason and then falls back to the clipboard.
func ListTargets(ctx context.Context) ([]Target, error) {
	ctx, cancel := context.WithTimeout(ctx, answerBound)
	defer cancel()

	out, err := Run(ctx, herdrBin, "agent", "list")
	if err != nil {
		Logf("vincent: herdr agent list failed: %v", err)
		return nil, ErrNoAnswer
	}
	var resp agentListResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		Logf("vincent: herdr agent list returned unparseable JSON: %v: %s", err, string(out))
		return nil, ErrNoAnswer
	}

	workspace := os.Getenv("HERDR_WORKSPACE_ID")
	self := os.Getenv("HERDR_PANE_ID")

	targets := []Target{}
	for _, a := range resp.Result.Agents {
		// A null agent means the pane is a plain shell. Pasting a review
		// into one would type the whole batch at a prompt.
		if a.Agent == nil || *a.Agent == "" {
			continue
		}
		if a.WorkspaceID != workspace {
			continue
		}
		if a.PaneID == "" || a.PaneID == self {
			continue
		}
		targets = append(targets, Target{
			PaneID: a.PaneID,
			Name:   rowName(a.Name, a.DisplayAgent, *a.Agent, a.PaneID),
			Kind:   *a.Agent,
			Status: a.AgentStatus,
			TabID:  a.TabID,
		})
	}
	if len(targets) == 0 {
		return nil, ErrNoAgent
	}
	return targets, nil
}

// rowName picks the most human label available for a picker row, in
// herdr-reviewr's precedence: the pane's own name, then herdr's display
// name for the agent, then the bare agent kind, then the pane id. The pane
// id is a last resort because "wD:p3" is meaningless to the reviewer, but
// it is better than a blank row.
func rowName(name, display, kind, paneID string) string {
	for _, candidate := range []string{name, display, kind} {
		if candidate != "" {
			return candidate
		}
	}
	return paneID
}

// Send stages text in paneID's input as a bracketed paste and brings that
// pane into view. It never presses Enter.
//
// The focus call is best-effort: the text is already delivered by then, and
// failing the whole send because herdr could not raise a window would clear
// nothing and tell the reviewer their review vanished. A focus failure is
// logged and swallowed.
func Send(ctx context.Context, paneID, text string) error {
	sendCtx, cancel := context.WithTimeout(ctx, answerBound)
	defer cancel()

	if _, err := Run(sendCtx, herdrBin, "pane", "send-text", paneID, Wrap(text)); err != nil {
		Logf("vincent: herdr pane send-text %s failed: %v", paneID, err)
		return ErrNoAnswer
	}

	focusCtx, focusCancel := context.WithTimeout(ctx, answerBound)
	defer focusCancel()
	if _, err := Run(focusCtx, herdrBin, "agent", "focus", paneID); err != nil {
		Logf("vincent: herdr agent focus %s failed (ignored): %v", paneID, err)
	}
	return nil
}
