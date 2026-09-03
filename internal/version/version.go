// =============================================================================
// File: internal/version/version.go
// Author: Spicer Matthews <spicer@cloudmanic.com>
// Created: 2026-04-29
// Copyright: 2026 Cloudmanic, LLC. All rights reserved.
// =============================================================================

// Package version exposes Vincent's release version. Keep this file
// tiny — it's the single source of truth that release CI will bump,
// and a one-line diff is trivial to review.
package version

// Version is the Vincent release version, displayed in the menu footer.
// Bump this constant on each release (or let release automation do it).
//
// Bump it when shipping a phase, too. There is no auto-update, so
// `vincent --version` is the only way to tell whether the binary on PATH is
// the one that was just built — and on Windows an install can silently fail
// to replace a running executable, which makes that a real question.
//
//	0.1.0  phase 0: the fork, stripped and blackened
//	0.2.0  phases 1 and 2: inline diffs, the Changes panel
const Version = "0.6.3"
