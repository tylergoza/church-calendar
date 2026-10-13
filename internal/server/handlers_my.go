package server

import (
	"errors"
	"net/http"
	"slices"

	"github.com/tylergoza/church-calendar/internal/store"
)

// People change or cancel their own bookings: /my/bookings lists them,
// /request/{id}/edit changes one and /request/{id}/cancel cancels it. Only
// the signed-in member who asked may (the status link's code is not
// enough: visitors can only withdraw while it waits). A booking a planner
// took over belongs to that event now, and is changed there.

// myRow is one of a member's requests with its booking.
type myRow struct {
	Request   store.Request
	Booking   *store.Booking // nil when there's none
	TakenOver bool           // a planner owns the booking now
	Upcoming  bool
}

// takenOver loads req's booking and reports whether a planner took it over
// (it's no longer a request booking).
func (s *Server) takenOver(req *store.Request) (*store.Booking, bool, error) {
	if req.BookingID == 0 {
		return nil, false, nil
	}
	b, err := s.store.GetBooking(req.BookingID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	return b, b.Source != store.SourceRequest, nil
}

// canChange reports whether the owner may still edit or cancel req: it's
// waiting or booked, hasn't passed and isn't an event's now.
func (s *Server) canChange(req *store.Request, takenOver bool) bool {
	last := req.EndsOn
	if last == "" {
		last = req.StartsOn
	}
	return (req.Status == "waiting" || req.Status == "approved") && !takenOver && last >= s.today()
}

func (s *Server) handleMyBookings(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	reqs, err := s.store.RequestsBy(u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var rows []myRow
	for _, q := range reqs {
		row := myRow{Request: q}
		if row.Booking, row.TakenOver, err = s.takenOver(&q); err != nil {
			s.serverError(w, r, err)
			return
		}
		last := q.EndsOn
		if last == "" {
			last = q.StartsOn
		}
		row.Upcoming = last >= s.today() && q.Status != "declined" && q.Status != "withdrawn" && q.Status != "cancelled"
		rows = append(rows, row)
	}
	// Upcoming first, soonest at the top; then what's over, newest first.
	slices.SortStableFunc(rows, func(a, b myRow) int {
		switch {
		case a.Upcoming && !b.Upcoming:
			return -1
		case !a.Upcoming && b.Upcoming:
			return 1
		case a.Upcoming:
			return compareStrings(a.Request.StartsOn+a.Request.StartTime, b.Request.StartsOn+b.Request.StartTime)
		}
		return compareStrings(b.Request.StartsOn+b.Request.StartTime, a.Request.StartsOn+a.Request.StartTime)
	})
	made, err := s.store.BookingsOf(u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var upcoming, past []store.Booking
	for _, b := range made {
		if b.LastDate() >= s.today() {
			upcoming = append(upcoming, b)
		} else {
			past = append(past, b)
		}
	}
	slices.Reverse(upcoming) // soonest first
	made = append(upcoming, past...)

	// Equipment on their own bookings, for the owner only.
	var ids []int64
	for _, row := range rows {
		if row.Booking != nil {
			ids = append(ids, row.Booking.ID)
		}
	}
	for _, b := range made {
		ids = append(ids, b.ID)
	}
	equip, err := s.store.BookingsEquipment(ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "my/bookings", map[string]any{
		"Title": "My bookings", "Rows": rows, "Made": made, "Equip": equip,
	})
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// refuse answers someone who may not change a request.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, message string) {
	s.render(w, r, http.StatusForbidden, "error", map[string]any{
		"Title": "Not yours to change", "Message": message, "PublicHeader": currentUser(r) == nil,
	})
}

// myRequest loads the request in the path for the member who sent it. A
// visitor with the link, or another member, is refused with 403 (404 when
// there's no such request).
func (s *Server) myRequest(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	req, err := s.store.GetRequest(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, false
	} else if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if u := currentUser(r); u == nil || req.UserID == 0 || u.ID != req.UserID {
		s.refuse(w, r, "Only the person who made this booking can change it. Visitors can withdraw a request that's still waiting from the link they were given.")
		return nil, false
	}
	return req, true
}

// changeable is myRequest plus the check that it can still be changed; if
// not it sends the person back to the status page with the reason.
func (s *Server) changeable(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	req, ok := s.myRequest(w, r)
	if !ok {
		return nil, false
	}
	_, taken, err := s.takenOver(req)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if !s.canChange(req, taken) {
		msg := "That booking can't be changed any more."
		if taken {
			msg = "That booking is now part of an event, so it's changed there."
		}
		s.setFlash(w, r, "error", msg)
		http.Redirect(w, r, requestURL(req.ID, req.Code), http.StatusSeeOther)
		return nil, false
	}
	return req, true
}

func (s *Server) requestEditForm(w http.ResponseWriter, r *http.Request, status int, req store.Request, picked []int64, errs []string) {
	rooms, err := s.bookableRooms()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	isPicked := map[int64]bool{}
	for _, id := range picked {
		isPicked[id] = true
	}
	equip, err := s.formEquipmentRows(rooms, req.Equipment, req.StartsOn, req.StartTime, req.EndsOn, req.EndTime, req.BookingID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	s.render(w, r, status, "requests/edit", map[string]any{
		"Title": "Change " + req.Title, "Form": req, "Rooms": rooms, "Picked": isPicked, "Errors": errs, "Today": s.today(),
		"Equip": equip,
	})
}

func (s *Server) handleRequestEdit(w http.ResponseWriter, r *http.Request) {
	req, ok := s.changeable(w, r)
	if !ok {
		return
	}
	form := *req
	var err error
	if form.Equipment, err = s.store.RequestEquipment(req.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.requestEditForm(w, r, http.StatusOK, form, req.RoomIDs(), nil)
}

func (s *Server) handleRequestUpdate(w http.ResponseWriter, r *http.Request) {
	req, ok := s.changeable(w, r)
	if !ok {
		return
	}
	form := *req
	rooms, errs, err := s.readRequest(r, &form)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Who asked doesn't change here.
	form.Name, form.Contact, form.Phone, form.Code, form.UserID = req.Name, req.Contact, req.Phone, req.Code, req.UserID
	if errs != nil {
		s.requestEditForm(w, r, http.StatusUnprocessableEntity, form, rooms, errs)
		return
	}
	busy, err := s.store.UpdateOwnRequest(&form, rooms)
	switch {
	case errors.Is(err, store.ErrRoomsBusy):
		s.requestEditForm(w, r, http.StatusConflict, form, rooms, busyErrors(busy, form.EndsOn != ""))
		return
	case errors.Is(err, store.ErrAlreadyDecided), errors.Is(err, store.ErrTakenOver):
		s.setFlash(w, r, "error", "That booking can't be changed any more.")
		http.Redirect(w, r, requestURL(req.ID, req.Code), http.StatusSeeOther)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	note, err := s.bookingShortNote(form.BookingID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, requestURL(req.ID, req.Code), "Saved. The rooms are booked for you."+note)
}

func (s *Server) handleRequestCancel(w http.ResponseWriter, r *http.Request) {
	req, ok := s.changeable(w, r)
	if !ok {
		return
	}
	if err := s.store.CancelOwnRequest(req.ID); errors.Is(err, store.ErrAlreadyDecided) || errors.Is(err, store.ErrTakenOver) {
		s.setFlash(w, r, "error", "That booking can't be cancelled any more.")
		http.Redirect(w, r, requestURL(req.ID, req.Code), http.StatusSeeOther)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, requestURL(req.ID, req.Code), "Cancelled. The rooms are free again.")
}

// Schedulers cancelling ---------------------------------------------------

// handleBookingCancel keeps a hand-made or request booking on record but
// off the calendar, with a note for the person. A date of a repeat can be
// cancelled alone or with every later one.
func (s *Server) handleBookingCancel(w http.ResponseWriter, r *http.Request) {
	b, ok := s.handMade(w, r) // 403 for another app's booking
	if !ok {
		return
	}
	note := formStr(r, "note")
	if len(note) > 4000 {
		s.setFlash(w, r, "error", "Keep the note under 4,000 characters.")
		http.Redirect(w, r, bookingURL(b.ID), http.StatusSeeOther)
		return
	}
	if b.Cancelled() {
		s.redirect(w, r, bookingURL(b.ID), "That booking was already cancelled.")
		return
	}
	by := currentUser(r).ID
	msg := b.Title + " cancelled."
	if b.SeriesID != 0 && formStr(r, "apply") == "later" {
		n, err := s.store.CancelSeriesFrom(b.SeriesID, b.OccurrenceDate, by, note)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		msg = b.Title + " no longer repeats from " + fmtDate(b.OccurrenceDate) + ": " + pluralize(n, "date") + " cancelled."
	} else if err := s.store.CancelBooking(b.ID, by, note); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, bookingURL(b.ID), msg)
}
