package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// inputKind discriminates which input shell is active. Plan 7 §"Input
// shell taxonomy": three shells (inline command bar, panel-local
// prompt, centered form) backed by one shared component family.
//
// M3a implements only inputSearchBar and inputOwnerBar (the inline
// command bar). M3b adds the panel-local prompt kinds; M4 adds the
// centered form kinds.
type inputKind int

const (
	inputNone inputKind = iota
	inputSearchBar
	inputOwnerBar
	inputLabelPrompt        // detail `+` — add label
	inputRemoveLabelPrompt  // detail `-` — remove label
	inputOwnerPrompt        // detail `a` — assign owner
	inputParentPrompt       // detail `p` — set parent
	inputBlockerPrompt      // detail `b` — add blocker
	inputLinkPrompt         // detail `L` — add link "kind number"
	inputNewIssueRow        // list `n` — inline title row at top of table
	inputBodyEditForm       // detail `e` — centered multi-line body editor
	inputBodyEditPostCreate // post-create chain — body editor for newly-created issue
	inputCommentForm        // detail `c` — centered multi-line comment editor
)

// isPanelPrompt reports whether a kind is one of the M3b panel-local
// prompt kinds (anchored to the bottom of the detail pane).
func (k inputKind) isPanelPrompt() bool {
	switch k {
	case inputLabelPrompt, inputRemoveLabelPrompt, inputOwnerPrompt,
		inputParentPrompt, inputBlockerPrompt, inputLinkPrompt:
		return true
	}
	return false
}

// isCommandBar reports whether a kind is one of the M3a inline
// command bar kinds (replaces the chip strip).
func (k inputKind) isCommandBar() bool {
	return k == inputSearchBar || k == inputOwnerBar
}

// isCenteredForm reports whether a kind is one of the M4 centered
// form kinds (multi-line textarea, ctrl+s commit, esc cancel,
// ctrl+e $EDITOR escape hatch).
func (k inputKind) isCenteredForm() bool {
	switch k {
	case inputBodyEditForm, inputBodyEditPostCreate, inputCommentForm:
		return true
	}
	return false
}

// fieldKind picks the bubbles component backing an inputField.
type fieldKind int

const (
	fieldSingleLine fieldKind = iota
	fieldMultiLine
)

// inputField is one editable field on an input. Bars and prompts have
// a single field; centered forms have two or more. The bubbles models
// are populated based on kind — never both at once.
//
// label and required are reserved for M4's centered-form rendering
// (label appears above each field; required gates the form's commit).
// The unused linter complains because M3a only needs kind+input. The
// nolint annotations document the milestone the fields land for.
type inputField struct {
	label    string //nolint:unused // reserved for M4 centered-form labels
	kind     fieldKind
	input    textinput.Model // populated when kind == fieldSingleLine
	area     textarea.Model  // populated when kind == fieldMultiLine
	required bool            //nolint:unused // reserved for M4 form validation
}

// value returns the current text from whichever bubbles model backs f.
func (f *inputField) value() string {
	if f.kind == fieldMultiLine {
		return f.area.Value()
	}
	return f.input.Value()
}

// setValue mirrors a string into whichever bubbles model backs f.
// Used by the $EDITOR escape hatch (M4) when handing a buffer back to
// a multi-line field on resume.
func (f *inputField) setValue(s string) {
	if f.kind == fieldMultiLine {
		f.area.SetValue(s)
		return
	}
	f.input.SetValue(s)
}

// focus / blur delegate to the bubbles model so cursor visibility +
// key dispatch flip correctly when the active field changes. Used by
// M4's multi-field forms when tab cycles fields. The nolint silences
// the M3a-vs-M4 dead-code lint until M4 wires it up.
//
//nolint:unused // reserved for M4 form field-cycling
func (f *inputField) focus() tea.Cmd {
	if f.kind == fieldMultiLine {
		return f.area.Focus()
	}
	return f.input.Focus()
}

//nolint:unused // reserved for M4 form field-cycling
func (f *inputField) blur() {
	if f.kind == fieldMultiLine {
		f.area.Blur()
		return
	}
	f.input.Blur()
}

// inputState carries every active-input case. The renderer dispatches
// on kind to pick the chrome (bar / prompt / form). The data path is
// uniform — caller drives keys through Update; on actionCommit it
// reads field values and dispatches the mutation; on actionCancel it
// discards and restores any pre-open snapshot.
//
// preFilter is the listFilter snapshot captured when an inline
// command bar opened, so a cancel can revert any live-applied changes.
// Empty filter for non-bar inputs.
//
// target / err / saving / formGen are populated for centered-form and
// panel-prompt kinds. target carries the issue context so a stale
// editor return / label-suggestion fetch cannot land on the wrong
// issue; formGen is the per-form monotonic ID (assigned by
// Model.openInput at form-open time) used to reject stale
// editorReturnedMsg whose form has since closed or re-opened.
//
// suggestHighlight / suggestScroll back the autocomplete menu on the
// `+` and `-` panel prompts. highlight is the index of the selected
// suggestion (cycles 0..N-1 with wrap on ↑↓); scroll is the first
// visible row in the menu when the entry list overflows the menu's
// height. Both are zero on every non-suggesting input kind.
type inputState struct {
	kind             inputKind
	title            string
	fields           []inputField
	active           int
	err              string
	saving           bool
	preFilter        ListFilter
	target           formTarget
	formGen          int64
	suggestHighlight int
	suggestScroll    int
}

// formTarget carries the issue identity a centered form is acting
// on. Threaded into the form when it opens, into the editor handoff
// (so the return can be matched against the still-open form), and
// into the mutation dispatch (so a stale response on the daemon
// side can be discarded against detail.gen). projectID + issueNumber
// are zero for forms that don't yet have a target (none today, but
// the shape leaves room for forward-looking shells).
type formTarget struct {
	projectID   int64
	issueNumber int64
	detailGen   int64
}

// inputAction names what the caller should do after Update. Actions
// drive the Model-level handler, not the input itself.
type inputAction int

const (
	actionNone inputAction = iota
	actionCommit
	actionCancel
	// actionEditorHandoff: a centered form requested the $EDITOR
	// escape hatch (ctrl+e). Model-level handler launches editorCmd
	// with the form's current buffer and formGen tag; the resulting
	// editorReturnedMsg writes the content back into the form.
	actionEditorHandoff
)

// activeField returns a pointer to the currently-focused field so
// callers can read its value or mutate it (e.g. ctrl+e handoff).
func (s *inputState) activeField() *inputField {
	if s == nil || len(s.fields) == 0 {
		return nil
	}
	idx := s.active
	if idx < 0 || idx >= len(s.fields) {
		idx = 0
	}
	return &s.fields[idx]
}

// Update routes a key into the active field and reports the action
// the caller should take. Centered forms route differently from bars
// and prompts: ctrl+s commits (Enter inserts a newline into the
// textarea); ctrl+e requests the $EDITOR escape hatch; saving=true
// blocks duplicate commits while a mutation is in flight.
//
// Label-prompt kinds (`+` / `-`) intercept ↑/↓/⇥ BEFORE delegating to
// the textinput so the autocomplete menu's highlight cursor moves
// (and ⇥ completes) without the keys reaching bubbles' own input
// handler — bubbles would otherwise interpret arrow keys as intra-
// buffer cursor motion, which makes no sense for a single-line cell.
// The Update path returns actionNone for those keys; the caller's
// suggestion source (m.suggestionsForPrompt) is consulted at render
// time to project the new highlight.
func (s inputState) Update(msg tea.KeyMsg) (inputState, inputAction) {
	if s.kind.isCenteredForm() {
		return s.updateForm(msg)
	}
	switch msg.Type {
	case tea.KeyEnter:
		return s, actionCommit
	case tea.KeyEsc:
		return s, actionCancel
	case tea.KeyCtrlU:
		s.activeField().setValue("")
		return s, actionNone
	}
	if isLabelPromptKind(s.kind) {
		if next, handled := s.handleSuggestKey(msg); handled {
			return next, actionNone
		}
	}
	return s.delegateToField(msg)
}

// isLabelPromptKind reports whether kind is one of the autocomplete-
// backed panel-prompt kinds (`+` add label, `-` remove label).
func isLabelPromptKind(k inputKind) bool {
	return k == inputLabelPrompt || k == inputRemoveLabelPrompt
}

// handleSuggestKey dispatches the navigation keys that the suggestion
// menu owns: ↑/↓ move the highlight (with wrap), ⇥ completes the
// active buffer to the highlighted suggestion's label. Returns
// handled=true when the key was consumed by the menu so the caller
// knows not to forward it to the textinput (which would otherwise
// move the buffer cursor or insert a tab character).
//
// The actual suggestion list isn't on inputState — it's recomputed at
// render time from m.suggestionsForPrompt — so handleSuggestKey only
// adjusts the highlight index. Callers wrap the index modulo the
// projected list length when they read it; we don't need to know the
// length here. ⇥ is a no-op when the buffer is empty (no completion
// target candidate yet); the renderer surfaces the suggestion list
// either way.
func (s inputState) handleSuggestKey(msg tea.KeyMsg) (inputState, bool) {
	switch msg.Type {
	case tea.KeyUp:
		s.suggestHighlight--
		return s, true
	case tea.KeyDown:
		s.suggestHighlight++
		return s, true
	case tea.KeyTab:
		// Tab completion of the buffer happens at the Model layer
		// where the suggestion list (LabelCount slice) is in scope.
		// Here we just signal "handled" so the textinput doesn't
		// receive the tab keystroke. The completion itself lives in
		// Model.completeFromSuggestion (called from routeInputKey).
		return s, true
	}
	return s, false
}

// updateForm is the Update path for centered forms. ctrl+s commits
// (Model-level handler validates kind-specific empty rules); esc
// cancels; ctrl+e hands off to $EDITOR; everything else (including
// Enter for newline insertion and tea.PasteMsg blobs from bracketed
// paste) delegates to the textarea so paste, cursor moves, and
// editing all work natively.
//
// While saving=true, ctrl+s is absorbed (no duplicate dispatches).
func (s inputState) updateForm(msg tea.KeyMsg) (inputState, inputAction) {
	switch msg.Type {
	case tea.KeyCtrlS:
		if s.saving {
			return s, actionNone
		}
		return s, actionCommit
	case tea.KeyEsc:
		return s, actionCancel
	case tea.KeyCtrlE:
		return s, actionEditorHandoff
	}
	return s.delegateToField(msg)
}

// delegateToField forwards a key into the active field's bubbles
// model so paste, cursor motion, backspace, arrow keys all work.
func (s inputState) delegateToField(msg tea.KeyMsg) (inputState, inputAction) {
	f := s.activeField()
	if f == nil {
		return s, actionNone
	}
	if f.kind == fieldMultiLine {
		var cmd tea.Cmd
		f.area, cmd = f.area.Update(msg)
		_ = cmd
		s.fields[s.active] = *f
		return s, actionNone
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	_ = cmd
	s.fields[s.active] = *f
	return s, actionNone
}

// newSearchBar constructs the inline command bar for `/` (search).
// preFilter snapshots the caller's current filter so a cancel can
// revert. The bar text input has no placeholder — empty bar reads as
// "type to search."
func newSearchBar(current ListFilter) inputState {
	ti := textinput.New()
	ti.SetValue(current.Search)
	ti.Focus()
	ti.Prompt = ""
	return inputState{
		kind:      inputSearchBar,
		title:     "search",
		fields:    []inputField{{kind: fieldSingleLine, input: ti}},
		preFilter: current,
	}
}

// newOwnerBar mirrors newSearchBar for the `o` (owner filter) key.
func newOwnerBar(current ListFilter) inputState {
	ti := textinput.New()
	ti.SetValue(current.Owner)
	ti.Focus()
	ti.Prompt = ""
	return inputState{
		kind:      inputOwnerBar,
		title:     "owner",
		fields:    []inputField{{kind: fieldSingleLine, input: ti}},
		preFilter: current,
	}
}

// newNewIssueRow constructs the M3.5c inline new-issue row that
// renders at the top of the list table. Single textinput field for
// the title; commits to api.CreateIssue with empty body. M4 will
// chain the centered body form after create for optional
// refinement.
func newNewIssueRow() inputState {
	ti := textinput.New()
	ti.Focus()
	ti.Prompt = ""
	return inputState{
		kind:   inputNewIssueRow,
		title:  "new issue",
		fields: []inputField{{kind: fieldSingleLine, input: ti}},
	}
}

// newPanelPrompt constructs an M3b panel-local prompt for kind. The
// title carries the issue context so the user sees "add label to #42"
// in the prompt's border. target carries projectID + issueNumber +
// detailGen so the autocomplete dispatch (label suggestions) and any
// future stale-response checks can scope themselves to the right
// issue.
func newPanelPrompt(kind inputKind, target formTarget) inputState {
	ti := textinput.New()
	ti.Focus()
	ti.Prompt = ""
	return inputState{
		kind:   kind,
		title:  panelPromptTitle(kind, target.issueNumber),
		fields: []inputField{{kind: fieldSingleLine, input: ti}},
		target: target,
	}
}

// panelPromptTitle is the verbal label that appears in the prompt
// border. Mirrors the modalLabel mapping from the now-retired
// modal.go but reads as a sentence ("add label to #42") rather than
// a CLI-style colon prefix.
func panelPromptTitle(kind inputKind, n int64) string {
	switch kind {
	case inputLabelPrompt:
		return fmt.Sprintf("add label to #%d", n)
	case inputRemoveLabelPrompt:
		return fmt.Sprintf("remove label from #%d", n)
	case inputOwnerPrompt:
		return fmt.Sprintf("assign #%d to", n)
	case inputParentPrompt:
		return fmt.Sprintf("set parent of #%d", n)
	case inputBlockerPrompt:
		return fmt.Sprintf("add blocker to #%d", n)
	case inputLinkPrompt:
		return fmt.Sprintf("add link to #%d (kind number)", n)
	}
	return ""
}

// formMinHeight / formMinWidth are the smallest terminal cells we'll
// open a centered form on. Below either, the form falls back to a
// degraded inline render via renderTinyFormFallback.
const (
	formMinHeight = 12
	formMinWidth  = 40
)

// newBodyEditForm constructs the centered multi-line editor opened by
// `e` on the detail view. current pre-fills the textarea with the
// existing body so the user starts on top of what's there. esc
// closes the form (returns to detail); ctrl+s dispatches EditBody;
// ctrl+e suspends to $EDITOR.
func newBodyEditForm(target formTarget, current string) inputState {
	return inputState{
		kind:   inputBodyEditForm,
		title:  fmt.Sprintf("edit body of #%d", target.issueNumber),
		fields: []inputField{newFormTextarea(current)},
		target: target,
	}
}

// newBodyEditPostCreate is opened automatically after a successful
// inline-row create commits. The textarea starts empty (the issue
// already exists with no body); esc keeps it that way and returns
// to the new issue's detail view; ctrl+s dispatches EditBody.
func newBodyEditPostCreate(target formTarget) inputState {
	return inputState{
		kind:   inputBodyEditPostCreate,
		title:  fmt.Sprintf("add body to #%d", target.issueNumber),
		fields: []inputField{newFormTextarea("")},
		target: target,
	}
}

// newCommentForm is the centered multi-line comment editor opened
// by `c` on the detail view. esc cancels (no comment posted);
// ctrl+s dispatches AddComment; empty content blocks commit per the
// kind-specific gate (comments must have content; clearing a body is
// legitimate but posting an empty comment is not).
func newCommentForm(target formTarget) inputState {
	return inputState{
		kind:   inputCommentForm,
		title:  fmt.Sprintf("comment on #%d", target.issueNumber),
		fields: []inputField{newFormTextarea("")},
		target: target,
	}
}

// newFormTextarea builds the bubbles textarea backing a centered
// form's only field. Pre-filled with current; focused so the cursor
// renders immediately; soft-wrap on so long lines fold inside the
// modal panel instead of horizontal-scrolling.
func newFormTextarea(current string) inputField {
	ta := textarea.New()
	ta.SetValue(current)
	ta.Focus()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	return inputField{kind: fieldMultiLine, area: ta}
}
