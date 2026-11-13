package web

import (
	"net/http"
	"strings"

	"git.cluster.aimingupward.com/steve/agent-project-controller/internal/hubadmin"
)

// Hub is the console's phone/hub mint path. It never exposes pairing URLs in
// device listings and requires an OIDC operator session for every mutation.
type Hub struct {
	Backend  hubadmin.Backend
	Endpoint string
}

func (s *Server) hubEnabled() bool {
	return s.Hub != nil && s.Hub.Backend != nil && strings.TrimSpace(s.Hub.Endpoint) != ""
}

func (s *Server) requireHubOIDC(w http.ResponseWriter, r *http.Request) bool {
	if !s.hubEnabled() {
		http.Error(w, "hub pairing is not configured", http.StatusNotFound)
		return false
	}
	actor, _ := r.Context().Value(actorKey{}).(string)
	if s.OIDC == nil || actor == "" {
		http.Error(w, "hub pairing requires an OIDC operator session", http.StatusForbidden)
		return false
	}
	return true
}

func hubNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func (s *Server) mintHubPairing(w http.ResponseWriter, r *http.Request) {
	if !s.requireHubOIDC(w, r) {
		return
	}
	hubNoStore(w)
	offer, err := s.Hub.Backend.Pair(r.Context())
	if err != nil {
		http.Error(w, hubadmin.ErrUnavailable.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, offer)
}

func (s *Server) listHubDevices(w http.ResponseWriter, r *http.Request) {
	if !s.requireHubOIDC(w, r) {
		return
	}
	hubNoStore(w)
	devices, err := s.Hub.Backend.Devices(r.Context())
	if err != nil {
		http.Error(w, hubadmin.ErrUnavailable.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, devices)
}

func (s *Server) revokeHubDevice(w http.ResponseWriter, r *http.Request) {
	if !s.requireHubOIDC(w, r) {
		return
	}
	hubNoStore(w)
	id := r.PathValue("id")
	ok, err := s.Hub.Backend.Revoke(r.Context(), id)
	if err != nil || !ok {
		http.Error(w, "device revocation unavailable", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]bool{"revoked": true})
}
