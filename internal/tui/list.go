package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// listAPI is the subset of *Client the list-view Update path needs.
// Defining it as an interface (instead of taking *Client directly) lets
// list_filter_test.go drive Update with a fake without standing up a
// httptest server — the client tests already cover the wire format.
type listAPI interface {
	ListIssues(ctx context.Context, projectID int64, f ListFilter) ([]Issue, error)
	ListAllIssues(ctx context.Context, f ListFilter) ([]Issue, error)
	CreateIssue(
		ctx context.Context, projectID int64, body CreateIssueBody,
	) (*MutationResp, error)
}

// searchField names which filter is being edited inline. The shared
// searchState holds the buffer; field discriminates so Enter routes
// the buffer to the right slot of ListFilter (or to CreateIssue).
type searchField int

const (
	searchFieldNone searchField = iota
	searchFieldQuery
	searchFieldOwner
	searchFieldLabel
	searchFieldNewTitle
)

// listModel owns list-view state: the current rows, cursor, the filter
// in effect, an optional active inline prompt, the resolved actor for
// mutations, and a one-shot status line that the View renders below the
// table. The keymap lives on the parent Model and is passed into Update;
// one instance keeps the help view in lockstep with what handlers
// actually do.
type listModel struct {
	issues  []Issue
	cursor  int
	filter  ListFilter
	search  searchState
	actor   string
	status  string
	err     error
	loading bool
}

// searchState tracks the inline prompt. inputting=true while the user
// is typing; the buffer is committed on Enter into the slot named by
// field (filter.Search/Owner/Labels, or a new-issue title).
type searchState struct {
	inputting bool
	field     searchField
	buffer    string
}

// newListModel returns a listModel waiting for its first fetch. loading=true
// keeps the spinner-equivalent on screen until initialFetchMsg lands.
func newListModel() listModel {
	return listModel{loading: true}
}

// Update handles list-view keys and fetch results. The top-level Model
// keeps responsibility for global keys (q, ?, R) and SSE messages, but
// it must skip those handlers while lm.search.inputting is true so
// character keys reach the prompt buffer (see model.go::Update).
func (lm listModel) Update(
	msg tea.Msg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		if lm.search.inputting {
			return lm.handleSearchKey(m, api, sc)
		}
		return lm.applyNavKey(m, km, api, sc)
	case initialFetchMsg, refetchedMsg:
		lm = lm.applyFetched(m)
	case mutationDoneMsg:
		return lm.applyMutation(m, api, sc)
	}
	return lm, nil
}

// applyNavKey routes a top-level keystroke through the cursor, filter,
// and prompt handlers. It returns early when a sub-handler reports it
// has consumed the key so the cyclomatic budget per function stays
// inside the project's ≤8 limit.
func (lm listModel) applyNavKey(
	msg tea.KeyMsg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	if next, ok := lm.applyCursorKey(msg, km); ok {
		return next, nil
	}
	if next, cmd, ok := lm.applyFilterKey(msg, km, api, sc); ok {
		return next, cmd
	}
	if next, ok := lm.applyPromptKey(msg, km, sc); ok {
		return next, nil
	}
	return lm, nil
}

// applyCursorKey handles the j/k/g/G family. ok=true means the key was
// consumed.
func (lm listModel) applyCursorKey(msg tea.KeyMsg, km keymap) (listModel, bool) {
	switch {
	case km.Up.matches(msg):
		if lm.cursor > 0 {
			lm.cursor--
		}
	case km.Down.matches(msg):
		if lm.cursor < len(lm.issues)-1 {
			lm.cursor++
		}
	case km.Home.matches(msg):
		lm.cursor = 0
	case km.End.matches(msg):
		if n := len(lm.issues); n > 0 {
			lm.cursor = n - 1
		}
	default:
		return lm, false
	}
	return lm, true
}

// applyFilterKey handles s (cycle status) and c (clear). Both dispatch
// a refetch so the daemon is the source of truth for status filtering.
func (lm listModel) applyFilterKey(
	msg tea.KeyMsg, km keymap, api listAPI, sc scope,
) (listModel, tea.Cmd, bool) {
	switch {
	case km.FilterStatus.matches(msg):
		lm.filter.Status = nextStatus(lm.filter.Status)
		lm.status = ""
		return lm, lm.refetchCmd(api, sc), true
	case km.ClearFilters.matches(msg):
		lm.filter = ListFilter{IncludeDeleted: lm.filter.IncludeDeleted}
		lm.status = ""
		return lm, lm.refetchCmd(api, sc), true
	}
	return lm, nil, false
}

// applyPromptKey opens an inline prompt: '/', 'o', 'l', or 'n'.
func (lm listModel) applyPromptKey(
	msg tea.KeyMsg, km keymap, sc scope,
) (listModel, bool) {
	switch {
	case km.Search.matches(msg):
		return lm.startPrompt(searchFieldQuery), true
	case km.FilterOwner.matches(msg):
		return lm.startPrompt(searchFieldOwner), true
	case km.FilterLabel.matches(msg):
		return lm.startPrompt(searchFieldLabel), true
	case km.NewIssue.matches(msg):
		return lm.beginNewIssue(sc), true
	}
	return lm, false
}

// beginNewIssue opens the title prompt unless the scope is all-projects
// (the daemon's create endpoint is project-scoped and the TUI has no
// project picker yet). The status-line message replaces the chip strip
// briefly so the user knows why nothing happened.
func (lm listModel) beginNewIssue(sc scope) listModel {
	if sc.allProjects {
		lm.status = "cannot create from all-projects view; cd into a project"
		return lm
	}
	return lm.startPrompt(searchFieldNewTitle)
}

// startPrompt seeds the inline prompt with an empty buffer.
func (lm listModel) startPrompt(f searchField) listModel {
	lm.search = searchState{inputting: true, field: f, buffer: ""}
	lm.status = ""
	return lm
}

// nextStatus cycles "" → "open" → "closed" → "".
func nextStatus(s string) string {
	switch s {
	case "":
		return "open"
	case "open":
		return "closed"
	default:
		return ""
	}
}

// applyFetched stores the latest issue list and clamps the cursor if
// it would otherwise point past the new list end.
func (lm listModel) applyFetched(msg tea.Msg) listModel {
	switch m := msg.(type) {
	case initialFetchMsg:
		lm.loading = false
		lm.err = m.err
		if m.err == nil {
			lm.issues = m.issues
		}
	case refetchedMsg:
		lm.err = m.err
		if m.err == nil {
			lm.issues = m.issues
		}
	}
	if lm.cursor >= len(lm.issues) {
		lm.cursor = max(0, len(lm.issues)-1)
	}
	return lm
}

// applyMutation handles the reply from CreateIssue. Success seeds the
// status line and dispatches a refetch so the new row shows up; failure
// leaves the issues alone and surfaces the error in the status line.
func (lm listModel) applyMutation(
	m mutationDoneMsg, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	if m.err != nil {
		lm.status = "create failed: " + m.err.Error()
		return lm, nil
	}
	if m.kind == "create" && m.resp != nil && m.resp.Issue != nil {
		lm.status = fmt.Sprintf("created #%d", m.resp.Issue.Number)
		return lm, lm.refetchCmd(api, sc)
	}
	return lm, nil
}

// handleSearchKey processes characters while a prompt is open. Enter
// commits the buffer to the right slot; Esc cancels; printable runes
// append; Backspace deletes the trailing rune.
func (lm listModel) handleSearchKey(
	msg tea.KeyMsg, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		return lm.commitPrompt(api, sc)
	case tea.KeyEsc:
		lm.search = searchState{}
		return lm, nil
	case tea.KeyBackspace:
		lm.search.buffer = trimLastRune(lm.search.buffer)
		return lm, nil
	case tea.KeyRunes, tea.KeySpace:
		lm.search.buffer += filterPrintable(string(msg.Runes))
		return lm, nil
	}
	return lm, nil
}

// commitPrompt routes the buffer to the configured field. The empty
// case for owner/label/search clears that filter; the empty case for a
// new-issue title cancels the create entirely.
func (lm listModel) commitPrompt(api listAPI, sc scope) (listModel, tea.Cmd) {
	field, buf := lm.search.field, lm.search.buffer
	lm.search = searchState{}
	switch field {
	case searchFieldQuery:
		lm.filter.Search = buf
		return lm, lm.refetchCmd(api, sc)
	case searchFieldOwner:
		lm.filter.Owner = buf
		return lm, lm.refetchCmd(api, sc)
	case searchFieldLabel:
		lm.filter.Labels = nonEmptyLabels(buf)
		return lm, lm.refetchCmd(api, sc)
	case searchFieldNewTitle:
		return lm.submitNewIssue(buf, api, sc)
	}
	return lm, nil
}

// submitNewIssue issues a CreateIssue when the title is non-empty.
// Empty title is a quiet no-op so accidental Enter doesn't churn the
// daemon.
func (lm listModel) submitNewIssue(
	title string, api listAPI, sc scope,
) (listModel, tea.Cmd) {
	if title == "" {
		return lm, nil
	}
	actor := lm.actor
	pid := sc.projectID
	return lm, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := api.CreateIssue(ctx, pid, CreateIssueBody{
			Title: title, Actor: actor,
		})
		return mutationDoneMsg{kind: "create", resp: resp, err: err}
	}
}

// refetchCmd returns a tea.Cmd that re-fetches the issue list using
// lm.filter for client-side fields (later tasks may filter in-memory)
// while the wire still only honors Status. The command path mirrors
// fetchInitial.
func (lm listModel) refetchCmd(api listAPI, sc scope) tea.Cmd {
	filter := lm.filter
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var (
			issues []Issue
			err    error
		)
		if sc.allProjects {
			issues, err = api.ListAllIssues(ctx, filter)
		} else {
			issues, err = api.ListIssues(ctx, sc.projectID, filter)
		}
		return refetchedMsg{issues: issues, err: err}
	}
}

// nonEmptyLabels splits on commas and drops empty results, so the user
// can either commit one label or several at once. Empty input clears
// the slice so 'l' followed by Enter unsets the filter.
func nonEmptyLabels(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// trimLastRune removes the last rune of s. Iterating once over the
// string keeps multi-byte runes intact (a naive s[:len(s)-1] would
// chop the trailing byte of a multi-byte rune in half).
func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return string(r[:len(r)-1])
}

// filterPrintable strips non-printable runes from the buffer keystroke.
// tea.KeyRunes for control codes occasionally arrives as a rune slice
// containing \x00 etc.; this keeps the prompt clean.
func filterPrintable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
