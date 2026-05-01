package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Client is the typed adapter the TUI uses to talk to the daemon. Errors
// include the request method+path so toast messages stay actionable.
type Client struct {
	base string
	hc   *http.Client
}

// NewClient wraps a pre-built *http.Client with a typed daemon adapter.
// base is the daemon URL — "http://kata.invalid" for unix-socket transport.
func NewClient(base string, hc *http.Client) *Client { return &Client{base: base, hc: hc} }

// ListIssues returns the issues for projectID filtered by f.
func (c *Client) ListIssues(ctx context.Context, projectID int64, f ListFilter) ([]Issue, error) {
	return c.listIssuesAt(ctx, fmt.Sprintf("/api/v1/projects/%d/issues", projectID), f)
}

// ListAllIssues lists issues across every project. The daemon may not yet
// implement /api/v1/issues; in that case the request surfaces as a 404
// APIError that callers can downgrade.
func (c *Client) ListAllIssues(ctx context.Context, f ListFilter) ([]Issue, error) {
	return c.listIssuesAt(ctx, "/api/v1/issues", f)
}

func (c *Client) listIssuesAt(ctx context.Context, path string, f ListFilter) ([]Issue, error) {
	if vals := f.values().Encode(); vals != "" {
		path += "?" + vals
	}
	var resp struct {
		Issues []Issue `json:"issues"`
	}
	return resp.Issues, c.do(ctx, http.MethodGet, path, nil, &resp)
}

// GetIssue fetches a single issue by number.
func (c *Client) GetIssue(ctx context.Context, projectID, number int64) (*Issue, error) {
	body, err := c.showIssue(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	return &body.Issue, nil
}

// CreateIssue posts a new issue. body.IdempotencyKey rides the
// Idempotency-Key header per spec §4.4 when non-empty.
func (c *Client) CreateIssue(
	ctx context.Context, projectID int64, body CreateIssueBody,
) (*MutationResp, error) {
	headers := map[string]string{}
	if body.IdempotencyKey != "" {
		headers["Idempotency-Key"] = body.IdempotencyKey
	}
	var resp MutationResp
	path := fmt.Sprintf("/api/v1/projects/%d/issues", projectID)
	if err := c.doWithHeaders(ctx, http.MethodPost, path, body, &resp, headers); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Close transitions the issue to status=closed.
func (c *Client) Close(
	ctx context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	return c.mutate(ctx, http.MethodPost,
		issuePath(projectID, number)+"/actions/close", actorBody(actor))
}

// Reopen transitions the issue back to status=open.
func (c *Client) Reopen(
	ctx context.Context, projectID, number int64, actor string,
) (*MutationResp, error) {
	return c.mutate(ctx, http.MethodPost,
		issuePath(projectID, number)+"/actions/reopen", actorBody(actor))
}

// AddComment appends a new comment to the issue.
func (c *Client) AddComment(
	ctx context.Context, projectID, number int64, body, actor string,
) (*MutationResp, error) {
	return c.mutate(ctx, http.MethodPost, issuePath(projectID, number)+"/comments",
		map[string]string{"body": body, "actor": actor})
}

// AddLabel attaches a label to the issue.
func (c *Client) AddLabel(
	ctx context.Context, projectID, number int64, label, actor string,
) (*MutationResp, error) {
	return c.mutate(ctx, http.MethodPost, issuePath(projectID, number)+"/labels",
		map[string]string{"label": label, "actor": actor})
}

// RemoveLabel sends actor in the query string because DELETE bodies are
// non-portable; the label is path-escaped to survive '/' and similar.
func (c *Client) RemoveLabel(
	ctx context.Context, projectID, number int64, label, actor string,
) (*MutationResp, error) {
	path := fmt.Sprintf("%s/labels/%s?actor=%s",
		issuePath(projectID, number), url.PathEscape(label), url.QueryEscape(actor))
	return c.mutate(ctx, http.MethodDelete, path, nil)
}

// Assign sets the issue owner. Empty owner routes to /actions/unassign
// because the daemon's PATCH endpoint cannot represent the clear case
// (string vs null) and /actions/assign rejects empty owners with 400.
func (c *Client) Assign(
	ctx context.Context, projectID, number int64, owner, actor string,
) (*MutationResp, error) {
	if owner == "" {
		return c.mutate(ctx, http.MethodPost,
			issuePath(projectID, number)+"/actions/unassign", actorBody(actor))
	}
	return c.mutate(ctx, http.MethodPost,
		issuePath(projectID, number)+"/actions/assign",
		map[string]string{"owner": owner, "actor": actor})
}

// AddLink creates a typed link from this issue to body.ToNumber.
func (c *Client) AddLink(
	ctx context.Context, projectID, number int64, body LinkBody, actor string,
) (*MutationResp, error) {
	return c.mutate(ctx, http.MethodPost, issuePath(projectID, number)+"/links",
		map[string]any{"type": body.Type, "to_number": body.ToNumber, "actor": actor})
}

// RemoveLink deletes a link by id. actor rides the query string per the
// DELETE-body portability convention.
func (c *Client) RemoveLink(
	ctx context.Context, projectID, number, linkID int64, actor string,
) (*MutationResp, error) {
	path := fmt.Sprintf("%s/links/%d?actor=%s",
		issuePath(projectID, number), linkID, url.QueryEscape(actor))
	return c.mutate(ctx, http.MethodDelete, path, nil)
}

// EditBody replaces issue.body via PATCH. v1 only supports body edits
// from the TUI; title edits would reuse the same endpoint.
func (c *Client) EditBody(
	ctx context.Context, projectID, number int64, body, actor string,
) (*MutationResp, error) {
	return c.mutate(ctx, http.MethodPatch, issuePath(projectID, number),
		map[string]any{"body": body, "actor": actor})
}

// ResolveProject runs the §4.2 resolution flow against startPath.
func (c *Client) ResolveProject(ctx context.Context, startPath string) (*ResolveResp, error) {
	var resp ResolveResp
	req := map[string]string{"start_path": startPath}
	if err := c.do(ctx, http.MethodPost, "/api/v1/projects/resolve", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListProjects returns the daemon's known projects.
func (c *Client) ListProjects(ctx context.Context) ([]ProjectSummary, error) {
	var resp struct {
		Projects []ProjectSummary `json:"projects"`
	}
	return resp.Projects, c.do(ctx, http.MethodGet, "/api/v1/projects", nil, &resp)
}

// ListComments and ListLinks route through GET /issues/{number} because
// the daemon embeds both slices there. ListEvents filters client-side
// because the poll endpoint accepts no issue_number filter.
func (c *Client) ListComments(
	ctx context.Context, projectID, number int64,
) ([]CommentEntry, error) {
	body, err := c.showIssue(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	return body.Comments, nil
}

// ListEvents returns the events tab data for one issue. See above note
// on the client-side filter.
func (c *Client) ListEvents(ctx context.Context, projectID, number int64) ([]EventLogEntry, error) {
	path := fmt.Sprintf("/api/v1/projects/%d/events?limit=200", projectID)
	var resp struct {
		Events []EventLogEntry `json:"events"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]EventLogEntry, 0, len(resp.Events))
	for _, e := range resp.Events {
		if e.IssueNumber != nil && *e.IssueNumber == number {
			out = append(out, e)
		}
	}
	return out, nil
}

// ListLinks returns the links tab data for one issue.
func (c *Client) ListLinks(ctx context.Context, projectID, number int64) ([]LinkEntry, error) {
	body, err := c.showIssue(ctx, projectID, number)
	if err != nil {
		return nil, err
	}
	return body.Links, nil
}

func (c *Client) showIssue(ctx context.Context, projectID, number int64) (*showIssueBody, error) {
	var resp showIssueBody
	if err := c.do(ctx, http.MethodGet, issuePath(projectID, number), nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func issuePath(projectID, number int64) string {
	return fmt.Sprintf("/api/v1/projects/%d/issues/%d", projectID, number)
}

func actorBody(actor string) map[string]string { return map[string]string{"actor": actor} }

// mutate is the shared shape of every mutation method: encode body,
// dispatch, decode the §4.5 envelope.
func (c *Client) mutate(ctx context.Context, method, path string, body any) (*MutationResp, error) {
	var resp MutationResp
	if err := c.do(ctx, method, path, body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	return c.doWithHeaders(ctx, method, path, body, out, nil)
}

func (c *Client) doWithHeaders(
	ctx context.Context, method, path string, body, out any, headers map[string]string,
) error {
	req, err := buildRequest(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.hc.Do(req) //nolint:gosec // G704: c.base built from our own daemon discovery
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return decodeError(resp, method, path)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func buildRequest(ctx context.Context, method, fullURL string, body any) (*http.Request, error) {
	if body == nil {
		return http.NewRequestWithContext(ctx, method, fullURL, nil)
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func decodeError(resp *http.Response, method, path string) error {
	var env struct {
		Status int `json:"status"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return &APIError{
		Method:  method,
		Path:    path,
		Status:  resp.StatusCode,
		Code:    env.Error.Code,
		Message: env.Error.Message,
		Hint:    env.Error.Hint,
	}
}
