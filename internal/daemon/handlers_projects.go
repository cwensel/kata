package daemon

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/db"
)

// registerProjectsHandlers installs project-scoped routes (resolve, init, list,
// show) on humaAPI. Resolution and init semantics live entirely on the daemon
// per spec §2.4 so all clients (CLI, TUI, future) see identical behavior.
func registerProjectsHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "resolveProject",
		Method:      "POST",
		Path:        "/api/v1/projects/resolve",
	}, func(ctx context.Context, in *api.ResolveProjectRequest) (*api.ResolveProjectResponse, error) {
		out, err := resolveProject(ctx, cfg.DB, in.Body.StartPath)
		if err != nil {
			return nil, err
		}
		return &api.ResolveProjectResponse{Body: *out}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "initProject",
		Method:      "POST",
		Path:        "/api/v1/projects",
	}, func(ctx context.Context, in *api.InitProjectRequest) (*api.InitProjectResponse, error) {
		out, created, err := initProject(ctx, cfg.DB, in)
		if err != nil {
			return nil, err
		}
		resp := &api.InitProjectResponse{}
		resp.Body.ProjectResolveBody = *out
		resp.Body.Created = created
		return resp, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "listProjects",
		Method:      "GET",
		Path:        "/api/v1/projects",
	}, func(ctx context.Context, _ *struct{}) (*api.ListProjectsResponse, error) {
		ps, err := cfg.DB.ListProjects(ctx)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ListProjectsResponse{}
		out.Body.Projects = ps
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "showProject",
		Method:      "GET",
		Path:        "/api/v1/projects/{project_id}",
	}, func(ctx context.Context, in *struct {
		ProjectID int64 `path:"project_id"`
	}) (*api.ShowProjectResponse, error) {
		p, err := cfg.DB.ProjectByID(ctx, in.ProjectID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, api.NewError(404, "project_not_found", "project not found", "", nil)
		}
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		aliases, err := cfg.DB.ProjectAliases(ctx, p.ID)
		if err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		out := &api.ShowProjectResponse{}
		out.Body.Project = p
		out.Body.Aliases = aliases
		return out, nil
	})
}

// resolveProject implements the strict resolution flow per spec §2.4. Order:
// .kata.toml binding wins; then alias lookup from the git root; else fail.
func resolveProject(ctx context.Context, store *db.DB, startPath string) (*api.ProjectResolveBody, error) {
	if startPath == "" {
		return nil, api.NewError(400, "validation", "start_path required", "", nil)
	}
	abs, err := filepath.Abs(startPath)
	if err != nil {
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	disc, err := config.DiscoverPaths(abs)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}

	if body, ok, err := resolveByKataToml(ctx, store, disc); err != nil {
		return nil, err
	} else if ok {
		return body, nil
	}

	if disc.GitRoot != "" {
		return resolveByAlias(ctx, store, disc)
	}

	return nil, api.NewError(404, "project_not_initialized",
		"no .kata.toml ancestor and no git ancestor",
		`run "kata init" inside a workspace`, nil)
}

// resolveByKataToml returns (body, true, nil) when a .kata.toml binding
// exists at the workspace root and resolves to a project. Returns
// (nil, false, nil) when there is no .kata.toml. Surfaces parse errors.
func resolveByKataToml(ctx context.Context, store *db.DB, disc config.DiscoveredPaths) (*api.ProjectResolveBody, bool, error) {
	if disc.WorkspaceRoot == "" {
		return nil, false, nil
	}
	cfgFile, err := config.ReadProjectConfig(disc.WorkspaceRoot)
	if err != nil {
		if errors.Is(err, config.ErrProjectConfigMissing) {
			return nil, false, nil
		}
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}
	project, err := store.ProjectByIdentity(ctx, cfgFile.Project.Identity)
	if errors.Is(err, db.ErrNotFound) {
		return nil, false, api.NewError(404, "project_not_initialized",
			"project "+cfgFile.Project.Identity+" is bound by .kata.toml but not registered",
			`run "kata init" in this workspace`, nil)
	}
	if err != nil {
		return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	alias, err := upsertAliasFor(ctx, store, project.ID, disc, false)
	if err != nil {
		return nil, false, err
	}
	return &api.ProjectResolveBody{
		Project:       project,
		Alias:         alias,
		WorkspaceRoot: disc.WorkspaceRoot,
	}, true, nil
}

// resolveByAlias looks up the alias derived from the git root and returns
// the bound project. Caller guarantees disc.GitRoot != "".
func resolveByAlias(ctx context.Context, store *db.DB, disc config.DiscoveredPaths) (*api.ProjectResolveBody, error) {
	info, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	alias, err := store.AliasByIdentity(ctx, info.Identity)
	if errors.Is(err, db.ErrNotFound) {
		return nil, api.NewError(404, "project_not_initialized",
			"no kata project is attached to this workspace",
			`run "kata init" in this workspace`, nil)
	}
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	_ = store.TouchAlias(ctx, alias.ID, info.RootPath)
	project, err := store.ProjectByID(ctx, alias.ProjectID)
	if err != nil {
		return nil, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return &api.ProjectResolveBody{Project: project, Alias: alias, WorkspaceRoot: ""}, nil
}

// initProject implements `kata init` on the daemon side per spec §2.4.
func initProject(ctx context.Context, store *db.DB, req *api.InitProjectRequest) (*api.ProjectResolveBody, bool, error) {
	if req.Body.StartPath == "" {
		return nil, false, api.NewError(400, "validation", "start_path required", "", nil)
	}
	abs, err := filepath.Abs(req.Body.StartPath)
	if err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}
	disc, err := config.DiscoverPaths(abs)
	if err != nil {
		return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
	}

	tomlCfg, err := readWorkspaceConfig(disc)
	if err != nil {
		return nil, false, err
	}

	identity, name, err := pickInitIdentity(req, disc, tomlCfg)
	if err != nil {
		return nil, false, err
	}
	if err := config.ValidateIdentity(identity); err != nil {
		return nil, false, api.NewError(400, "validation", err.Error(), "", nil)
	}

	// When --project was supplied outside any git/workspace ancestor, synthesize
	// a local alias rooted at the start path so upsertAliasFor has something to
	// attach. This is the explicit escape hatch documented in spec §2.4.
	if disc.GitRoot == "" && disc.WorkspaceRoot == "" {
		disc.WorkspaceRoot = abs
	}

	project, created, err := upsertProject(ctx, store, identity, name)
	if err != nil {
		return nil, false, err
	}

	alias, err := upsertAliasFor(ctx, store, project.ID, disc, req.Body.Reassign)
	if err != nil {
		return nil, false, err
	}

	dest := writeDestination(disc, abs)
	if tomlCfg == nil || tomlCfg.Project.Identity != identity {
		if err := config.WriteProjectConfig(dest, identity, name); err != nil {
			return nil, false, api.NewError(500, "internal", err.Error(), "", nil)
		}
	}

	return &api.ProjectResolveBody{
		Project:       project,
		Alias:         alias,
		WorkspaceRoot: dest,
	}, created, nil
}

// readWorkspaceConfig reads .kata.toml only when a workspace root was actually
// discovered; passing "" to ReadProjectConfig would resolve to the daemon's
// cwd. Parse errors surface as 400; "missing" returns nil.
func readWorkspaceConfig(disc config.DiscoveredPaths) (*config.ProjectConfig, error) {
	if disc.WorkspaceRoot == "" {
		return nil, nil
	}
	cfgFile, err := config.ReadProjectConfig(disc.WorkspaceRoot)
	if err != nil {
		if errors.Is(err, config.ErrProjectConfigMissing) {
			return nil, nil
		}
		return nil, api.NewError(400, "validation", err.Error(), "", nil)
	}
	return cfgFile, nil
}

// pickInitIdentity decides the (identity, name) pair for kata init based on
// flags, .kata.toml content, and the discovered git workspace.
func pickInitIdentity(req *api.InitProjectRequest, disc config.DiscoveredPaths, tomlCfg *config.ProjectConfig) (string, string, error) {
	switch {
	case tomlCfg != nil && req.Body.ProjectIdentity != "" && tomlCfg.Project.Identity != req.Body.ProjectIdentity:
		if !req.Body.Replace {
			return "", "", api.NewError(http.StatusConflict, "project_binding_conflict",
				".kata.toml declares a different identity",
				"pass replace=true to overwrite", nil)
		}
		identity := req.Body.ProjectIdentity
		return identity, pickName(req.Body.Name, identity), nil
	case tomlCfg != nil:
		identity := tomlCfg.Project.Identity
		name := pickName(req.Body.Name, tomlCfg.Project.Name)
		if name == "" {
			name = pickName("", identity)
		}
		return identity, name, nil
	case req.Body.ProjectIdentity != "":
		identity := req.Body.ProjectIdentity
		return identity, pickName(req.Body.Name, identity), nil
	default:
		if disc.GitRoot == "" {
			return "", "", api.NewError(400, "validation",
				"cannot derive project identity outside a git workspace",
				`pass project_identity or run inside a git repo`, nil)
		}
		info, err := config.ComputeAliasIdentity(disc)
		if err != nil {
			return "", "", api.NewError(400, "validation", err.Error(), "", nil)
		}
		identity := info.Identity
		return identity, pickName(req.Body.Name, identity), nil
	}
}

// writeDestination chooses where to write .kata.toml: workspace root if any,
// else git root, else the absolute start path.
func writeDestination(disc config.DiscoveredPaths, abs string) string {
	if disc.WorkspaceRoot != "" {
		return disc.WorkspaceRoot
	}
	if disc.GitRoot != "" {
		return disc.GitRoot
	}
	return abs
}

// upsertProject returns the existing project (created=false) when one matches
// the identity, otherwise creates a new project (created=true).
func upsertProject(ctx context.Context, store *db.DB, identity, name string) (db.Project, bool, error) {
	got, err := store.ProjectByIdentity(ctx, identity)
	if err == nil {
		return got, false, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.Project{}, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	created, err := store.CreateProject(ctx, identity, name)
	if err != nil {
		return db.Project{}, false, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return created, true, nil
}

// upsertAliasFor attaches the discovered alias to projectID. If the alias is
// already attached to a *different* project, returns project_alias_conflict
// (409) unless reassign is true (in which case we move it).
func upsertAliasFor(ctx context.Context, store *db.DB, projectID int64, disc config.DiscoveredPaths, reassign bool) (db.ProjectAlias, error) {
	info, err := config.ComputeAliasIdentity(disc)
	if err != nil {
		return db.ProjectAlias{}, api.NewError(400, "validation", err.Error(), "", nil)
	}
	existing, err := store.AliasByIdentity(ctx, info.Identity)
	if err == nil {
		if existing.ProjectID == projectID {
			_ = store.TouchAlias(ctx, existing.ID, info.RootPath)
			refreshed, _ := store.AliasByIdentity(ctx, info.Identity)
			return refreshed, nil
		}
		if !reassign {
			return db.ProjectAlias{}, api.NewError(http.StatusConflict, "project_alias_conflict",
				"alias already attached to a different project",
				"pass reassign=true to move it", map[string]any{
					"alias_identity":      info.Identity,
					"existing_project_id": existing.ProjectID,
				})
		}
		if _, execErr := store.ExecContext(ctx,
			`UPDATE project_aliases
			 SET project_id = ?, root_path = ?, last_seen_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
			 WHERE id = ?`,
			projectID, info.RootPath, existing.ID); execErr != nil {
			return db.ProjectAlias{}, api.NewError(500, "internal", execErr.Error(), "", nil)
		}
		refreshed, _ := store.AliasByIdentity(ctx, info.Identity)
		return refreshed, nil
	}
	if !errors.Is(err, db.ErrNotFound) {
		return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	a, err := store.AttachAlias(ctx, projectID, info.Identity, info.Kind, info.RootPath)
	if err != nil {
		return db.ProjectAlias{}, api.NewError(500, "internal", err.Error(), "", nil)
	}
	return a, nil
}

// pickName returns explicit if non-empty, otherwise the last `/` or `:`-separated
// segment of identity (so "github.com/wesm/kata" → "kata").
func pickName(explicit, identity string) string {
	if explicit != "" {
		return explicit
	}
	for i := len(identity) - 1; i >= 0; i-- {
		if identity[i] == '/' || identity[i] == ':' {
			return identity[i+1:]
		}
	}
	return identity
}
