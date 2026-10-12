package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tylergoza/church-calendar/internal/store"
)

// roomWeeks is how far ahead a room's page lists its bookings.
const roomWeeks = 8

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
	from := s.today()
	to := s.todayTime().AddDate(0, 0, roomWeeks*7-1).Format(store.DateLayout)
	bookings, err := s.store.BookingsBetween(from, to, rm.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "rooms/show", map[string]any{
		"Title": rm.Name, "Room": rm, "Bookings": bookings, "Weeks": roomWeeks,
	})
}

func (s *Server) roomForm(w http.ResponseWriter, r *http.Request, status int, rm store.Room, errs []string) {
	title := "Add a room"
	if rm.ID != 0 {
		title = "Edit " + rm.Name
	}
	s.render(w, r, status, "rooms/form", map[string]any{"Title": title, "Form": rm, "Errors": errs})
}

func (s *Server) handleRoomNew(w http.ResponseWriter, r *http.Request) {
	s.roomForm(w, r, http.StatusOK, store.Room{Bookable: true, NeedsApproval: true}, nil)
}

// readRoom reads the form into rm. The archived box is only on the edit
// form, so a new room is never archived.
func readRoom(r *http.Request, rm *store.Room) []string {
	var errs []string
	rm.Name, rm.Notes = formStr(r, "name"), formStr(r, "notes")
	rm.Bookable, rm.NeedsApproval = formStr(r, "bookable") == "1", formStr(r, "needs_approval") == "1"
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

func (s *Server) handleRoomCreate(w http.ResponseWriter, r *http.Request) {
	var rm store.Room
	if errs := readRoom(r, &rm); errs != nil {
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
	if errs := readRoom(r, rm); errs != nil {
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
