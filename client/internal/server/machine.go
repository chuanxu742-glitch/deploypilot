package server

import (
	"net/http"
	"os"
	"os/user"
	"runtime"
)

// handleMachine reports this client's host and process identity after pairing.
// The working directory is the initial directory inherited by AI commands,
// not the independent interactive terminal's current directory.
func (s *Server) handleMachine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()
	username := ""
	if current, err := user.Current(); err == nil {
		username = current.Username
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"hostname": hostname,
		"platform": runtime.GOOS + "/" + runtime.GOARCH,
		"username": username,
		"cwd":      cwd,
		"version":  s.version,
	})
}
