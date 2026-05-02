package tui

type footerContext struct {
	view        viewID
	layout      layoutMode
	pane        focusPane
	input       inputKind
	modal       modalKind
	detailFocus detailFocus
	activeTab   detailTab
	hasRows     bool
	hasChildren bool
}

func footerHints(ctx footerContext) []helpRow {
	if ctx.modal == modalQuitConfirm {
		return quitModalFooterHints()
	}
	if ctx.input != inputNone {
		return inputFooterHints(ctx.input)
	}
	switch ctx.view {
	case viewDetail:
		return detailFooterHints(ctx)
	case viewList:
		return listFooterHints(ctx)
	}
	return globalFooterHints()
}

func inputFooterHints(kind inputKind) []helpRow {
	switch {
	case kind.isCommandBar():
		return []helpRow{
			{key: "enter", desc: "commit"},
			{key: "esc", desc: "cancel"},
			{key: "ctrl+u", desc: "clear"},
		}
	case kind.isPanelPrompt():
		return []helpRow{
			{key: "enter", desc: "commit"},
			{key: "esc", desc: "cancel"},
		}
	case kind == inputFilterForm:
		return []helpRow{
			{key: "ctrl+s", desc: "apply"},
			{key: "esc", desc: "cancel"},
			{key: "ctrl+r", desc: "reset"},
		}
	case kind == inputNewIssueForm:
		return []helpRow{
			{key: "ctrl+s", desc: "create"},
			{key: "esc", desc: "cancel"},
			{key: "tab", desc: "field"},
			{key: "ctrl+e", desc: "editor"},
		}
	case kind.isCenteredForm():
		return []helpRow{
			{key: "ctrl+s", desc: "save"},
			{key: "esc", desc: "cancel"},
			{key: "ctrl+e", desc: "editor"},
		}
	}
	return nil
}

func listFooterHints(ctx footerContext) []helpRow {
	rows := []helpRow{
		{key: "↑↓", desc: "move"},
		{key: "↵", desc: "open"},
	}
	if ctx.hasChildren {
		rows = append(rows, helpRow{key: "space", desc: "expand"})
	}
	rows = append(rows, helpRow{key: "n", desc: "new"})
	if ctx.hasRows {
		rows = append(rows, helpRow{key: "N", desc: "child"})
	}
	return append(rows,
		helpRow{key: "/", desc: "search"},
		helpRow{key: "f", desc: "filter"},
		helpRow{key: "s", desc: "status"},
		helpRow{key: "c", desc: "clear"},
		helpRow{key: "x", desc: "close"},
		helpRow{key: "?", desc: "help"},
		helpRow{key: "q", desc: "quit"},
	)
}

func detailFooterHints(ctx footerContext) []helpRow {
	if ctx.detailFocus == focusChildren && ctx.hasChildren {
		return []helpRow{
			{key: "↑↓", desc: "child"},
			{key: "↵", desc: "open child"},
			{key: "N", desc: "child"},
			{key: "p", desc: "parent"},
			{key: "↹", desc: "activity"},
			{key: "esc", desc: "back"},
			{key: "?", desc: "help"},
			{key: "q", desc: "quit"},
		}
	}
	return []helpRow{
		{key: "↑↓", desc: "move"},
		{key: "↹", desc: "tab"},
		{key: "↵", desc: "jump"},
		{key: "esc", desc: "back"},
		{key: "e", desc: "edit"},
		{key: "c", desc: "comment"},
		{key: "x", desc: "close"},
		{key: "+", desc: "label"},
		{key: "a", desc: "owner"},
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	}
}

func quitModalFooterHints() []helpRow {
	return []helpRow{
		{key: "y", desc: "confirm"},
		{key: "n/esc", desc: "cancel"},
	}
}

func globalFooterHints() []helpRow {
	return []helpRow{
		{key: "?", desc: "help"},
		{key: "q", desc: "quit"},
	}
}

func listFooterContext(lm listModel, chrome viewChrome) footerContext {
	row, ok := lm.targetQueueRow()
	return footerContext{
		view:        viewList,
		layout:      layoutStacked,
		pane:        focusList,
		input:       chrome.input.kind,
		hasRows:     ok,
		hasChildren: ok && row.hasChildren,
	}
}

func detailFooterContext(dm detailModel, chrome viewChrome) footerContext {
	return footerContext{
		view:        viewDetail,
		layout:      layoutStacked,
		pane:        focusDetail,
		input:       chrome.input.kind,
		detailFocus: dm.detailFocus,
		activeTab:   dm.activeTab,
		hasChildren: len(dm.children) > 0,
	}
}

func splitFooterContext(m Model) footerContext {
	chrome := m.chrome()
	if m.focus == focusDetail {
		ctx := detailFooterContext(m.detail, chrome)
		ctx.layout = layoutSplit
		ctx.pane = focusDetail
		ctx.modal = m.modal
		return ctx
	}
	ctx := listFooterContext(m.list, chrome)
	ctx.layout = layoutSplit
	ctx.pane = focusList
	ctx.modal = m.modal
	return ctx
}
