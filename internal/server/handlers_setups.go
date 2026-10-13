package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/church-calendar/internal/store"
)

// /setups is for schedulers: what the next two weeks of bookings need set
// up, and what is still set up from before. Short lines are flagged. With
// the calendar alone each counted line has an In use / In storage toggle;
// with the tracker, what's still set up is the equipment it has sitting in
// a calendar room.

const setupDays = 14

// setupBooking is a booking with the lines it asks for.
type setupBooking struct {
	Booking store.Booking
	Equip   *equipView
}

// setupDay is one day's bookings.
type setupDay struct {
	Date  string
	Items []setupBooking
}

// stillThere is equipment the tracker has sitting in a calendar room.
type stillThere struct {
	Name  string
	Place store.EquipmentPlace
}

func (s *Server) handleSetups(w http.ResponseWriter, r *http.Request) {
	today := s.today()
	to := s.todayTime().AddDate(0, 0, setupDays-1).Format(store.DateLayout)
	list, err := s.store.BookingsAskingEquipment(today, to)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ids := make([]int64, len(list))
	for i, b := range list {
		ids[i] = b.ID
	}
	lines, err := s.store.BookingsEquipment(ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var days []*setupDay
	for _, b := range list {
		v, err := s.buildEquipView(lines[b.ID], &b, true, false, "/setups")
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if v == nil {
			continue
		}
		v.Toggle = !s.trackerOn()
		date := max(b.StartsOn, today) // one that began earlier is under way: today
		if n := len(days); n == 0 || days[n-1].Date != date {
			days = append(days, &setupDay{Date: date})
		}
		d := days[len(days)-1]
		d.Items = append(d.Items, setupBooking{Booking: b, Equip: v})
	}

	data := map[string]any{"Title": "Setups", "Days": days, "TrackerOn": s.trackerOn(), "Back": "/setups"}
	if s.trackerOn() {
		equipment, err := s.store.ListEquipment(true)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		names := map[int64]string{}
		for _, e := range equipment {
			names[e.ID] = e.Name
		}
		places, err := s.store.EquipmentPlaces()
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		var still []stillThere
		for _, e := range equipment {
			for _, p := range places[e.ID] {
				if p.RoomID != 0 {
					still = append(still, stillThere{Name: names[e.ID], Place: p})
				}
			}
		}
		data["Still"] = still
	} else {
		where, err := s.loadWhere()
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		var still []inUseHere
		for _, list := range where.inUse {
			still = append(still, list...)
		}
		slices.SortStableFunc(still, func(a, b inUseHere) int {
			return strings.Compare(a.Line.InUseChangedAt, b.Line.InUseChangedAt)
		})
		data["InUse"] = still
	}
	s.render(w, r, http.StatusOK, "setups", data)
}

// handleLineInUse turns the In use / In storage toggle on a booking's line.
// Schedulers only (the route checks), and only while the tracker isn't
// connected: with it, where equipment is comes from the tracker.
func (s *Server) handleLineInUse(w http.ResponseWriter, r *http.Request) {
	if s.trackerOn() {
		s.render(w, r, http.StatusForbidden, "error", map[string]any{
			"Title": "Set in the tracker", "Message": "Where equipment is comes from the maintenance tracker, so it can't be set here.",
		})
		return
	}
	id := pathID(r)
	equipmentID, _ := strconv.ParseInt(r.PathValue("equipment_id"), 10, 64)
	lines, err := s.store.BookingEquipment(id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var line *store.EquipmentLine
	for i := range lines {
		if lines[i].EquipmentID == equipmentID && !lines[i].Service() {
			line = &lines[i]
		}
	}
	if line == nil {
		s.notFound(w, r)
		return
	}
	inUse := formStr(r, "in_use") == "1"
	if err := s.store.SetLineInUse(id, equipmentID, inUse, currentUser(r).ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := line.Name + " is in storage."
	if inUse {
		msg = line.Name + " is in use."
	}
	back := bookingURL(id)
	if to := formStr(r, "back"); to != "" {
		back = safeRedirect(to)
	}
	s.redirect(w, r, back, msg)
}

// handleShortBookings is the Short tab on /requests: upcoming bookings with
// a line asked for over the total, each with its short lines and a link to
// its request when it has one.
func (s *Server) handleShortBookings(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ShortBookings(s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ids := make([]int64, len(list))
	for i, b := range list {
		ids[i] = b.ID
	}
	lines, err := s.store.BookingsEquipment(ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	type shortRow struct {
		Booking   store.Booking
		RequestID int64
		Lines     []store.EquipmentLine // the short ones
	}
	var rows []shortRow
	for _, b := range list {
		row := shortRow{Booking: b}
		if row.RequestID, err = s.store.RequestForBooking(b.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, l := range lines[b.ID] {
			if l.Short > 0 {
				row.Lines = append(row.Lines, l)
			}
		}
		rows = append(rows, row)
	}
	s.render(w, r, http.StatusOK, "requests/index", map[string]any{
		"Title": "Room requests", "Status": "short", "Statuses": store.RequestStatuses, "ShortRows": rows,
	})
}
