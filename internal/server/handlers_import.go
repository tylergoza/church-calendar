package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
	"github.com/tylergoza/church-calendar/internal/tracker"
)

// Import rooms from the maintenance tracker ----------------------------------

// tracker is a client for the tracker connection as it is now. It isn't
// Configured when the connection is off.
func (s *Server) tracker() *tracker.Client {
	c := s.connection("tracker")
	return tracker.New(c.Get("tracker_url"), c.Get("tracker_token"))
}

// importPlace is one bookable place on the import page.
type importPlace struct {
	tracker.Place
	Label  string
	Room   *store.Room // the room that follows it, or the hand-made one it will take over
	Taken  bool        // its name belongs to a room that follows another place
	Exists bool        // already imported
}

// trackerPlaces asks the tracker for every place. All of them are needed
// (not only the bookable kinds) to tell a deleted place from one that's
// now a floor.
func (s *Server) trackerPlaces(ctx context.Context) ([]tracker.Place, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.tracker().Places(ctx)
}

func (s *Server) handleRoomImport(w http.ResponseWriter, r *http.Request) {
	if !s.tracker().Configured() {
		s.renderRoomImport(w, r, nil, nil, tracker.ErrNotConfigured)
		return
	}
	places, err := s.trackerPlaces(r.Context())
	s.renderRoomImport(w, r, nil, places, err)
}

// renderRoomImport shows the tracker's bookable places next to the rooms
// here, and result when an import just ran.
func (s *Server) renderRoomImport(w http.ResponseWriter, r *http.Request, result *store.ImportResult, places []tracker.Place, err error) {
	data := map[string]any{"Title": "Import rooms from the tracker", "Result": result}
	if errors.Is(err, tracker.ErrNotConfigured) {
		data["Off"] = true
		s.render(w, r, http.StatusOK, "rooms/import", data)
		return
	} else if err != nil {
		data["Error"] = err.Error()
		s.render(w, r, http.StatusOK, "rooms/import", data)
		return
	}
	rooms, err := s.store.ListRooms(true)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byPlace, byName := map[int64]*store.Room{}, map[string]*store.Room{}
	for i := range rooms {
		rm := &rooms[i]
		if rm.PlaceID != 0 {
			byPlace[rm.PlaceID] = rm
		}
		byName[strings.ToLower(rm.Name)] = rm
	}
	var list []importPlace
	seen := map[int64]bool{}
	for _, p := range places {
		seen[p.ID] = true
		if !bookableKind(p.Kind) && byPlace[p.ID] == nil {
			continue
		}
		ip := importPlace{Place: p, Label: p.Label()}
		if rm := byPlace[p.ID]; rm != nil {
			ip.Room, ip.Exists = rm, true
		} else if rm := byName[strings.ToLower(ip.Label)]; rm != nil {
			ip.Room, ip.Taken = rm, rm.PlaceID != 0
		}
		list = append(list, ip)
	}
	var gone []store.Room
	for _, rm := range rooms {
		if rm.PlaceID != 0 && !seen[rm.PlaceID] && !rm.Archived {
			gone = append(gone, rm)
		}
	}
	data["Places"], data["Gone"] = list, gone
	s.render(w, r, http.StatusOK, "rooms/import", data)
}

// bookableKind is the kinds of place a room can be: not sites or floors.
func bookableKind(kind string) bool {
	return kind == "building" || kind == "room" || kind == "area"
}

// handleRoomImportRun imports the picked places and brings every room
// already imported up to date with the tracker, then shows what it did.
func (s *Server) handleRoomImportRun(w http.ResponseWriter, r *http.Request) {
	if !s.tracker().Configured() {
		s.renderRoomImport(w, r, nil, nil, tracker.ErrNotConfigured)
		return
	}
	places, err := s.trackerPlaces(r.Context())
	if err != nil {
		s.setFlash(w, r, "error", "Nothing was imported: "+err.Error())
		http.Redirect(w, r, "/rooms/import", http.StatusSeeOther)
		return
	}
	var all []store.TrackerPlace
	for _, p := range places {
		all = append(all, store.TrackerPlace{ID: p.ID, Label: p.Label(), Name: p.Name, Path: p.Path})
	}
	var pick []int64
	for _, v := range r.PostForm["place"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			pick = append(pick, id)
		}
	}
	res, err := s.store.ImportRooms(all, pick)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("rooms imported from tracker", "user", currentUser(r).Username, "added", len(res.Added),
		"linked", len(res.Linked), "renamed", len(res.Renamed), "archived", len(res.Archived), "bookings", res.Bookings)
	s.renderRoomImport(w, r, &res, places, nil)
}
