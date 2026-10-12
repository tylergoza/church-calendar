package server

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/tylergoza/church-calendar/internal/recur"
	"github.com/tylergoza/church-calendar/internal/store"
)

func bookingURL(id int64) string { return "/calendar/" + strconv.FormatInt(id, 10) }

// canSeeDetails is the visibility rule, the same as the event planner's:
// public bookings show in full to everyone, private ones to people signed
// in, and locked ones to nobody (only their rooms and times show).
func canSeeDetails(b *store.Booking, u *store.User) bool {
	switch b.Visibility {
	case "public":
		return true
	case "private":
		return u != nil
	}
	return false
}

// sourceLabel names where a booking came from.
func sourceLabel(source string) string {
	switch source {
	case store.SourceCalendar:
		return "Calendar"
	case store.SourceRequest:
		return "Room request"
	case "events":
		return "Event planner"
	case "productions":
		return "Production planner"
	}
	return source
}

// TopUpSeries makes the dates repeating bookings are due, up to six months
// ahead. Run at startup and every few hours.
func (s *Server) TopUpSeries() (int, error) {
	n, err := s.store.ExtendSeries(s.today())
	if err != nil {
		s.log.Error("extend repeating bookings", "err", err)
		return n, err
	}
	if n > 0 {
		s.log.Info("extended repeating bookings", "created", n)
	}
	return n, nil
}

// booking loads the booking in the path, answering 404 when there's none.
func (s *Server) booking(w http.ResponseWriter, r *http.Request) (*store.Booking, bool) {
	b, err := s.store.GetBooking(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil, false
	} else if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return b, true
}

// handMade loads the booking in the path for changing. Bookings from
// another app are read-only here: they're changed in that app.
func (s *Server) handMade(w http.ResponseWriter, r *http.Request) (*store.Booking, bool) {
	b, ok := s.booking(w, r)
	if !ok {
		return nil, false
	}
	if !b.HandMade() {
		s.render(w, r, http.StatusForbidden, "error", map[string]any{
			"Title":   "Change it in " + sourceLabel(b.Source),
			"Message": "This booking comes from the " + sourceLabel(b.Source) + ", so it can only be changed there. The calendar shows a copy.",
		})
		return nil, false
	}
	return b, true
}

func (s *Server) handleBookingShow(w http.ResponseWriter, r *http.Request) {
	b, ok := s.booking(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	data := map[string]any{"Booking": b, "CanSee": canSeeDetails(b, u), "Source": sourceLabel(b.Source)}
	data["Title"] = "Busy"
	if data["CanSee"].(bool) {
		data["Title"] = b.Title
	}
	if b.SeriesID != 0 && u != nil {
		sr, err := s.store.GetSeries(b.SeriesID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Repeats"] = sr.Rule.Describe()
	}
	if u.CanSchedule() {
		clashes, err := s.store.ConflictsWith(b.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Conflicts"] = clashes
	}
	s.render(w, r, http.StatusOK, "bookings/show", data)
}

// roomChoices is the rooms the form offers: those not archived, plus any
// archived ones the booking already uses so saving doesn't drop them.
func (s *Server) roomChoices(b *store.Booking) ([]store.Room, error) {
	all, err := s.store.ListRooms(true)
	if err != nil {
		return nil, err
	}
	using := b.RoomIDs()
	var out []store.Room
	for _, rm := range all {
		if !rm.Archived || slices.Contains(using, rm.ID) {
			out = append(out, rm)
		}
	}
	return out, nil
}

func (s *Server) bookingForm(w http.ResponseWriter, r *http.Request, status int, b store.Booking, picked []int64, errs []string, extra map[string]any) {
	rooms, err := s.roomChoices(&b)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	isPicked := map[int64]bool{}
	for _, id := range picked {
		isPicked[id] = true
	}
	title := "Add a booking"
	if b.ID != 0 {
		title = "Edit " + b.Title
	}
	data := map[string]any{
		"Title": title, "Form": b, "Rooms": rooms, "Picked": isPicked, "Errors": errs, "Statuses": store.BookingStatuses,
	}
	if b.SeriesID != 0 {
		sr, err := s.store.GetSeries(b.SeriesID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data["Repeats"] = sr.Rule.Describe()
	}
	if _, ok := extra["Repeat"]; !ok && b.SeriesID == 0 {
		data["Repeat"] = newRepeat(b.StartsOn)
	}
	for k, v := range extra {
		data[k] = v
	}
	s.render(w, r, status, "bookings/form", data)
}

func (s *Server) handleBookingNew(w http.ResponseWriter, r *http.Request) {
	b := store.Booking{StartsOn: s.today(), Status: "confirmed", Visibility: "private"}
	if d := r.URL.Query().Get("date"); validDate(d) {
		b.StartsOn = d
	}
	var picked []int64
	if id := queryInt(r, "room"); id != 0 {
		picked = append(picked, id)
	}
	s.bookingForm(w, r, http.StatusOK, b, picked, nil, nil)
}

// readBooking reads the form into b and returns the rooms picked.
func readBooking(r *http.Request, b *store.Booking) ([]int64, []string) {
	b.Title, b.Kind, b.Status = formStr(r, "title"), formStr(r, "kind"), formStr(r, "status")
	b.StartsOn, b.StartTime = formStr(r, "starts_on"), formStr(r, "start_time")
	b.EndsOn, b.EndTime = formStr(r, "ends_on"), formStr(r, "end_time")
	b.Description = formStr(r, "description")
	// Hand-made bookings are public or private. Locked is for apps that
	// send no details at all; a scheduler making one here knows them anyway.
	if b.Visibility = formStr(r, "visibility"); b.Visibility != "public" {
		b.Visibility = "private"
	}
	if b.EndsOn == b.StartsOn {
		b.EndsOn = ""
	}
	var rooms []int64
	for _, v := range r.PostForm["room"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 && !slices.Contains(rooms, id) {
			rooms = append(rooms, id)
		}
	}
	var errs []string
	if b.Title == "" {
		errs = append(errs, "Give the booking a title.")
	}
	if !slices.Contains(store.BookingStatuses, b.Status) {
		errs = append(errs, "Pick a status.")
	}
	if !validDate(b.StartsOn) {
		errs = append(errs, "Pick the day it starts.")
	}
	if b.EndsOn != "" && (!validDate(b.EndsOn) || b.EndsOn < b.StartsOn) {
		errs = append(errs, "It can't end before the day it starts.")
	}
	timesOK := true
	for _, t := range []string{b.StartTime, b.EndTime} {
		if t != "" && !validTime(t) {
			errs = append(errs, "Times look like 18:30.")
			timesOK = false
			break
		}
	}
	if timesOK && b.EndTime != "" && b.StartTime == "" {
		errs = append(errs, "Add a start time, or clear the end time for an all-day booking.")
	} else if timesOK && b.EndsOn == "" && b.EndTime != "" && b.EndTime <= b.StartTime {
		errs = append(errs, "It has to end after it starts.")
	}
	return rooms, errs
}

// alignToRule moves a new booking (and rule.Start) to the first date its
// repeat pattern gives on or after the chosen start, keeping its length.
func alignToRule(b *store.Booking, rule *recur.Rule) error {
	start, err := recur.ParseDate(b.StartsOn)
	if err != nil {
		return err
	}
	first, ok := rule.Next(start.AddDate(0, 0, -1))
	if !ok {
		return errors.New("That pattern has no dates on or after the start date.")
	}
	if first.Equal(start) {
		return nil
	}
	if b.EndsOn != "" {
		if end, err := recur.ParseDate(b.EndsOn); err == nil {
			b.EndsOn = recur.FormatDate(end.Add(first.Sub(start)))
		}
	}
	b.StartsOn = recur.FormatDate(first)
	rule.Start = first
	return nil
}

// clashNote is a sentence for the flash when a booking, or the dates of
// its repeat from today on, clash with others; "" when nothing does.
func (s *Server) clashNote(b *store.Booking) (string, error) {
	list := []store.Booking{*b}
	if b.SeriesID != 0 {
		occ, err := s.store.Occurrences(b.SeriesID)
		if err != nil {
			return "", err
		}
		list = list[:0]
		for _, o := range occ {
			if o.LastDate() >= s.today() {
				list = append(list, o)
			}
		}
	}
	n := 0
	for _, o := range list {
		c, err := s.store.ConflictsWith(o.ID)
		if err != nil {
			return "", err
		}
		if len(c) > 0 {
			n++
		}
	}
	switch {
	case n == 0:
		return "", nil
	case b.SeriesID == 0:
		return " It clashes with another booking in the same room: see below.", nil
	}
	return " " + pluralize(n, "date") + " clash with other bookings in the same rooms: see Conflicts.", nil
}

func (s *Server) handleBookingCreate(w http.ResponseWriter, r *http.Request) {
	var b store.Booking
	rooms, errs := readBooking(r, &b)
	rf, rule, repeats, rerrs := readRepeat(r, b.StartsOn)
	if repeats && rerrs == nil && errs == nil {
		// A repeat starts on the pattern's first date, so a start of Monday
		// for "every Wednesday" becomes that Wednesday.
		if err := alignToRule(&b, &rule); err != nil {
			rerrs = append(rerrs, err.Error())
		}
	}
	if errs = append(errs, rerrs...); errs != nil {
		s.bookingForm(w, r, http.StatusUnprocessableEntity, b, rooms, errs, map[string]any{"Repeat": rf})
		return
	}
	b.CreatedBy = currentUser(r).ID
	id, err := s.store.CreateBooking(&b, rooms)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := b.Title + " added."
	if repeats {
		if b.SeriesID, err = s.store.MakeSeries(id, rule, s.today()); err != nil {
			s.serverError(w, r, err)
			return
		}
		msg += " " + repeatSummary(rule) + "."
	}
	note, err := s.clashNote(&b)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, bookingURL(id), msg+note)
}

func (s *Server) handleBookingEdit(w http.ResponseWriter, r *http.Request) {
	b, ok := s.handMade(w, r)
	if !ok {
		return
	}
	s.bookingForm(w, r, http.StatusOK, *b, b.RoomIDs(), nil, map[string]any{"Apply": "this"})
}

// bookingChange is what differs between old and now, for "this and later".
func bookingChange(old store.Booking, now store.Booking, rooms []int64) store.BookingChange {
	str := func(a, b string) *string {
		if a == b {
			return nil
		}
		return &b
	}
	c := store.BookingChange{
		Title: str(old.Title, now.Title), Kind: str(old.Kind, now.Kind),
		StartTime: str(old.StartTime, now.StartTime), EndTime: str(old.EndTime, now.EndTime),
		Visibility: str(old.Visibility, now.Visibility), Description: str(old.Description, now.Description),
	}
	was, is := slices.Sorted(slices.Values(old.RoomIDs())), slices.Sorted(slices.Values(rooms))
	if !slices.Equal(was, is) {
		c.Rooms = &rooms
	}
	return c
}

func (s *Server) handleBookingUpdate(w http.ResponseWriter, r *http.Request) {
	b, ok := s.handMade(w, r)
	if !ok {
		return
	}
	old := *b
	rooms, errs := readBooking(r, b)
	extra := map[string]any{}
	scope := "this"
	var rule recur.Rule
	repeats := false
	if old.SeriesID != 0 {
		switch formStr(r, "apply") {
		case "", "this":
		case "later":
			scope = "later"
			if b.StartsOn != old.StartsOn || b.EndsOn != old.EndsOn {
				errs = append(errs, "Only this date can move to another day. To move every later date, end the repeat here and make a new one.")
			}
		default:
			errs = append(errs, "Choose whether to change just this date or this and later dates.")
		}
		extra["Apply"] = scope
	} else {
		var rf repeatForm
		var rerrs []string
		rf, rule, repeats, rerrs = readRepeat(r, b.StartsOn)
		errs = append(errs, rerrs...)
		extra["Repeat"] = rf
	}
	if errs != nil {
		s.bookingForm(w, r, http.StatusUnprocessableEntity, *b, rooms, errs, extra)
		return
	}
	if err := s.store.UpdateBooking(b, rooms); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "Saved."
	if scope == "later" {
		if c := bookingChange(old, *b, rooms); !c.Empty() {
			n, err := s.store.ApplyForward(b.ID, c)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			msg = "Saved. Changes made to " + pluralize(n, "date") + " (this and later)."
		}
	}
	if repeats {
		var err error
		if b.SeriesID, err = s.store.MakeSeries(b.ID, rule, s.today()); err != nil {
			s.serverError(w, r, err)
			return
		}
		msg = "Saved. " + repeatSummary(rule) + "."
	}
	note, err := s.clashNote(b)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, bookingURL(b.ID), msg+note)
}

func (s *Server) handleBookingDelete(w http.ResponseWriter, r *http.Request) {
	b, ok := s.handMade(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteBooking(b.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := b.Title + " deleted."
	if b.SeriesID != 0 {
		msg = b.Title + " on " + fmtDate(b.OccurrenceDate) + " removed. The other dates are still booked."
	}
	s.redirect(w, r, "/", msg)
}

// handleBookingEndSeries removes this date of a repeat and every later one.
func (s *Server) handleBookingEndSeries(w http.ResponseWriter, r *http.Request) {
	b, ok := s.handMade(w, r)
	if !ok {
		return
	}
	if b.SeriesID == 0 {
		s.redirect(w, r, bookingURL(b.ID), "")
		return
	}
	n, err := s.store.EndSeriesFrom(b.SeriesID, b.OccurrenceDate)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/", b.Title+" no longer repeats from "+fmtDate(b.OccurrenceDate)+": "+pluralize(n, "date")+" removed.")
}
