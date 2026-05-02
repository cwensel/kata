package tui

import (
	"strings"
	"testing"
)

func TestFooterHints_ListContextRowsAndChildren(t *testing.T) {
	tests := []struct {
		name string
		ctx  footerContext
		want []helpRow
		deny []helpRow
	}{
		{
			name: "row with children exposes expand and new child",
			ctx: footerContext{
				view: viewList, input: inputNone, hasRows: true, hasChildren: true,
			},
			want: []helpRow{{key: "space", desc: "expand"}, {key: "N", desc: "child"}},
		},
		{
			name: "leaf omits expand",
			ctx:  footerContext{view: viewList, input: inputNone, hasRows: true},
			deny: []helpRow{{key: "space", desc: "expand"}},
		},
		{
			name: "empty queue omits new child",
			ctx:  footerContext{view: viewList, input: inputNone},
			deny: []helpRow{{key: "N", desc: "child"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := footerHints(tt.ctx)
			for _, want := range tt.want {
				assertHelpRowPresent(t, got, want)
			}
			for _, deny := range tt.deny {
				assertHelpRowAbsent(t, got, deny)
			}
		})
	}
}

func TestFooterHints_DetailContexts(t *testing.T) {
	comments := footerHints(footerContext{
		view: viewDetail, input: inputNone, detailFocus: focusActivity, activeTab: tabComments,
	})
	for _, want := range []helpRow{
		{key: "c", desc: "comment"},
		{key: "e", desc: "edit"},
		{key: "+", desc: "label"},
		{key: "a", desc: "owner"},
	} {
		assertHelpRowPresent(t, comments, want)
	}

	children := footerHints(footerContext{
		view: viewDetail, input: inputNone, detailFocus: focusChildren, hasChildren: true,
	})
	for _, want := range []helpRow{
		{key: "↵", desc: "open child"},
		{key: "N", desc: "child"},
	} {
		assertHelpRowPresent(t, children, want)
	}
}

func TestFooterHints_InputAndModalContexts(t *testing.T) {
	tests := []struct {
		name string
		ctx  footerContext
		want []helpRow
	}{
		{
			name: "search bar",
			ctx:  footerContext{view: viewList, input: inputSearchBar},
			want: []helpRow{
				{key: "enter", desc: "commit"},
				{key: "esc", desc: "cancel"},
				{key: "ctrl+u", desc: "clear"},
			},
		},
		{
			name: "filter form",
			ctx:  footerContext{view: viewList, input: inputFilterForm},
			want: []helpRow{
				{key: "ctrl+s", desc: "apply"},
				{key: "esc", desc: "cancel"},
				{key: "ctrl+r", desc: "reset"},
			},
		},
		{
			name: "quit modal",
			ctx:  footerContext{view: viewList, modal: modalQuitConfirm},
			want: []helpRow{
				{key: "y", desc: "confirm"},
				{key: "n/esc", desc: "cancel"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := footerHints(tt.ctx)
			for _, want := range tt.want {
				assertHelpRowPresent(t, got, want)
			}
		})
	}
}

func TestListViewFooterUsesContextualHints(t *testing.T) {
	parentNum := int64(1)
	lm := listModel{issues: []Issue{
		{ProjectID: 7, Number: parentNum, Title: "parent", Status: "open"},
		{ProjectID: 7, Number: 2, ParentNumber: &parentNum, Title: "child", Status: "open"},
	}}
	got := stripANSI(lm.View(120, 12, viewChrome{}))
	assertStringContains(t, got, "space expand")
	assertStringContains(t, got, "N child")
}

func TestDetailViewFooterUsesChildrenFocusHints(t *testing.T) {
	dm := detailModel{
		issue:       &Issue{Number: 1, Title: "parent", Status: "open"},
		children:    []Issue{{Number: 2, Title: "child", Status: "open"}},
		detailFocus: focusChildren,
	}
	got := stripANSI(dm.View(120, 18, viewChrome{}))
	assertStringContains(t, got, "open child")
	assertStringContains(t, got, "N child")
}

func assertHelpRowPresent(t *testing.T, rows []helpRow, want helpRow) {
	t.Helper()
	for _, row := range rows {
		if row == want {
			return
		}
	}
	t.Fatalf("footer rows missing %+v in %+v", want, rows)
}

func assertHelpRowAbsent(t *testing.T, rows []helpRow, deny helpRow) {
	t.Helper()
	for _, row := range rows {
		if row == deny {
			t.Fatalf("footer rows unexpectedly contain %+v in %+v", deny, rows)
		}
	}
}

func assertStringContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output missing %q:\n%s", want, got)
	}
}
