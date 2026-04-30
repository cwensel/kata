package daemon

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// registerEvents wires the polling endpoints (Huma) and the SSE endpoint
// (raw mux). Both are implemented incrementally across Plan 4 tasks: Task 5
// adds polling, Task 6 adds the SSE handshake/drain, Task 7 adds the live
// phase. This stub keeps server.go building before any of those land.
func registerEvents(humaAPI huma.API, mux *http.ServeMux, cfg ServerConfig) {
	_ = humaAPI
	_ = mux
	_ = cfg
}
