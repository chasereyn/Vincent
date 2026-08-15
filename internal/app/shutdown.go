// =============================================================================
// File: internal/app/shutdown.go
// Author: Chase Reynolds
// Created: 2026-08-15
// Copyright: 2026 Chase Reynolds. All rights reserved.
// =============================================================================

// shutdown.go makes Vincent exit when it is told to, which is not free for a
// terminal UI.
//
// tcell puts the console in raw mode, and it is right to: a TUI wants Ctrl+C
// delivered as a keypress, not as "terminate". The cost is that Vincent opts
// out of the console's control events, so the usual ways a process dies with
// its terminal stop applying. Meanwhile the main loop blocks in PollEvent,
// which never returns once the console behind it is gone.
//
// The result, observed live and reproduced deliberately: closing the
// terminal leaves vincent.exe running forever. On Windows that also locks
// the binary, so the next `make install` fails with "Device or resource
// busy" — the orphan blocks its own replacement.
//
// The fix is to handle the signals explicitly, and to guarantee an exit even
// when the graceful path cannot complete. Both halves matter. Merely calling
// signal.Notify would make things WORSE if the loop is wedged: Go's default
// action for SIGTERM is to die immediately, and taking that over without
// guaranteeing an exit converts a hard kill into a hang.

package app

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

// quitGracePeriod is how long the signal watcher waits for the event loop to
// wind down on its own before restoring the terminal and exiting the hard
// way. Long enough for a healthy loop to finish a draw, short enough that a
// wedged one still goes away while the user is still watching.
const quitGracePeriod = 750 * time.Millisecond

// exitAfterSignal is the exit status for a signal-terminated run: the
// conventional 128 + SIGINT.
const exitAfterSignal = 130

// quitEvent is posted by the signal watcher to wake the event loop. Going
// through the event queue rather than setting a.quit directly keeps the
// no-UI-state-from-a-goroutine rule intact — the flag is still only written
// on the main thread, in handleEvent.
type quitEvent struct {
	when time.Time
}

// When satisfies the tcell.Event interface.
func (e *quitEvent) When() time.Time { return e.when }

// startSignalWatch arranges for Vincent to exit on Ctrl+C, SIGTERM, and the
// console-close events Windows maps onto them.
//
// The watcher asks nicely first — a posted quitEvent lets the main loop
// break, unwind, and restore the terminal through the normal path — and then
// stops asking. If the loop has not exited within quitGracePeriod it is not
// going to, so the watcher restores the terminal itself and exits the
// process. That second half is the whole point: without it this function
// would replace a guaranteed kill with a possible hang.
func (a *App) startSignalWatch() {
	ch := make(chan os.Signal, 1)
	// SIGTERM is not delivered on Windows, but naming it is free and this
	// binary also ships for Linux and macOS, where it is the signal a
	// process manager actually sends.
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	a.signalStop = ch

	go func() {
		if _, ok := <-ch; !ok {
			return // stopSignalWatch closed the channel on a normal exit.
		}
		_ = a.screen.PostEvent(&quitEvent{when: time.Now()})
		time.Sleep(quitGracePeriod)
		// Still here: the loop is wedged, most likely blocked in PollEvent
		// on a console that no longer exists. Put the terminal back the way
		// we found it and leave — an orphan that survives its terminal is
		// strictly worse than an abrupt exit.
		a.Close()
		os.Exit(exitAfterSignal)
	}()
}

// stopSignalWatch unregisters the handler so a normal exit does not leave
// the watcher goroutine holding a live signal registration. Safe to call
// more than once.
func (a *App) stopSignalWatch() {
	if a.signalStop == nil {
		return
	}
	signal.Stop(a.signalStop)
	close(a.signalStop)
	a.signalStop = nil
}
