package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fetchIssue wraps Client.GetIssue for the Enter-jump path. The 5s
// ceiling matches fetchInitial so the detail view honors the same
// budget as every other read. gen tags the detail-open generation so
// applyFetched can discard the result if the user jumped or popped
// before the request finished.
func fetchIssue(api detailAPI, projectID, number, gen int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		issue, err := api.GetIssue(ctx, projectID, number)
		return detailFetchedMsg{gen: gen, issue: issue, err: err}
	}
}

// fetchComments wraps Client.ListComments for use as a tea.Cmd. The 5s
// ceiling matches fetchInitial so the detail view honors the same
// budget as the list-fetch path. See fetchIssue for the gen rationale.
func fetchComments(api detailAPI, projectID, number, gen int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		comments, err := api.ListComments(ctx, projectID, number)
		return commentsFetchedMsg{gen: gen, comments: comments, err: err}
	}
}

// fetchEvents wraps Client.ListEvents for use as a tea.Cmd.
func fetchEvents(api detailAPI, projectID, number, gen int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		events, err := api.ListEvents(ctx, projectID, number)
		return eventsFetchedMsg{gen: gen, events: events, err: err}
	}
}

// fetchLinks wraps Client.ListLinks for use as a tea.Cmd.
func fetchLinks(api detailAPI, projectID, number, gen int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		links, err := api.ListLinks(ctx, projectID, number)
		return linksFetchedMsg{gen: gen, links: links, err: err}
	}
}
