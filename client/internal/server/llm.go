package server

import (
	"encoding/json"
	"net/http"
)

// handleLLMStatus reports whether a key/model are configured (never the key).
func (s *Server) handleLLMStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"hasKey": s.llm.HasKey(),
		"model":  s.llm.Model(),
	})
}

// handleLLMConfig sets the in-memory API key and/or model. The key is held in
// memory only and is never persisted or logged.
func (s *Server) handleLLMConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		APIKey *string `json:"apiKey"`
		Model  *string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.APIKey != nil {
		s.llm.SetAPIKey(*req.APIKey)
	}
	if req.Model != nil {
		s.llm.SetModel(*req.Model)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hasKey": s.llm.HasKey(),
		"model":  s.llm.Model(),
	})
}

// handleLLMModels lists available models from the provider.
func (s *Server) handleLLMModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.llm.ListModels(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": s.llm.Redact(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
