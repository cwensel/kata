package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// modalKind names which centered confirm/info modal is active.
// modalNone is the quiescent state. After M3.5b only the quit-confirm
// case exists; future plans (delete-confirm, etc.) extend the enum.
type modalKind int

const (
	modalNone modalKind = iota
	modalQuitConfirm
)

// renderQuitConfirmModal returns the centered "Are you sure?" panel.
// Mirrors msgvault's modalQuitConfirm: bordered box with the prompt
// text and `[Y] Yes  [N] No` action hint. Width is fixed (~40 cells)
// so the panel stays compact regardless of terminal width.
func renderQuitConfirmModal() string {
	body := strings.Join([]string{
		"Quit kata?",
		"",
		"Are you sure you want to quit?",
		"",
		"[Y] Yes    [N] No",
	}, "\n")
	return modalBoxStyle.Render(body)
}

// overlayModal centers a modal panel over the rendered background
// view. Mirrors msgvault's view.go::overlayModal: split the bg into
// lines, compute centering, splice the modal into the right offset
// preserving lipgloss Width math (so colored bg lines around the
// modal stay intact).
//
// width / height come from Model.width / Model.height. background is
// the already-rendered sub-view (list, detail, help, empty).
func overlayModal(background, modal string, width, height int) string {
	if modal == "" {
		return background
	}
	bgLines := strings.Split(background, "\n")
	modalLines := strings.Split(modal, "\n")
	modalH := len(modalLines)
	startLine := (height - modalH) / 2
	if startLine < 0 {
		startLine = 0
	}
	modalW := lipgloss.Width(modal)
	leftPad := (width - modalW) / 2
	if leftPad < 0 {
		leftPad = 0
	}
	for i, mLine := range modalLines {
		idx := startLine + i
		if idx >= len(bgLines) {
			break
		}
		bg := bgLines[idx]
		bgW := lipgloss.Width(bg)
		var b strings.Builder
		// Left portion of bg before modal.
		if leftPad > 0 {
			left := truncateBgToWidth(bg, leftPad)
			b.WriteString(left)
			if lipgloss.Width(left) < leftPad {
				b.WriteString(strings.Repeat(" ", leftPad-lipgloss.Width(left)))
			}
		}
		b.WriteString(mLine)
		// Right portion of bg after modal.
		rightStart := leftPad + modalW
		if rightStart < bgW {
			b.WriteString(skipBgToWidth(bg, rightStart))
		}
		bgLines[idx] = b.String()
	}
	return strings.Join(bgLines, "\n")
}

// truncateBgToWidth returns the prefix of s whose visible width is
// at most w, preserving ANSI escape sequences. Helper for overlayModal
// when slicing the background to make room for the modal.
func truncateBgToWidth(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	// runewidth.Truncate with empty tail returns the prefix; lipgloss
	// doesn't expose a width-truncate helper that handles its own
	// styles, so we fall back to a rune-aware loop.
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runewidthRune(r)
		if used+rw > w {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String()
}

// skipBgToWidth returns the suffix of s starting after the first
// visible w cells. ANSI sequences are preserved by the loop (they
// don't add to visible width).
func skipBgToWidth(s string, w int) string {
	used := 0
	for i, r := range s {
		if used >= w {
			return s[i:]
		}
		used += runewidthRune(r)
	}
	return ""
}

// runewidthRune returns the cell width of a single rune. Thin wrapper
// over runewidth.RuneWidth so the truncate/skip helpers stay readable.
func runewidthRune(r rune) int { return runewidth.RuneWidth(r) }
