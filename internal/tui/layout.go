package tui

// layoutMode discriminates between the stacked single-view layout
// (the M1-M5 default) and the M6 split-pane layout that renders the
// list and detail side-by-side. Re-evaluated on every WindowSizeMsg
// via pickLayout so a resize across the breakpoint flips the layout
// in both directions.
type layoutMode int

const (
	layoutStacked layoutMode = iota
	layoutSplit
)

// focusPane names which pane owns key dispatch in split layout. Only
// meaningful when m.layout == layoutSplit; in stacked layout m.view
// is the authoritative dispatch state. Tab/Enter from focusList →
// focusDetail; Esc from focusDetail → focusList.
type focusPane int

const (
	focusList focusPane = iota
	focusDetail
)

// splitListPaneWidth is the fixed cell width of the list pane in
// split layout. Plan 7 spec said 60-64; bumped to 68 for Plan 8 row
// chips so the title column still has 20+ cells after subtracting
// the # / status / updated columns. The remaining width feeds the
// detail pane.
const splitListPaneWidth = 68

// splitMinWidth and splitMinHeight are the breakpoint thresholds for
// split layout. Below either dimension we fall back to layoutStacked
// so a too-tight terminal keeps the single-pane layout.
//
// User-confirmed thresholds (post Plan-8 chrome bump from 7 to 9
// fixed rows on detail, 5 fixed rows on list): width>=140 (68-cell
// list pane + a usable detail pane) AND height>=36 (9 fixed rows of
// detail chrome + comfortable body content per pane).
const (
	splitMinWidth  = 140
	splitMinHeight = 36
)

// pickLayout chooses between the stacked single-view layout and the
// split-pane layout based on terminal dimensions. Below either
// threshold, fall back to stacked. Re-run on every WindowSizeMsg.
func pickLayout(width, height int) layoutMode {
	if width >= splitMinWidth && height >= splitMinHeight {
		return layoutSplit
	}
	return layoutStacked
}

// handleLayoutFlip preserves selection and focus across a layout
// transition. Called from routeTopLevel's WindowSizeMsg branch when
// pickLayout returns a different mode than m.layout had before.
//
// stacked → split: derive m.focus from m.view (viewList → focusList,
// viewDetail → focusDetail) so the user's currently-focused pane
// stays the active one in the split. m.view stays as it was so any
// subsequent split → stacked flip restores the same single-pane
// rendering.
//
// split → stacked: set m.view from m.focus (focusList → viewList,
// focusDetail → viewDetail) so the user keeps seeing the pane they
// were last focused on. m.focus stays as it was so a subsequent
// stacked → split flip lands on the same pane.
//
// Selection survives in both directions because lm.selectedNumber is
// identity-based and dm.issue is a pointer the layout flip never
// touches. Other invariants (gen counters, formGen, modal state,
// SSE state) live on Model and are likewise untouched.
func (m Model) handleLayoutFlip(prev layoutMode) Model {
	if prev == layoutSplit && m.layout == layoutStacked {
		// Coming back to the stacked layout: pick the view that
		// matches the focused pane so the user keeps seeing the
		// pane they last interacted with.
		if m.focus == focusDetail && m.detail.issue != nil {
			m.view = viewDetail
		} else {
			m.view = viewList
		}
		return m
	}
	if prev == layoutStacked && m.layout == layoutSplit {
		// Entering split: derive focus from the view the user was
		// looking at. viewHelp / viewEmpty fall through to focusList
		// (the right-hand pane is informational; the list is what
		// they should be navigating).
		if m.view == viewDetail && m.detail.issue != nil {
			m.focus = focusDetail
		} else {
			m.focus = focusList
		}
		return m
	}
	return m
}
