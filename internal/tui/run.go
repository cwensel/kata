// Package tui implements the kata terminal UI built on Bubble Tea.
package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/wesm/kata/internal/daemonclient"
)

// Options controls TUI behavior. Stable across versions; new fields
// must be optional.
type Options struct {
	AllProjects    bool
	IncludeDeleted bool
	Stdout         io.Writer // typically os.Stdout
	Stderr         io.Writer // typically os.Stderr
}

// Run starts the TUI. Blocks until the user quits or ctx is cancelled.
// Returns nil on clean exit. Returns errNotATTY when stdin/stdout are
// not a terminal so callers can print a friendly message.
func Run(ctx context.Context, opts Options) error {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return errNotATTY
	}
	c, sc, err := bootClient(ctx, opts)
	if err != nil {
		return err
	}
	m := initialModel(opts)
	m.api = c
	m.scope = sc
	if sc.empty {
		m.view = viewEmpty
	}
	progOpts := []tea.ProgramOption{
		tea.WithContext(ctx),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(), // future-proof; ignored by current handlers
	}
	if opts.Stdout != nil {
		progOpts = append(progOpts, tea.WithOutput(opts.Stdout))
	}
	if _, err := tea.NewProgram(m, progOpts...).Run(); err != nil {
		return err
	}
	return nil
}

// bootClient discovers the daemon, constructs the typed HTTP client, and
// resolves the initial scope. Splitting this off Run keeps Run's
// cyclomatic complexity inside the project's ≤8 hard limit and isolates
// the network preflight from the Bubble Tea wiring.
func bootClient(ctx context.Context, opts Options) (*Client, scope, error) {
	endpoint, err := daemonclient.EnsureRunning(ctx)
	if err != nil {
		return nil, scope{}, err
	}
	hc, err := daemonclient.NewHTTPClient(ctx, endpoint,
		daemonclient.Opts{Timeout: 5 * time.Second})
	if err != nil {
		return nil, scope{}, err
	}
	c := NewClient(endpoint, hc)
	cwd, _ := os.Getwd()
	sc, err := bootResolveScope(ctx, c, opts.AllProjects, cwd)
	if err != nil {
		return nil, scope{}, err
	}
	return c, sc, nil
}

// scope describes the issue-set the TUI is browsing. Exactly one of
// projectID, allProjects, empty is set.
type scope struct {
	projectID   int64
	allProjects bool
	empty       bool
	projectName string
	workspace   string
}

// bootResolveScope implements §7.2 of the master spec. Order:
//  1. --all-projects → cross-project mode.
//  2. POST /projects/resolve(cwd) success → single-project mode.
//  3. project_not_initialized + ≥1 registered project → fall back to
//     all-projects so the user has something to look at.
//  4. project_not_initialized + zero registered projects → empty state.
//  5. Any other resolve error → propagate so Run fails loudly.
func bootResolveScope(
	ctx context.Context, c *Client, allProjects bool, cwd string,
) (scope, error) {
	if allProjects {
		return scope{allProjects: true}, nil
	}
	rr, err := c.ResolveProject(ctx, cwd)
	if err == nil {
		return scope{
			projectID:   rr.Project.ID,
			projectName: rr.Project.Name,
			workspace:   rr.WorkspaceRoot,
		}, nil
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "project_not_initialized" {
		return scope{}, err
	}
	projects, listErr := c.ListProjects(ctx)
	if listErr != nil {
		return scope{}, listErr
	}
	if len(projects) == 0 {
		return scope{empty: true}, nil
	}
	return scope{allProjects: true}, nil
}

// errNotATTY indicates the TUI was launched outside a terminal.
var errNotATTY = errors.New("kata tui requires a terminal (stdin/stdout must be a tty)")

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
