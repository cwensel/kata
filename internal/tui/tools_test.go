//go:build tools

// Package tui keeps Plan 6 toolchain dependencies pinned so go mod tidy
// does not drop them before the first real consumer in Task 4.
package tui

import (
	_ "github.com/charmbracelet/x/exp/teatest"
)
