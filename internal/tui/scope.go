package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// handleScopeToggle implements the R binding. Three cases:
//   - currently single-project → switch to all-projects.
//   - currently all-projects + boot resolved a home project → switch back.
//   - currently all-projects + no home project → emit a "no project bound"
//     toast and stay put.
//
// On a successful switch the cache is dropped, the list model is reset
// (loading=true, no rows, cursor=0, no filter), and a fresh fetchInitial
// is dispatched so the new scope's data lands. Filters are intentionally
// cleared because most filters (owner, search) make no sense across the
// scope boundary; a future iteration could carry status across.
func (m Model) handleScopeToggle() (Model, tea.Cmd) {
	if m.scope.allProjects {
		if m.scope.homeProjectID == 0 {
			return m.toastNoBinding()
		}
		m.scope.allProjects = false
		m.scope.projectID = m.scope.homeProjectID
		m.scope.projectName = m.scope.homeProjectName
		return m.applyScopeChange()
	}
	m.scope.allProjects = true
	m.scope.projectID = 0
	m.scope.projectName = ""
	return m.applyScopeChange()
}

// applyScopeChange resets list-view state and dispatches a fresh
// fetchInitial so the new scope's data populates. Cache is dropped so
// SSE-driven refetches start clean. m.list is replaced by a fresh
// listModel — the pre-toggle actor is preserved because resolveTUIActor
// is deterministic and the value re-derives at construction.
func (m Model) applyScopeChange() (Model, tea.Cmd) {
	if m.cache != nil {
		m.cache.drop()
	}
	priorActor := m.list.actor
	m.list = newListModel()
	m.list.actor = priorActor
	m.pendingRefetch = false
	if m.api == nil {
		return m, nil
	}
	return m, m.fetchInitial()
}

// toastNoBinding surfaces the "no project bound" hint. The TTL gives the
// user time to read and matches the resynced toast's cadence.
func (m Model) toastNoBinding() (Model, tea.Cmd) {
	m.toast = &toast{
		text:      "no project bound; run `kata init`",
		level:     toastError,
		expiresAt: m.toastNow().Add(toastNoBindingTTL),
	}
	return m, toastExpireCmd(toastNoBindingTTL)
}

// renderEmpty draws the centered onboarding hint shown when the daemon
// has zero registered projects. lipgloss.Place handles vertical and
// horizontal centering inside width × height; small terminals fall back
// to top-left placement (lipgloss caps the offsets) so the message
// remains visible.
func renderEmpty(width, height int) string {
	body := strings.Join([]string{
		titleStyle.Render("no kata projects registered yet"),
		"",
		subtleStyle.Render("run `kata init` in a repo to get started."),
		subtleStyle.Render("press q to quit."),
	}, "\n")
	if width <= 0 || height <= 0 {
		return body
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}
