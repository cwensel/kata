package daemon

import (
	"context"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/kata/internal/api"
)

// registerInstanceHandlers installs /api/v1/instance — the local kata
// installation's stable identifier. The value is set by db.Open at first init
// and never changes; this endpoint surfaces it for future federation spoke
// discovery.
func registerInstanceHandlers(humaAPI huma.API, cfg ServerConfig) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "instance",
		Method:      "GET",
		Path:        "/api/v1/instance",
	}, func(_ context.Context, _ *struct{}) (*api.InstanceResponse, error) {
		uid := cfg.DB.InstanceUID()
		if uid == "" {
			return nil, api.NewError(503, "instance_uid_unset",
				"meta.instance_uid not yet set", "", nil)
		}
		out := &api.InstanceResponse{}
		out.Body.InstanceUID = uid
		return out, nil
	})
}
