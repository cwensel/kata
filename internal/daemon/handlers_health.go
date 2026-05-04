package daemon

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
	"github.com/wesm/kata/internal/version"
)

// registerHealthHandlers installs /api/v1/ping and /api/v1/health on humaAPI.
// /ping is the cheap liveness probe (no DB touch). /health reads
// meta.schema_version and reports uptime relative to cfg.StartedAt.
func registerHealthHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "ping",
		Method:      "GET",
		Path:        "/api/v1/ping",
	}, func(ctx context.Context, _ *struct{}) (*api.PingResponse, error) {
		_ = ctx
		out := &api.PingResponse{}
		out.Body.OK = true
		out.Body.Service = "kata"
		out.Body.Version = version.Version
		out.Body.PID = os.Getpid()
		return out, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "health",
		Method:      "GET",
		Path:        "/api/v1/health",
	}, func(ctx context.Context, _ *struct{}) (*api.HealthResponse, error) {
		var v string
		if err := cfg.DB.QueryRowContext(ctx,
			`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
			return nil, api.NewError(500, "internal", err.Error(), "", nil)
		}
		schema, err := strconv.Atoi(v)
		if err != nil {
			return nil, api.NewError(500, "internal", "invalid schema_version: "+v, "", nil)
		}
		out := &api.HealthResponse{}
		out.Body.OK = true
		out.Body.DBPath = cfg.DB.Path()
		out.Body.SchemaVersion = schema
		out.Body.Version = version.Version
		out.Body.StartedAt = cfg.StartedAt
		out.Body.Uptime = time.Since(cfg.StartedAt).Round(time.Second).String()
		return out, nil
	})
}
