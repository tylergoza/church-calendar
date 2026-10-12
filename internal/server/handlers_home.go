package server

import "net/http"

// handleHome is the front page. The month grid and agenda arrive with the
// public calendar (PLAN.md step 3); until then it says what's here.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "home", map[string]any{"PublicHeader": currentUser(r) == nil})
}
