package api

import (
	"net/http"

	"github.com/Skroby/mittens/ui/internal/channel"
	"github.com/Skroby/mittens/ui/internal/session"
	"github.com/Skroby/mittens/ui/internal/ws"
)

// Deps holds all dependencies needed by API handlers.
type Deps struct {
	Sessions   *session.Manager
	Hubs       *ws.HubManager
	Channel    *channel.Manager
	ChannelSSE *channel.SSEHandler
	MittensBin string
	StateDir   string
}

// Register registers all API routes on the given mux.
func Register(mux *http.ServeMux, deps *Deps) {
	sh := &SessionHandler{Sessions: deps.Sessions}
	wsh := &ws.Handler{Sessions: deps.Sessions, Hubs: deps.Hubs}
	ch := &ChannelHandler{Channel: deps.Channel}
	cfg := &ConfigHandler{MittensBin: deps.MittensBin}
	fs := &FSHandler{}
	presets := NewPresetHandler(deps.StateDir)

	// Session CRUD.
	mux.HandleFunc("GET /api/v1/sessions", sh.List)
	mux.HandleFunc("POST /api/v1/sessions", sh.Create)
	mux.HandleFunc("GET /api/v1/sessions/{id}", sh.Get)
	mux.HandleFunc("DELETE /api/v1/sessions/{id}", sh.Terminate)
	mux.HandleFunc("POST /api/v1/sessions/{id}/relaunch", sh.Relaunch)
	mux.HandleFunc("POST /api/v1/sessions/{id}/resize", sh.Resize)
	mux.HandleFunc("GET /api/v1/sessions/{id}/scrollback", sh.Scrollback)

	// WebSocket.
	mux.HandleFunc("/ws/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		wsh.ServeWS(w, r, id)
	})

	// Channel events (SSE) and response.
	mux.Handle("GET /api/v1/channel/events", deps.ChannelSSE)
	mux.HandleFunc("POST /api/v1/channel/{requestId}/respond", ch.Respond)

	// Config.
	mux.HandleFunc("GET /api/v1/config/extensions", cfg.ListExtensions)

	// Filesystem browsing.
	mux.HandleFunc("GET /api/v1/fs/browse", fs.Browse)

	// Presets.
	mux.HandleFunc("GET /api/v1/presets", presets.List)
	mux.HandleFunc("POST /api/v1/presets", presets.Save)
	mux.HandleFunc("DELETE /api/v1/presets/{name}", presets.Delete)
}
