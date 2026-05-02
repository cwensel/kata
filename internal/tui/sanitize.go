package tui

import "github.com/wesm/kata/internal/textsafe"

// sanitizeForDisplay is the TUI-side alias for textsafe.Block — strips
// ANSI escape sequences, Unicode control characters, and Cf bidi
// overrides from agent-authored text before it lands on the terminal.
// Kept as a thin alias so existing call sites stay readable; the real
// logic lives in internal/textsafe so the CLI can share it.
func sanitizeForDisplay(s string) string { return textsafe.Block(s) }
