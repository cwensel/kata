package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderSplit composes the M6 split-pane layout: a 68-cell list
// pane on the left + flex detail pane on the right, sharing a
// single top-line title bar and bottom-line info+footer rows. Each
// pane is bordered; the focused pane's border uses panelActiveBorder,
// the inactive pane uses panelInactiveBorder so the user always sees
// which pane owns key dispatch.
//
// Modal overlays (filter form, new-issue form, quit confirm,
// suggestion menu) render OVER the whole composition — the modal
// machinery in Model.View applies after this returns. The caller
// (Model.viewBody) only invokes renderSplit when m.layout ==
// layoutSplit; the M5 too-narrow short-circuit runs ahead of this
// path, so width/height are guaranteed >= split breakpoints.
func renderSplit(m Model) string {
	width, height := m.width, m.height
	title := renderTitleBar(width, m.scope, kataVersion)
	footer := renderSplitFooter(width, m)
	infoLine := renderSplitInfoLine(width, m)
	// Body = (height - title - infoLine - footer) rows. The two panes
	// share that vertical budget; they're rendered side-by-side then
	// joined column-wise with lipgloss.JoinHorizontal so each pane
	// keeps its own border.
	bodyHeight := height - 3 // title + info + footer
	if bodyHeight < 4 {
		bodyHeight = 4
	}
	listW := splitListPaneWidth
	detailW := width - listW
	if detailW < 20 {
		detailW = 20
	}
	listPane := renderSplitListPane(m, listW, bodyHeight)
	detailPane := renderSplitDetailPane(m, detailW, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, listPane, detailPane)
	return strings.Join([]string{title, body, infoLine, footer}, "\n")
}

// renderSplitListPane renders the bordered list pane: list-table
// body inside a lipgloss panel whose border color reflects the
// focus state. paneW is the OUTER width including the 2-cell border
// surround; paneH is the OUTER height. Inner content takes
// paneW-2 cells wide and paneH-2 rows tall.
func renderSplitListPane(m Model, paneW, paneH int) string {
	innerW := paneW - 2
	innerH := paneH - 2
	if innerW < 10 {
		innerW = 10
	}
	if innerH < 2 {
		innerH = 2
	}
	chrome := m.chrome()
	chrome.narrow = true
	body := m.list.ViewBody(innerW, innerH, chrome)
	return splitPaneStyle(m.focus == focusList, paneW, paneH).Render(body)
}

// renderSplitDetailPane renders the bordered detail pane. When no
// detail is open (initial split-mode boot) we render a placeholder
// hint inviting the user to pick an issue from the list pane;
// otherwise we render dm's body+activity area inside the border.
func renderSplitDetailPane(m Model, paneW, paneH int) string {
	innerW := paneW - 2
	innerH := paneH - 2
	if innerW < 10 {
		innerW = 10
	}
	if innerH < 2 {
		innerH = 2
	}
	body := splitDetailBody(m, innerW, innerH)
	return splitPaneStyle(m.focus == focusDetail, paneW, paneH).Render(body)
}

// splitDetailBody picks the rendered body for the detail pane. With
// an open issue, defer to dm.ViewSplit; otherwise show the empty
// hint so the pane never goes blank.
func splitDetailBody(m Model, innerW, innerH int) string {
	if m.detail.issue == nil {
		return splitDetailEmptyHint(innerW, innerH)
	}
	return m.detail.ViewSplit(innerW, innerH, m.chrome())
}

// splitDetailEmptyHint is the placeholder text shown in the detail
// pane when no issue is open (initial split-mode boot, or after the
// user opened detail then somehow cleared dm.issue). Centered
// vertically + horizontally so the pane reads as intentionally
// empty rather than broken.
func splitDetailEmptyHint(innerW, innerH int) string {
	hint := subtleStyle.Render("select an issue from the list pane")
	if innerW <= 0 || innerH <= 0 {
		return hint
	}
	return lipgloss.Place(innerW, innerH, lipgloss.Center, lipgloss.Center, hint)
}

// splitPaneStyle returns the border style for one pane. The focused
// pane uses panelActiveBorder (magenta); the inactive pane uses
// panelInactiveBorder (gray). Width/Height set the OUTER dimensions
// so callers know how much space the rendered string occupies.
func splitPaneStyle(focused bool, paneW, paneH int) lipgloss.Style {
	border := panelInactiveBorder
	if focused {
		border = panelActiveBorder
	}
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(border).
		Width(paneW - 2).
		Height(paneH - 2)
}

// renderSplitInfoLine renders the shared info line at the bottom of
// the split layout (one row above the footer). Mirrors the per-view
// info line: panel prompt > status > SSE-degraded > toast > scroll
// indicator. Sourced from whichever pane is focused so the right
// pane's contextual hint surfaces.
func renderSplitInfoLine(width int, m Model) string {
	body := splitInfoBody(m)
	return statsLineStyle.Render(padToWidth(body, titleBarInnerWidth(width)))
}

// splitInfoBody picks the info-line text for the split layout from
// the focused pane's state. Panel prompts and command bars own the
// info row when active; otherwise fall through to the focused
// pane's status text or any active toast/SSE hint.
func splitInfoBody(m Model) string {
	chrome := m.chrome()
	if chrome.input.kind.isCommandBar() {
		return renderInfoBar(chrome.input, titleBarInnerWidth(m.width))
	}
	if chrome.input.kind.isPanelPrompt() {
		return renderInfoPrompt(chrome.input, titleBarInnerWidth(m.width))
	}
	if m.focus == focusList && m.list.status != "" {
		return m.list.status
	}
	if m.focus == focusDetail && m.detail.status != "" {
		return m.detail.status
	}
	if chrome.sseStatus != sseConnected {
		return sseDegradedFlash(chrome.sseStatus)
	}
	if chrome.toast != nil {
		return chrome.toast.text
	}
	return ""
}

// renderSplitFooter renders the shared footer help row for the split
// layout. The help items track the focused pane: list bindings when
// focusList; detail bindings when focusDetail. The right-aligned
// position indicator only renders for the list pane (the detail pane
// has its own per-tab indicator on the info line above).
func renderSplitFooter(width int, m Model) string {
	items := footerHints(splitFooterContext(m))
	left := joinHelpItems(items)
	right := splitFooterRight(m)
	body := padLeftRightInside(left, right, titleBarInnerWidth(width))
	return footerBarStyle.Render(body)
}

// splitFooterRight returns the right-aligned text shown on the
// shared split footer. List focus surfaces the [N/M] cursor
// position; detail focus surfaces the focused-pane label so the user
// can read the focus state textually as well as via the border
// color (multi-redundant cue, helpful in colorNone mode).
func splitFooterRight(m Model) string {
	if m.focus == focusList {
		return footerPositionIndicator(m.list.cursor, len(m.list.visibleRows()))
	}
	return ""
}

// ViewSplit renders the detail body for the M6 split-mode detail
// pane. Same composition as View but skips the outer title bar
// (renderSplit owns the shared top row) and the info+footer (also
// shared). The remaining vertical budget is split between body and
// the active tab in the same 2/3 / 1/3 ratio as the stacked
// renderer. Loading / no-issue states match View.
//
// chrome is currently unused — renderSplit owns the shared title
// bar / info / footer — but the parameter is kept for symmetry with
// View() so future per-pane chrome (status flash, scroll indicator)
// has a wire-up path without churning the call sites.
func (dm detailModel) ViewSplit(width, height int, _ viewChrome) string {
	if dm.loading {
		return statusStyle.Render("loading…")
	}
	if dm.issue == nil {
		return statusStyle.Render("no issue selected")
	}
	if width <= 0 || height < 6 {
		return dm.renderTinyFallback(width)
	}
	meta := renderHeaderMeta(*dm.issue)
	assign := renderHeaderAssignment(width, *dm.issue)
	titleRow := renderHeaderTitle(width, *dm.issue)
	bodyRule := renderLabeledRule("body", width)
	activityRule := renderLabeledRule("activity", width)
	tabs := dm.renderTabStrip()
	// Reserve six fixed rows in the pane:
	//   meta + assign + title + bodyRule + activityRule + tabs.
	// The remaining height splits between body content (2/3) and
	// active-tab content (1/3) — same ratio as the stacked renderer.
	avail := height - 6
	if avail < detailMinSplit {
		avail = detailMinSplit
	}
	bodyA := avail * 2 / 3
	if bodyA < detailMinBodyRows {
		bodyA = detailMinBodyRows
	}
	tabA := avail - bodyA
	if tabA < detailMinTabRows {
		tabA = detailMinTabRows
	}
	body := dm.renderBody(width, bodyA)
	tabContent := dm.renderActiveTab(width, tabA)
	bodyArea := dm.padArea(body, bodyA, width)
	tabArea := dm.padArea(tabContent, tabA, width)
	return strings.Join([]string{
		meta, assign, titleRow, bodyRule, bodyArea, activityRule, tabs, tabArea,
	}, "\n")
}

// pickHighlightedIssue returns a copy of the issue currently under
// the list cursor in the filtered slice, or false when the list is
// empty. Used by the M6 detail-follows-cursor path to retarget
// dm.issue immediately as the cursor moves.
func pickHighlightedIssue(lm listModel) (Issue, bool) {
	rows := lm.visibleRows()
	if len(rows) == 0 {
		return Issue{}, false
	}
	idx := lm.cursor
	if idx >= len(rows) {
		idx = len(rows) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return rows[idx].issue, true
}
