package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// modalKind names which mutation prompt is open. modalNone is the
// quiescent state — render skips the modal line and key dispatch falls
// through to normal handlers. The other kinds discriminate which
// detail-side mutation the buffer should drive on commit.
type modalKind int

const (
	modalNone modalKind = iota
	modalAddLabel
	modalRemoveLabel
	modalAssignOwner
	modalSetParent
	modalAddBlocker
	modalAddLink
)

// modalAction is the result of one HandleKey call. modalIdle is the
// pass-through case (printable rune, backspace, no terminal action);
// modalCommit means Enter was pressed — caller should read the buffer
// off the value and dispatch the mutation; modalCancel means Esc was
// pressed — caller should clear the modal and do nothing else.
type modalAction int

const (
	modalIdle modalAction = iota
	modalCommit
	modalCancel
)

// modal is a single-line text-prompt component. Used by the detail view
// for mutation prompts (label name, owner name, link target). The
// listModel uses its own searchState because the prompt set there pre-
// dates the modal type and the two have slightly different commit
// semantics (refetch vs mutation dispatch); consolidating later if the
// shapes converge.
type modal struct {
	kind   modalKind
	label  string // prompt prefix, e.g. "label:"
	buffer string // accumulated keystrokes
}

// open seeds the modal with the prompt text for kind.
func openModal(k modalKind) modal {
	return modal{kind: k, label: modalLabel(k)}
}

// modalLabel maps a kind to its prompt prefix.
func modalLabel(k modalKind) string {
	switch k {
	case modalAddLabel:
		return "label:"
	case modalRemoveLabel:
		return "remove label:"
	case modalAssignOwner:
		return "assign to:"
	case modalSetParent:
		return "parent #:"
	case modalAddBlocker:
		return "blocker #:"
	case modalAddLink:
		return "link (kind number):"
	default:
		return ""
	}
}

// active reports whether the modal is currently soliciting input. Used
// by the dispatch path to gate global keys (q/?/R) on modal input.
func (m modal) active() bool { return m.kind != modalNone }

// HandleKey is the modal key path. Enter commits, Esc cancels, printable
// runes append, backspace trims. Other keys (arrows, tabs) are ignored
// while the modal is open so the user's typing reaches the buffer.
func (m modal) HandleKey(msg tea.KeyMsg) (modal, modalAction) {
	switch msg.Type {
	case tea.KeyEnter:
		return m, modalCommit
	case tea.KeyEsc:
		return modal{}, modalCancel
	case tea.KeyBackspace:
		m.buffer = trimLastRune(m.buffer)
		return m, modalIdle
	case tea.KeyRunes, tea.KeySpace:
		m.buffer += filterPrintable(string(msg.Runes))
		return m, modalIdle
	}
	return m, modalIdle
}

// Render formats the modal for the View pipeline. Empty kind returns
// "" so the rendered detail view stays unchanged when no modal is open.
func (m modal) Render() string {
	if m.kind == modalNone {
		return ""
	}
	return chipActive.Render(fmt.Sprintf("%s %s_  (esc to cancel)", m.label, m.buffer))
}
