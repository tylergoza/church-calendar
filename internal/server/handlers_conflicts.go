package server

import (
	"errors"
	"net/http"

	"github.com/tylergoza/church-calendar/internal/store"
)

// conflictGroup is one room's clashes on the conflicts page.
type conflictGroup struct {
	Room  string
	Items []store.Conflict
}

// handleConflicts lists clashes from today on, grouped by room. Pairs
// marked "that's fine" show only with ?all=1.
func (s *Server) handleConflicts(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	list, err := s.store.Conflicts(s.today(), all)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var groups []conflictGroup
	for _, c := range list {
		if n := len(groups); n == 0 || groups[n-1].Room != c.Room.Name {
			groups = append(groups, conflictGroup{Room: c.Room.Name})
		}
		groups[len(groups)-1].Items = append(groups[len(groups)-1].Items, c)
	}
	s.render(w, r, http.StatusOK, "conflicts", map[string]any{
		"Title": "Conflicts", "Groups": groups, "Count": len(list), "All": all,
	})
}

// handleConflictOK marks a pair "that's fine" (ok=1) or takes the mark off.
func (s *Server) handleConflictOK(w http.ResponseWriter, r *http.Request) {
	a, b, ok := formInt(r, "a"), formInt(r, "b"), formStr(r, "ok") == "1"
	err := s.store.SetConflictOK(a, b, ok, currentUser(r).ID)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.render(w, r, http.StatusBadRequest, "error", map[string]any{"Title": "Can't do that", "Message": "Pick two different bookings."})
		return
	}
	msg := "Marked as fine. It won't show as a conflict again."
	if !ok {
		msg = "Showing it as a conflict again."
	}
	s.redirect(w, r, safeRedirect(formStr(r, "next")), msg)
}
