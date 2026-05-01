package tui

import (
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
	// M3b adds:
	//   inputLabelPrompt, inputOwnerPrompt, inputLinkPrompt,
	//   inputParentPrompt, inputBlockerPrompt
	// M4 adds:
	//   inputNewIssueForm, inputEditBodyForm, inputCommentForm
)

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
// err / saving are reserved for M4's centered-form validation +
// in-flight commit handling. The nolint silences the M3a-vs-M4
// dead-code lint until M4 wires them up.
type inputState struct {
	kind      inputKind
	title     string
	fields    []inputField
	active    int
	err       string //nolint:unused // reserved for M4 form validation messages
	saving    bool   //nolint:unused // reserved for M4 in-flight commit gate
	preFilter ListFilter
}

// inputAction names what the caller should do after Update. Actions
// drive the Model-level handler, not the input itself.
type inputAction int

const (
	actionNone inputAction = iota
	actionCommit
	actionCancel
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
// the caller should take. Bars commit on Enter; cancel on Esc;
// ctrl+u clears the field. Other keys delegate to the bubbles model
// for cursor / paste / backspace handling.
func (s inputState) Update(msg tea.KeyMsg) (inputState, inputAction) {
	switch msg.Type {
	case tea.KeyEnter:
		// Single-line inputs commit on Enter (bars and prompts). Forms
		// (M4) override this to advance fields / insert newlines.
		return s, actionCommit
	case tea.KeyEsc:
		return s, actionCancel
	case tea.KeyCtrlU:
		s.activeField().setValue("")
		return s, actionNone
	}
	// Delegate everything else to the bubbles model so paste / cursor
	// movement / backspace / arrow keys work.
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
