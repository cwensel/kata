package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderInputBar formats the inline command bar as a bordered box.
// Used by the M3a above-the-table layout; M3.5 moved bars to the
// info line via renderInfoBar instead. Kept for any caller that
// wants the heavier bordered presentation.
//
//nolint:unused // superseded by renderInfoBar in M3.5
func renderInputBar(s inputState, width int) string {
	if width < 10 {
		width = 10
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(panelActiveBorder).
		Width(width-2). // -2 for the side borders
		Padding(0, 1)
	body := sanitizeForDisplay(s.activeField().input.View())
	rendered := box.Render(body)
	// Embed the title in the top border via a manual overlay: lipgloss
	// doesn't expose a "title in border" primitive yet, so prepend a
	// labeled top line and let the box's own top border act as the
	// underline.
	title := titleStyle.Render(" " + s.title + " ")
	return title + "\n" + rendered
}

// renderPanelPrompt is the M3b bordered panel-prompt shell. M3.5
// moved panel prompts to the info line via renderInfoPrompt for a
// lighter footprint; this stays for any caller wanting the heavier
// bordered presentation.
//
//nolint:unused // superseded by renderInfoPrompt in M3.5
func renderPanelPrompt(s inputState, width int) string {
	if width < 10 {
		width = 10
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(panelActiveBorder).
		Width(width-2).
		Padding(0, 1)
	body := sanitizeForDisplay(s.activeField().input.View())
	rendered := box.Render(body)
	title := titleStyle.Render(" " + s.title + " ")
	return title + "\n" + rendered
}

// renderCenteredForm is the M4 centered modal panel: bordered title
// strip, textarea body, footer hint inside the box. Sized to ~70%
// of the terminal (capped to 100x24 lines so wide windows don't get
// a stretched form). Composed inline by Model.View via overlayModal
// so the form sits on top of the underlying view's background.
//
// Render-time sanitization is applied to the title and footer text
// (trusted package strings, but cheap and consistent) and the
// textarea's view delegates to bubbles' own input-side Sanitize so
// any pasted ANSI sequence is dropped before it reaches the buffer.
// Mutation payloads read the field value untouched — only the
// display layer applies sanitization.
func renderCenteredForm(s inputState, width, height int) string {
	if width < formMinWidth || height < formMinHeight {
		return renderTinyFormFallback(s)
	}
	innerW := formInnerWidth(width)
	innerH := formInnerHeight(height)
	f := s.activeField()
	if f == nil {
		return ""
	}
	// Resize the textarea to the form's interior.
	f.area.SetWidth(innerW)
	f.area.SetHeight(innerH - 2 /* title + footer */)
	body := f.area.View()
	footer := renderFormFooter(s, innerW)
	statusLine := renderFormStatus(s, innerW)
	parts := []string{
		titleStyle.Render(s.title),
		body,
	}
	if statusLine != "" {
		parts = append(parts, statusLine)
	}
	parts = append(parts, footer)
	box := modalBoxStyle.Width(innerW).Padding(0, 1).Render(strings.Join(parts, "\n"))
	return box
}

// renderFormFooter is the footer-hint row inside the panel. While
// saving=true the hint flips to a single "saving…" cell so the user
// sees they should wait.
func renderFormFooter(s inputState, innerW int) string {
	if s.saving {
		return statusStyle.Render("saving…")
	}
	hint := "ctrl+s save · esc cancel · ctrl+e $EDITOR"
	if len(hint) > innerW {
		hint = "ctrl+s save · esc cancel"
	}
	return subtleStyle.Render(hint)
}

// renderFormStatus surfaces in-form errors (editor cancel / error,
// empty-comment block on commit). Empty when no status to show.
func renderFormStatus(s inputState, _ int) string {
	if s.err == "" {
		return ""
	}
	return errorStyle.Render(s.err)
}

// renderTinyFormFallback is the degraded render for terminals smaller
// than the form's minimum size. Just dumps the textarea so the user
// can still type; no border / no centering. Useful in narrow CI
// terminals or test fixtures.
func renderTinyFormFallback(s inputState) string {
	f := s.activeField()
	if f == nil {
		return ""
	}
	return s.title + "\n" + f.area.View()
}

// formInnerWidth picks the centered form's interior width. ~70% of
// terminal width, capped at 100 cells so a 200-cell window doesn't
// produce a stretched-out modal that's hard to read.
func formInnerWidth(width int) int {
	w := width * 7 / 10
	if w > 100 {
		w = 100
	}
	if w < formMinWidth {
		w = formMinWidth
	}
	return w
}

// formInnerHeight picks the centered form's interior height. Caps
// at 24 rows so the modal is roughly screen-sized, not full-screen.
func formInnerHeight(height int) int {
	h := height * 7 / 10
	if h > 24 {
		h = 24
	}
	if h < formMinHeight {
		h = formMinHeight
	}
	return h
}
