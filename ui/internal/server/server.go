package server

import (
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Skroby/mittens/ui/internal/api"
	"github.com/Skroby/mittens/ui/internal/channel"
	"github.com/Skroby/mittens/ui/internal/session"
	"github.com/Skroby/mittens/ui/internal/ws"
)

// Config holds server configuration.
type Config struct {
	Port       int
	Dev        bool   // proxy to Vite dev server
	MittensBin string // path to mittens binary
	StateDir   string // ~/.mittens-ui
	Frontend   fs.FS  // embedded frontend assets
}

// Server is the main HTTP server for mittens-ui.
type Server struct {
	config     Config
	sessions   *session.Manager
	hubs       *ws.HubManager
	channelMgr *channel.Manager
	channelSSE *channel.SSEHandler
	httpServer *http.Server
}

// New creates a new server with all dependencies wired up.
func New(cfg Config) (*Server, error) {
	// Fail fast if tmux is not installed.
	if err := session.RequireTmux(); err != nil {
		return nil, err
	}

	store := session.NewStore(cfg.StateDir)
	hubs := ws.NewHubManager()

	channelDir := filepath.Join(cfg.StateDir, "channels")
	channelMgr := channel.NewManager(channelDir)
	channelSSE := channel.NewSSEHandler()

	// Wire channel events to SSE.
	channelMgr.OnRequest = func(req *channel.Request) {
		channelSSE.SendEvent(req)
	}

	mgr := session.NewManager(cfg.MittensBin, store)
	mgr.ChannelDir = channelDir

	// Hub factory: create a hub for each new session and set it.
	mgr.HubFactory = func(sessionID string) session.OutputHub {
		return hubs.GetOrCreate(sessionID)
	}

	// Recover previous sessions.
	mgr.Recover()

	return &Server{
		config:     cfg,
		sessions:   mgr,
		hubs:       hubs,
		channelMgr: channelMgr,
		channelSSE: channelSSE,
	}, nil
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Register API routes.
	api.Register(mux, &api.Deps{
		Sessions:   s.sessions,
		Hubs:       s.hubs,
		Channel:    s.channelMgr,
		ChannelSSE: s.channelSSE,
		MittensBin: s.config.MittensBin,
		StateDir:   s.config.StateDir,
	})

	// Serve frontend.
	if s.config.Dev {
		// Proxy to Vite dev server.
		viteURL, _ := url.Parse("http://localhost:5173")
		proxy := httputil.NewSingleHostReverseProxy(viteURL)
		mux.Handle("/", proxy)
		log.Println("Dev mode: proxying frontend to http://localhost:5173")
	} else if s.config.Frontend != nil {
		// Serve embedded frontend with SPA fallback.
		frontendFS, err := fs.Sub(s.config.Frontend, "frontend/dist")
		if err != nil {
			return fmt.Errorf("embedded frontend: %w", err)
		}
		fileServer := http.FileServer(http.FS(frontendFS))
		mux.Handle("/", spaHandler(fileServer, frontendFS))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body><h1>mittens-ui</h1><p>No frontend built. Run with --dev or build the frontend first.</p></body></html>")
		})
	}

	// Apply middleware.
	var handler http.Handler = mux
	handler = CORS(handler)
	handler = Logger(handler)
	handler = Recovery(handler)

	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.config.Port),
		Handler: handler,
	}

	log.Printf("mittens-ui listening on http://localhost:%d", s.config.Port)
	return s.httpServer.ListenAndServe()
}

// spaHandler serves static files, falling back to index.html for SPA routes.
func spaHandler(fileServer http.Handler, fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else {
			path = path[1:] // strip leading /
		}

		// Check if file exists.
		if _, err := fs.Stat(fsys, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		// SPA fallback: serve index.html for non-API routes.
		if len(path) < 4 || path[:4] != "api/" {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}

		http.NotFound(w, r)
	})
}

// FindMittensBin locates the mittens binary, returning an absolute path.
func FindMittensBin() string {
	// Check next to our binary.
	exe, err := os.Executable()
	if err == nil {
		// Resolve symlinks so relative joins are accurate.
		exe, _ = filepath.EvalSymlinks(exe)
		dir := filepath.Dir(exe)
		for _, rel := range []string{filepath.Join("..", "mittens"), "mittens"} {
			candidate := filepath.Join(dir, rel)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				if abs, err := filepath.Abs(candidate); err == nil {
					return abs
				}
				return candidate
			}
		}
	}

	// Check PATH.
	if path, err := exec.LookPath("mittens"); err == nil {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
		return path
	}

	return "mittens"
}
