// =============================================================================
// File: internal/review/herdr_test.go
// Author: Chase Reynolds
// Created: 2026-09-02
// Copyright: 2026 Chase Reynolds. All rights reserved.
//
// Nothing here runs the herdr binary. Every test injects a Runner and
// asserts on the argv Vincent builds, which is the whole point: a test that
// actually called `herdr pane send-text` would stage text in whichever
// agent pane happened to be open on the machine running the suite.
// =============================================================================

package review

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRun installs a Runner for the duration of one test, recording every
// argv it is handed and replying with canned output.
func fakeRun(t *testing.T, reply map[string]string, fail map[string]error) *[][]string {
	t.Helper()
	calls := &[][]string{}
	prev := Run
	prevLog := Logf
	Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		argv := append([]string{name}, args...)
		*calls = append(*calls, argv)
		key := strings.Join(args, " ")
		for prefix, err := range fail {
			if strings.HasPrefix(key, prefix) {
				return nil, err
			}
		}
		for prefix, out := range reply {
			if strings.HasPrefix(key, prefix) {
				return []byte(out), nil
			}
		}
		return nil, nil
	}
	// Silence the verbatim-failure log so a deliberate error path doesn't
	// spray herdr envelopes across the test output.
	Logf = func(string, ...any) {}
	t.Cleanup(func() { Run = prev; Logf = prevLog })
	return calls
}

// liveListJSON is the real envelope from `herdr agent list` on herdr 0.8.2,
// trimmed to the fields Vincent reads plus one null-agent pane and one
// pane in another workspace. Kept verbatim in shape so a herdr field
// rename shows up here as a failing test rather than as an empty picker.
const liveListJSON = `{"id":"cli:agent:list","result":{"agents":[
{"agent":"claude","agent_status":"working","cwd":"/repo","pane_id":"wE:p2","tab_id":"wE:t1","workspace_id":"wE","terminal_title":"one"},
{"agent":"claude","agent_status":"idle","cwd":"/repo","pane_id":"wE:p3","tab_id":"wE:t2","workspace_id":"wE","name":"reviewer bot"},
{"agent":null,"agent_status":"","cwd":"/repo","pane_id":"wE:p4","tab_id":"wE:t2","workspace_id":"wE"},
{"agent":"codex","agent_status":"done","cwd":"/other","pane_id":"wD:p1","tab_id":"wD:t1","workspace_id":"wD"},
{"agent":"claude","agent_status":"working","cwd":"/repo","pane_id":"wE:p1","tab_id":"wE:t1","workspace_id":"wE"}
],"type":"agent_list"}}`

// TestListTargets_FiltersToWorkspaceAndExcludesSelf pins the candidate
// rule: an agent must be present, in this workspace, and not this pane.
func TestListTargets_FiltersToWorkspaceAndExcludesSelf(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_WORKSPACE_ID", "wE")
	t.Setenv("HERDR_PANE_ID", "wE:p1")
	calls := fakeRun(t, map[string]string{"agent list": liveListJSON}, nil)

	targets, err := ListTargets(context.Background())
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2: %+v", len(targets), targets)
	}
	if targets[0].PaneID != "wE:p2" || targets[1].PaneID != "wE:p3" {
		t.Errorf("wrong panes: %+v", targets)
	}
	if got := (*calls)[0]; strings.Join(got, " ") != "herdr agent list" {
		t.Errorf("argv = %v, want [herdr agent list]", got)
	}
}

// TestListTargets_RowNamePrecedence pins name → display_agent → agent →
// pane_id. The reviewer sees these strings, so getting the order wrong
// means a picker full of "claude, claude, claude".
func TestListTargets_RowNamePrecedence(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "wE")
	t.Setenv("HERDR_PANE_ID", "wE:p9")
	json := `{"result":{"agents":[
{"agent":"claude","pane_id":"wE:p1","workspace_id":"wE","name":"named","display_agent":"shown"},
{"agent":"claude","pane_id":"wE:p2","workspace_id":"wE","display_agent":"shown"},
{"agent":"claude","pane_id":"wE:p3","workspace_id":"wE"},
{"agent":" ","pane_id":"wE:p4","workspace_id":"wE"}
]}}`
	fakeRun(t, map[string]string{"agent list": json}, nil)

	targets, err := ListTargets(context.Background())
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	want := []string{"named", "shown", "claude", " "}
	if len(targets) != len(want) {
		t.Fatalf("got %d targets, want %d", len(targets), len(want))
	}
	for i, w := range want {
		if targets[i].Name != w {
			t.Errorf("targets[%d].Name = %q, want %q", i, targets[i].Name, w)
		}
	}
}

// TestListTargets_NoCandidatesIsErrNoAgent pins that an empty candidate set
// is reported as ErrNoAgent, not as an empty success — the app needs to
// flash a reason and fall back to the clipboard.
func TestListTargets_NoCandidatesIsErrNoAgent(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "wZ")
	t.Setenv("HERDR_PANE_ID", "wZ:p1")
	fakeRun(t, map[string]string{"agent list": liveListJSON}, nil)

	if _, err := ListTargets(context.Background()); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("err = %v, want ErrNoAgent", err)
	}
}

// TestListTargets_FailureIsErrNoAnswer pins that herdr's own error never
// escapes: a failed call comes back as the one plain sentence the status
// bar can show.
func TestListTargets_FailureIsErrNoAnswer(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "wE")
	fakeRun(t, nil, map[string]error{
		"agent list": errors.New(`exit status 1: {"error":{"code":"pane_not_found"}}`),
	})

	_, err := ListTargets(context.Background())
	if !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err = %v, want ErrNoAnswer", err)
	}
	if strings.Contains(err.Error(), "pane_not_found") {
		t.Errorf("herdr's own error text leaked into %q", err)
	}
}

// TestListTargets_UnparseableJSONIsErrNoAnswer pins the degrade path for a
// herdr version whose output we no longer understand.
func TestListTargets_UnparseableJSONIsErrNoAnswer(t *testing.T) {
	t.Setenv("HERDR_WORKSPACE_ID", "wE")
	fakeRun(t, map[string]string{"agent list": "not json at all"}, nil)

	if _, err := ListTargets(context.Background()); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err = %v, want ErrNoAnswer", err)
	}
}

// TestSend_BuildsSendTextThenFocus pins the exact commands: `pane
// send-text` with a bracketed-paste-wrapped payload, then `agent focus`.
// Most importantly it pins that `agent prompt` is NOT used — that command
// appends Enter and would submit the review unattended.
func TestSend_BuildsSendTextThenFocus(t *testing.T) {
	calls := fakeRun(t, nil, nil)

	if err := Send(context.Background(), "wE:p2", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("made %d calls, want 2: %v", len(*calls), *calls)
	}
	send := (*calls)[0]
	if strings.Join(send[:4], " ") != "herdr pane send-text wE:p2" {
		t.Errorf("send argv = %v", send)
	}
	if send[4] != "\x1b[200~hello\x1b[201~" {
		t.Errorf("payload = %q, want a bracketed-paste frame", send[4])
	}
	if got := strings.Join((*calls)[1], " "); got != "herdr agent focus wE:p2" {
		t.Errorf("focus argv = %q", got)
	}
	for _, c := range *calls {
		if strings.Contains(strings.Join(c, " "), "agent prompt") {
			t.Fatal("Send must never use `herdr agent prompt` — it presses Enter")
		}
	}
}

// TestSend_FocusFailureIsNotAnError pins the consume-on-success boundary:
// the text is already delivered when focus runs, so a focus failure must
// not report a failed send (which would make the app keep a batch it has
// in fact handed over — or worse, send it twice).
func TestSend_FocusFailureIsNotAnError(t *testing.T) {
	fakeRun(t, nil, map[string]error{"agent focus": errors.New("no such window")})

	if err := Send(context.Background(), "wE:p2", "hello"); err != nil {
		t.Fatalf("focus failure should be swallowed, got %v", err)
	}
}

// TestSend_SendTextFailureIsErrNoAnswer pins that a failed send-text is a
// real error, so the app keeps the batch instead of clearing it.
func TestSend_SendTextFailureIsErrNoAnswer(t *testing.T) {
	fakeRun(t, nil, map[string]error{"pane send-text": errors.New("closed pane")})

	if err := Send(context.Background(), "wE:p2", "hello"); !errors.Is(err, ErrNoAnswer) {
		t.Fatalf("err = %v, want ErrNoAnswer", err)
	}
}

// TestAvailable_ReadsHerdrEnv pins the gate that keeps Vincent from
// shelling out to herdr when it is not running inside a herdr pane.
func TestAvailable_ReadsHerdrEnv(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	if !Available() {
		t.Error("HERDR_ENV=1 should be available")
	}
	t.Setenv("HERDR_ENV", "")
	if Available() {
		t.Error("empty HERDR_ENV should not be available")
	}
}
