package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type projectAliasRef struct {
	ID            int64  `json:"id"`
	ProjectID     int64  `json:"project_id"`
	AliasIdentity string `json:"alias_identity"`
	AliasKind     string `json:"alias_kind"`
	RootPath      string `json:"root_path"`
}

type projectRef struct {
	ID              int64             `json:"id"`
	Identity        string            `json:"identity"`
	Name            string            `json:"name"`
	NextIssueNumber int64             `json:"next_issue_number"`
	Aliases         []projectAliasRef `json:"aliases,omitempty"`
}

func newProjectsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "projects", Short: "list and inspect kata projects"}
	cmd.AddCommand(projectsListCmd(), projectsShowCmd(), projectsRenameCmd(), projectsMergeCmd())
	return cmd
}

func projectsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list known projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet, baseURL+"/api/v1/projects", nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			var b struct {
				Projects []struct {
					ID              int64  `json:"id"`
					Identity        string `json:"identity"`
					Name            string `json:"name"`
					NextIssueNumber int64  `json:"next_issue_number"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			for _, p := range b.Projects {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%d  %s  (%s, next #%d)\n",
					p.ID, p.Identity, p.Name, p.NextIssueNumber); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func projectsRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <project> <name>",
		Short: "rename a project",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(strings.Join(args[1:], " "))
			if name == "" {
				return &cliError{Message: "project name must be non-empty", Kind: kindValidation, ExitCode: ExitValidation}
			}

			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			project, err := resolveProjectSelector(ctx, client, baseURL, args[0])
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPatch,
				fmt.Sprintf("%s/api/v1/projects/%d", baseURL, project.ID),
				map[string]string{"name": name})
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			var b struct {
				Project struct {
					ID   int64  `json:"id"`
					Name string `json:"name"`
				} `json:"project"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "renamed project #%d to %s\n", b.Project.ID, b.Project.Name)
			return err
		},
	}
}

func projectsMergeCmd() *cobra.Command {
	var targetName string
	cmd := &cobra.Command{
		Use:   "merge <source> <target>",
		Short: "merge one project into a surviving project",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			source, err := resolveProjectSelector(ctx, client, baseURL, args[0])
			if err != nil {
				return err
			}
			target, err := resolveProjectSelector(ctx, client, baseURL, args[1])
			if err != nil {
				return err
			}
			if source.ID == target.ID {
				return &cliError{
					Message:  "source and target project must be different",
					Kind:     kindValidation,
					ExitCode: ExitValidation,
				}
			}
			body := map[string]any{"source_project_id": source.ID}
			if strings.TrimSpace(targetName) != "" {
				body["target_name"] = strings.TrimSpace(targetName)
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodPost,
				fmt.Sprintf("%s/api/v1/projects/%d/merge", baseURL, target.ID), body)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			var b struct {
				Source       projectRef `json:"source"`
				Target       projectRef `json:"target"`
				IssuesMoved  int64      `json:"issues_moved"`
				AliasesMoved int64      `json:"aliases_moved"`
				EventsMoved  int64      `json:"events_moved"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"merged project #%d into #%d (%s); moved %s, %s, %s; next #%d\n",
				b.Source.ID, b.Target.ID, b.Target.Name,
				pluralCount(b.IssuesMoved, "issue"),
				pluralCount(b.AliasesMoved, "alias"),
				pluralCount(b.EventsMoved, "event"),
				b.Target.NextIssueNumber)
			return err
		},
	}
	cmd.Flags().StringVar(&targetName, "rename-target", "", "rename the surviving target project after merge")
	return cmd
}

func projectsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <project>",
		Short: "show project details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			baseURL, err := ensureDaemon(ctx)
			if err != nil {
				return err
			}
			client, err := httpClientFor(ctx, baseURL)
			if err != nil {
				return err
			}
			project, err := resolveProjectSelector(ctx, client, baseURL, args[0])
			if err != nil {
				return err
			}
			status, bs, err := httpDoJSON(ctx, client, http.MethodGet,
				fmt.Sprintf("%s/api/v1/projects/%d", baseURL, project.ID), nil)
			if err != nil {
				return err
			}
			if status >= 400 {
				return apiErrFromBody(status, bs)
			}
			if flags.JSON {
				var buf bytes.Buffer
				if err := emitJSON(&buf, json.RawMessage(bs)); err != nil {
					return err
				}
				_, err := fmt.Fprint(cmd.OutOrStdout(), buf.String())
				return err
			}
			var b struct {
				Project struct {
					ID              int64  `json:"id"`
					Identity        string `json:"identity"`
					Name            string `json:"name"`
					NextIssueNumber int64  `json:"next_issue_number"`
				} `json:"project"`
			}
			if err := json.Unmarshal(bs, &b); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "#%d %s (%s, next #%d)\n",
				b.Project.ID, b.Project.Identity, b.Project.Name, b.Project.NextIssueNumber)
			return err
		},
	}
}

func resolveProjectSelector(ctx context.Context, client *http.Client, baseURL, selector string) (projectRef, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return projectRef{}, &cliError{Message: "project selector must be non-empty", Kind: kindValidation, ExitCode: ExitValidation}
	}
	projects, err := loadProjectRefs(ctx, client, baseURL)
	if err != nil {
		return projectRef{}, err
	}
	if id, parseErr := strconv.ParseInt(selector, 10, 64); parseErr == nil {
		for _, project := range projects {
			if project.ID == id {
				return project, nil
			}
		}
		return projectRef{}, &cliError{
			Message:  fmt.Sprintf("project #%d not found", id),
			Kind:     kindNotFound,
			ExitCode: ExitNotFound,
		}
	}
	// Prefer stable identity/alias matches over display-name matches. This
	// keeps `projects merge kenn steward` usable even if a failed repair left
	// both rows with the same display name.
	for _, matcher := range []projectSelectorMatcher{
		projectIdentityExact,
		projectAliasExact,
		projectIdentitySuffix,
		projectAliasSuffix,
		projectNameExact,
		projectNameSuffix,
	} {
		if match, ok, err := uniqueProjectMatch(selector, projects, matcher); ok || err != nil {
			return match, err
		}
	}
	return projectRef{}, &cliError{
		Message:  fmt.Sprintf("project selector %q did not match any project", selector),
		Kind:     kindNotFound,
		ExitCode: ExitNotFound,
	}
}

func loadProjectRefs(ctx context.Context, client *http.Client, baseURL string) ([]projectRef, error) {
	status, bs, err := httpDoJSON(ctx, client, http.MethodGet, baseURL+"/api/v1/projects", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, apiErrFromBody(status, bs)
	}
	var list struct {
		Projects []projectRef `json:"projects"`
	}
	if err := json.Unmarshal(bs, &list); err != nil {
		return nil, err
	}
	for i := range list.Projects {
		status, detail, err := httpDoJSON(ctx, client, http.MethodGet,
			fmt.Sprintf("%s/api/v1/projects/%d", baseURL, list.Projects[i].ID), nil)
		if err != nil {
			return nil, err
		}
		if status >= 400 {
			return nil, apiErrFromBody(status, detail)
		}
		var show struct {
			Aliases []projectAliasRef `json:"aliases"`
		}
		if err := json.Unmarshal(detail, &show); err != nil {
			return nil, err
		}
		list.Projects[i].Aliases = show.Aliases
	}
	return list.Projects, nil
}

type projectSelectorMatcher func(projectRef, string) bool

func uniqueProjectMatch(selector string, projects []projectRef, matchesSelector projectSelectorMatcher) (projectRef, bool, error) {
	matches := make([]projectRef, 0, 1)
	seen := make(map[int64]bool)
	for _, project := range projects {
		if matchesSelector(project, selector) && !seen[project.ID] {
			seen[project.ID] = true
			matches = append(matches, project)
		}
	}
	switch len(matches) {
	case 0:
		return projectRef{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return projectRef{}, false, &cliError{
			Message:  ambiguousProjectSelectorMessage(selector, matches),
			Kind:     kindValidation,
			ExitCode: ExitValidation,
		}
	}
}

func projectIdentityExact(project projectRef, selector string) bool {
	return project.Identity == selector
}

func projectAliasExact(project projectRef, selector string) bool {
	for _, alias := range project.Aliases {
		if alias.AliasIdentity == selector {
			return true
		}
	}
	return false
}

func projectIdentitySuffix(project projectRef, selector string) bool {
	return identitySuffixMatches(project.Identity, selector)
}

func projectAliasSuffix(project projectRef, selector string) bool {
	for _, alias := range project.Aliases {
		if identitySuffixMatches(alias.AliasIdentity, selector) {
			return true
		}
	}
	return false
}

func projectNameExact(project projectRef, selector string) bool {
	return project.Name == selector
}

func projectNameSuffix(project projectRef, selector string) bool {
	return identitySuffixMatches(project.Name, selector)
}

func identitySuffixMatches(identity, selector string) bool {
	return identity == selector ||
		strings.HasSuffix(identity, "/"+selector) ||
		strings.HasSuffix(identity, ":"+selector)
}

func ambiguousProjectSelectorMessage(selector string, matches []projectRef) string {
	parts := make([]string, 0, len(matches))
	for _, match := range matches {
		parts = append(parts, fmt.Sprintf("#%d %s (%s)", match.ID, match.Identity, match.Name))
	}
	return fmt.Sprintf("project selector %q is ambiguous: %s", selector, strings.Join(parts, ", "))
}

func pluralCount(n int64, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
