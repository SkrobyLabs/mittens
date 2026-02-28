package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FSHandler handles filesystem browsing endpoints.
type FSHandler struct{}

// DirEntry is a single directory listing entry.
type DirEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

// BrowseResponse is the response for directory browsing.
type BrowseResponse struct {
	Path    string     `json:"path"`
	Parent  string     `json:"parent"`
	Entries []DirEntry `json:"entries"`
}

// Browse lists the contents of a directory.
func (h *FSHandler) Browse(w http.ResponseWriter, r *http.Request) {
	reqPath := r.URL.Query().Get("path")
	if reqPath == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			home = "/"
		}
		reqPath = home
	}

	// Clean and resolve the path.
	reqPath = filepath.Clean(reqPath)

	// Reject relative paths and path traversal.
	if !filepath.IsAbs(reqPath) || strings.Contains(reqPath, "..") {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}

	info, err := os.Stat(reqPath)
	if err != nil {
		writeError(w, http.StatusNotFound, "path not found")
		return
	}
	if !info.IsDir() {
		writeError(w, http.StatusBadRequest, "path is not a directory")
		return
	}

	entries, err := os.ReadDir(reqPath)
	if err != nil {
		writeError(w, http.StatusForbidden, "cannot read directory")
		return
	}

	var dirs []DirEntry
	for _, e := range entries {
		// Skip hidden files/directories.
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, DirEntry{
				Name:  e.Name(),
				Path:  filepath.Join(reqPath, e.Name()),
				IsDir: true,
			})
		}
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})

	parent := filepath.Dir(reqPath)
	if parent == reqPath {
		parent = "" // at root
	}

	writeJSON(w, http.StatusOK, BrowseResponse{
		Path:    reqPath,
		Parent:  parent,
		Entries: dirs,
	})
}
