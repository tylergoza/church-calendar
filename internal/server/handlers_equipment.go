package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/tylergoza/church-calendar/internal/store"
)

// adminEquipmentRow is one line of the list: the equipment and the names of
// the rooms it's offered with.
type adminEquipmentRow struct {
	store.Equipment
	Rooms []string
	Where *equipWhere // counted equipment only
}

func (s *Server) handleEquipment(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.ListEquipment(r.URL.Query().Get("archived") == "1")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rooms, err := s.store.ListRooms(true)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	names := map[int64]string{}
	for _, rm := range rooms {
		names[rm.ID] = rm.Name
	}
	where, err := s.loadWhere()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rows := make([]adminEquipmentRow, len(all))
	for i, e := range all {
		rows[i].Equipment = e
		if !e.Service() {
			rows[i].Where = where.of(e.ID, e.Total, e.FromTracker())
		}
		for _, id := range e.RoomIDs {
			rows[i].Rooms = append(rows[i].Rooms, names[id])
		}
	}
	s.render(w, r, http.StatusOK, "equipment/index", map[string]any{
		"Title": "Equipment", "Equipment": rows, "ShowArchived": r.URL.Query().Get("archived") == "1",
	})
}

func (s *Server) equipmentForm(w http.ResponseWriter, r *http.Request, status int, e store.Equipment, errs []string) {
	title := "Add equipment"
	if e.ID != 0 {
		title = "Edit " + e.Name
	}
	every, err := s.store.ListRooms(false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	bookable, err := s.bookableRooms()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	loose, groups := groupRooms(every, bookable)
	picked := map[int64]bool{}
	for _, id := range e.RoomIDs {
		picked[id] = true
	}
	data := map[string]any{
		"Title": title, "Form": e, "Errors": errs, "Loose": loose, "Groups": groups, "Picked": picked,
		"HasRooms": len(bookable) > 0,
	}
	if e.FromTracker() {
		data["TrackerURL"] = s.tracker().PageURL("products", e.ProductID)
		var names []string
		for _, rm := range every {
			if picked[rm.ID] {
				names = append(names, rm.Name)
			}
		}
		data["RoomNames"] = names
	}
	s.render(w, r, status, "equipment/form", data)
}

func (s *Server) handleEquipmentNew(w http.ResponseWriter, r *http.Request) {
	s.equipmentForm(w, r, http.StatusOK, store.Equipment{Mode: store.ModeCount}, nil)
}

// readEquipmentForm reads the form into e. Imported equipment never gets here:
// its fields are the tracker's. The archived box is only on the edit form.
func (s *Server) readEquipmentForm(r *http.Request, e *store.Equipment) []string {
	var errs []string
	e.Name, e.Notes = formStr(r, "name"), formStr(r, "notes")
	e.Mode = formStr(r, "mode")
	e.Total = 0
	switch e.Mode {
	case store.ModeCount:
		if v := formStr(r, "total"); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n < 0 || n > 100000 {
				errs = append(errs, "Total is how many there are, or leave it empty.")
			} else {
				e.Total = n
			}
		}
	case store.ModeService:
	default:
		e.Mode = store.ModeCount
		errs = append(errs, "Pick how it's asked for.")
	}
	if e.ID != 0 {
		e.Archived = formStr(r, "archived") == "1"
	}
	bookable, err := s.bookableRooms()
	if err != nil {
		return append(errs, "The rooms couldn't be read.")
	}
	e.RoomIDs = nil
	for _, v := range r.PostForm["room"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || slices.Contains(e.RoomIDs, id) {
			continue
		}
		if slices.ContainsFunc(bookable, func(rm store.Room) bool { return rm.ID == id }) {
			e.RoomIDs = append(e.RoomIDs, id)
		}
	}
	if e.Name == "" {
		errs = append(errs, "Give the equipment a name.")
	}
	return errs
}

func (s *Server) handleEquipmentCreate(w http.ResponseWriter, r *http.Request) {
	var e store.Equipment
	if errs := s.readEquipmentForm(r, &e); errs != nil {
		s.equipmentForm(w, r, http.StatusUnprocessableEntity, e, errs)
		return
	}
	_, err := s.store.CreateEquipment(&e)
	if errors.Is(err, store.ErrEquipmentNameTaken) {
		s.equipmentForm(w, r, http.StatusUnprocessableEntity, e, []string{"Other equipment is already called " + e.Name + "."})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/equipment", e.Name+" added.")
}

func (s *Server) handleEquipmentEdit(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.GetEquipment(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.equipmentForm(w, r, http.StatusOK, *e, nil)
}

func (s *Server) handleEquipmentUpdate(w http.ResponseWriter, r *http.Request) {
	e, err := s.store.GetEquipment(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if e.FromTracker() {
		s.equipmentForm(w, r, http.StatusForbidden, *e, []string{"This equipment comes from the tracker. Change it there."})
		return
	}
	if errs := s.readEquipmentForm(r, e); errs != nil {
		s.equipmentForm(w, r, http.StatusUnprocessableEntity, *e, errs)
		return
	}
	err = s.store.UpdateEquipment(e)
	if errors.Is(err, store.ErrEquipmentNameTaken) {
		s.equipmentForm(w, r, http.StatusUnprocessableEntity, *e, []string{"Other equipment is already called " + e.Name + "."})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Saved."
	if e.Archived {
		msg = e.Name + " is archived: it's kept on old bookings but can't be asked for any more."
	}
	s.redirect(w, r, "/equipment", msg)
}
