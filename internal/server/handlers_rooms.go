package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

func roomURL(id int64) string { return "/rooms/" + strconv.FormatInt(id, 10) }

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	rooms, err := s.store.ListRooms(u.CanSchedule() && r.URL.Query().Get("archived") == "1")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "rooms/index", map[string]any{
		"Title": "Rooms", "Rooms": rooms, "ShowArchived": r.URL.Query().Get("archived") == "1",
		"TrackerOn": s.tracker().Configured(),
	})
}

func (s *Server) handleRoomShow(w http.ResponseWriter, r *http.Request) {
	rm, err := s.store.GetRoom(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	start := s.weekOf(r)
	end := start.AddDate(0, 0, 6)
	entries, err := s.store.Calendar(start.Format(store.DateLayout), end.Format(store.DateLayout), s.access(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	days := calDays(start, end, 0, s.today(), entries, func(e store.CalendarEntry) bool { return hasRoom(e, rm.Name) })
	week := func(d time.Time) string { return roomURL(rm.ID) + "?week=" + d.Format(store.DateLayout) }
	var inside *store.Room
	if rm.ParentID != 0 {
		inside, _ = s.store.GetRoom(rm.ParentID)
	}
	data := map[string]any{
		"Inside": inside,
		"Title":  rm.Name, "Room": rm, "Days": days, "Week": weekLabel(start),
		"PrevURL": week(start.AddDate(0, 0, -7)), "NextURL": week(start.AddDate(0, 0, 7)),
		"IsThisWeek": start.Format(store.DateLayout) == s.thisWeek(),
		"Weekdays":   []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	}
	if rm.FromTracker() {
		data["TrackerURL"] = s.tracker().PageURL("place", rm.PlaceID)
	}
	s.render(w, r, http.StatusOK, "rooms/show", data)
}

func (s *Server) roomForm(w http.ResponseWriter, r *http.Request, status int, rm store.Room, errs []string) {
	title := "Add a room"
	if rm.ID != 0 {
		title = "Edit " + rm.Name
	}
	parents, err := s.parentChoices(rm.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := map[string]any{"Title": title, "Form": rm, "Errors": errs, "Parents": parents}
	if rm.FromTracker() {
		data["TrackerURL"] = s.tracker().PageURL("place", rm.PlaceID)
	}
	s.render(w, r, status, "rooms/form", data)
}

// parentChoices are the rooms a hand-made room can be inside: not archived,
// and not the room itself or anything inside it (that would be a loop).
func (s *Server) parentChoices(self int64) ([]store.Room, error) {
	all, err := s.store.ListRooms(false)
	if err != nil {
		return nil, err
	}
	banned := map[int64]bool{self: self != 0}
	for grew := true; grew; {
		grew = false
		for _, rm := range all {
			if !banned[rm.ID] && banned[rm.ParentID] {
				banned[rm.ID], grew = true, true
			}
		}
	}
	var out []store.Room
	for _, rm := range all {
		if !banned[rm.ID] {
			out = append(out, rm)
		}
	}
	return out, nil
}

func (s *Server) handleRoomNew(w http.ResponseWriter, r *http.Request) {
	s.roomForm(w, r, http.StatusOK, store.Room{Bookable: true}, nil)
}

// readRoom reads the form into rm. The archived box is only on the edit
// form, so a new room is never archived.
func readRoom(r *http.Request, rm *store.Room) []string {
	var errs []string
	rm.Name, rm.Notes = formStr(r, "name"), formStr(r, "notes")
	if !rm.FromTracker() {
		// An imported room's bookable flag, kind and building are the tracker's.
		rm.Bookable = formStr(r, "bookable") == "1"
		rm.ParentID = 0
		if v := formStr(r, "parent"); v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				rm.ParentID = id
			}
		}
	}
	if rm.ID != 0 {
		rm.Archived = formStr(r, "archived") == "1"
	}
	rm.Capacity = 0
	if v := formStr(r, "capacity"); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 100000 {
			errs = append(errs, "Capacity is a number of people, or leave it empty.")
		} else {
			rm.Capacity = n
		}
	}
	if rm.Name == "" {
		errs = append(errs, "Give the room a name.")
	}
	return errs
}

// readRoomChecked is readRoom, then checks that Inside is a room that may
// hold this one.
func (s *Server) readRoomChecked(r *http.Request, rm *store.Room) []string {
	errs := readRoom(r, rm)
	if rm.ParentID != 0 && !rm.FromTracker() {
		choices, err := s.parentChoices(rm.ID)
		if err != nil || !slices.ContainsFunc(choices, func(c store.Room) bool { return c.ID == rm.ParentID }) {
			rm.ParentID = 0
			errs = append(errs, "Pick a room that isn't this one or inside it for Inside, or leave it empty.")
		}
	}
	return errs
}

func (s *Server) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	var rm store.Room
	if errs := s.readRoomChecked(r, &rm); errs != nil {
		s.roomForm(w, r, http.StatusUnprocessableEntity, rm, errs)
		return
	}
	id, err := s.store.CreateRoom(&rm)
	if errors.Is(err, store.ErrRoomNameTaken) {
		s.roomForm(w, r, http.StatusUnprocessableEntity, rm, []string{"Another room is already called " + rm.Name + "."})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, roomURL(id), rm.Name+" added.")
}

func (s *Server) handleRoomEdit(w http.ResponseWriter, r *http.Request) {
	rm, err := s.store.GetRoom(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.roomForm(w, r, http.StatusOK, *rm, nil)
}

func (s *Server) handleRoomUpdate(w http.ResponseWriter, r *http.Request) {
	rm, err := s.store.GetRoom(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if errs := s.readRoomChecked(r, rm); errs != nil {
		s.roomForm(w, r, http.StatusUnprocessableEntity, *rm, errs)
		return
	}
	err = s.store.UpdateRoom(rm)
	if errors.Is(err, store.ErrRoomNameTaken) {
		s.roomForm(w, r, http.StatusUnprocessableEntity, *rm, []string{"Another room is already called " + rm.Name + "."})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Saved."
	if rm.Archived {
		msg = rm.Name + " is archived: it's kept on old bookings but can't be picked for new ones."
	}
	s.redirect(w, r, roomURL(rm.ID), msg)
}
