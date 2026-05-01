package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fetchIssue wraps Client.GetIssue for the Enter-jump path. The 5s
// ceiling matches fetchInitial so the detail view honors the same
// budget as every other read.
func fetchIssue(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		issue, err := api.GetIssue(ctx, projectID, number)
		return detailFetchedMsg{issue: issue, err: err}
	}
}

// fetchComments wraps Client.ListComments for use as a tea.Cmd. The 5s
// ceiling matches fetchInitial so the detail view honors the same
// budget as the list-fetch path.
func fetchComments(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		comments, err := api.ListComments(ctx, projectID, number)
		return commentsFetchedMsg{comments: comments, err: err}
	}
}

// fetchEvents wraps Client.ListEvents for use as a tea.Cmd.
func fetchEvents(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		events, err := api.ListEvents(ctx, projectID, number)
		return eventsFetchedMsg{events: events, err: err}
	}
}

// fetchLinks wraps Client.ListLinks for use as a tea.Cmd.
func fetchLinks(api detailAPI, projectID, number int64) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		links, err := api.ListLinks(ctx, projectID, number)
		return linksFetchedMsg{links: links, err: err}
	}
}
