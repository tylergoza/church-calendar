package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

// The push API is how the other apps (the event planner, the production
// planner) put their dates on the calendar. Each sends with a source token
// made on /admin/tokens, tied to one source, and can only touch bookings
// with that source. The calendar keeps a thin copy and never refuses one
// for a clash: the event already exists in that app, so a PUT answers 200
// with the clashes listed, for the app to show as a warning.
//
// Availability and rooms take any token: what other bookings show follows
// the token's access, the same as the read API (an app always sees its
// own bookings in full).

const maxPushBody = 256 << 10

func (s *Server) pushRoutes(api func(string, http.HandlerFunc)) {
	api("PUT /api/v1/bookings/{source}/{source_id}", s.requireToken(store.TokenSource, false, s.ownSource(s.apiPutBooking)))
	api("DELETE /api/v1/bookings/{source}/{source_id}", s.requireToken(store.TokenSource, false, s.ownSource(s.apiDeleteBooking)))
	api("GET /api/v1/bookings/{source}", s.requireToken(store.TokenSource, false, s.ownSource(s.apiSourceBookings)))
	api("GET /api/v1/availability", s.requireToken("", false, s.apiAvailability))
	api("GET /api/v1/rooms", s.requireToken("", false, s.apiRooms))
}

// sourceIDRe is what an app's ID for a booking may look like: "12",
// "date-88".
var sourceIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

// ownSource refuses a source token reaching for another source's bookings.
func (s *Server) ownSource(next tokenHandler) tokenHandler {
	return func(w http.ResponseWriter, r *http.Request, tok *store.Token) {
		if r.PathValue("source") != tok.Source {
			apiError(w, http.StatusForbidden, `this token can only send bookings for "`+tok.Source+`"`)
			return
		}
		if strings.Contains(r.Pattern, "{source_id}") && !sourceIDRe.MatchString(r.PathValue("source_id")) {
			apiError(w, http.StatusBadRequest, "the booking's ID can only use letters, digits and . _ : - (at most 100)")
			return
		}
		next(w, r, tok)
	}
}

// JSON -----------------------------------------------------------------------

// apiBlockJSON is a booking in availability and conflicts. Busy ones (that
// the token may not see in full) carry only rooms and times. The event
// planner's client reads this shape (calendar.Block).
type apiBlockJSON struct {
	ID        int64    `json:"id,omitempty"`
	Source    string   `json:"source,omitempty"`
	SourceID  string   `json:"source_id,omitempty"`
	Title     string   `json:"title,omitempty"`
	Status    string   `json:"status,omitempty"`
	StartsOn  string   `json:"starts_on"`
	StartTime string   `json:"start_time"`
	EndsOn    string   `json:"ends_on"`
	EndTime   string   `json:"end_time"`
	Rooms     []string `json:"rooms"`
	Busy      bool     `json:"busy"`
}

// blockFor is b as tok sees it: its own source's in full, others by the
// token's access.
func blockFor(b store.Booking, tok *store.Token) apiBlockJSON {
	e := store.CalendarEntry{Booking: b}
	if tok.Kind != store.TokenSource || b.Source != tok.Source {
		e = store.EntryFor(b, tok.Access())
	}
	j := apiBlockJSON{Busy: e.Busy, StartsOn: e.StartsOn, StartTime: e.StartTime, EndsOn: e.EndsOn,
		EndTime: e.EndTime, Rooms: roomNames(e.Rooms)}
	if !e.Busy {
		j.ID, j.Source, j.SourceID, j.Title, j.Status = e.ID, e.Source, e.SourceID, e.Title, e.Status
	}
	return j
}

// apiSourceBookingJSON is one of an app's own bookings, as stored.
type apiSourceBookingJSON struct {
	ID          int64    `json:"id"`
	SourceID    string   `json:"source_id"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	StartsOn    string   `json:"starts_on"`
	StartTime   string   `json:"start_time"`
	EndsOn      string   `json:"ends_on"`
	EndTime     string   `json:"end_time"`
	Rooms       []string `json:"rooms"`
	Visibility  string   `json:"visibility"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Status      string   `json:"status"`
	UpdatedAt   string   `json:"updated_at"` // RFC 3339, UTC
	CalendarURL string   `json:"calendar_url"`
}

func (s *Server) sourceBookingJSON(r *http.Request, b store.Booking) apiSourceBookingJSON {
	updated := b.UpdatedAt
	if t, err := time.Parse(time.DateTime, updated); err == nil {
		updated = t.Format(time.RFC3339)
	}
	return apiSourceBookingJSON{ID: b.ID, SourceID: b.SourceID, Title: b.Title, Kind: b.Kind, StartsOn: b.StartsOn,
		StartTime: b.StartTime, EndsOn: b.EndsOn, EndTime: b.EndTime, Rooms: roomNames(b.Rooms), Visibility: b.Visibility,
		Description: b.Description, URL: b.URL, Status: b.Status, UpdatedAt: updated,
		CalendarURL: s.baseURL(r) + bookingURL(b.ID)}
}

// PUT --------------------------------------------------------------------------

type pushRoomJSON struct {
	PlaceID int64  `json:"place_id"`
	Name    string `json:"name"`
}

type pushBody struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind"`
	StartsOn    string         `json:"starts_on"`
	StartTime   string         `json:"start_time"`
	EndsOn      string         `json:"ends_on"`
	EndTime     string         `json:"end_time"`
	Rooms       []pushRoomJSON `json:"rooms"`
	Visibility  string         `json:"visibility"`
	Description string         `json:"description"`
	URL         string         `json:"url"`
	Status      string         `json:"status"`
	Replaces    int64          `json:"replaces"`
}

// pushError is a 400 with a message per field.
type pushError map[string]string

func (e pushError) write(w http.ResponseWriter) {
	keys := make([]string, 0, len(e))
	for k := range e {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	msgs := make([]string, len(keys))
	for i, k := range keys {
		msgs[i] = k + ": " + e[k]
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": strings.Join(msgs, "; "), "fields": e})
}

// readPush checks and tidies a pushed booking. Only what can't be stored
// is refused; times an app's own form allows but the calendar can't use
// are tidied rather than refused, so the app's send queue doesn't jam:
// an end time with no start time is dropped (all day), and so is a
// same-day end time that isn't after the start (busy to the day's end).
func readPush(p pushBody) (store.Booking, []store.PushRoom, pushError) {
	errs := pushError{}
	trim := strings.TrimSpace
	b := store.Booking{Title: trim(p.Title), Kind: trim(p.Kind), StartsOn: trim(p.StartsOn), StartTime: trim(p.StartTime),
		EndsOn: trim(p.EndsOn), EndTime: trim(p.EndTime), Visibility: trim(p.Visibility), Description: strings.TrimSpace(p.Description),
		URL: trim(p.URL), Status: trim(p.Status)}
	if !validDate(b.StartsOn) {
		errs["starts_on"] = "a date like 2026-11-07 is needed"
	}
	if b.EndsOn != "" && (!validDate(b.EndsOn) || b.EndsOn < b.StartsOn) {
		errs["ends_on"] = "a date like 2026-11-07, not before starts_on, or blank for the same day"
	}
	if b.EndsOn == b.StartsOn {
		b.EndsOn = ""
	}
	for field, t := range map[string]string{"start_time": b.StartTime, "end_time": b.EndTime} {
		if t != "" && !validTime(t) {
			errs[field] = "a time like 18:30, or blank"
		}
	}
	if b.StartTime == "" || (b.EndsOn == "" && b.EndTime != "" && b.EndTime <= b.StartTime) {
		b.EndTime = ""
	}
	switch b.Visibility {
	case "":
		b.Visibility = "private"
	case "public", "private", "locked":
	default:
		errs["visibility"] = "public, private or locked"
	}
	if b.Status == "" {
		b.Status = "confirmed"
	} else if !slices.Contains(store.BookingStatuses, b.Status) {
		errs["status"] = "confirmed, tentative or cancelled"
	}
	if len(b.Title) > 200 {
		errs["title"] = "at most 200 characters"
	}
	if len(b.Kind) > 100 {
		errs["kind"] = "at most 100 characters"
	}
	if len(b.Description) > 10000 {
		errs["description"] = "at most 10,000 characters"
	}
	if b.URL != "" {
		if u, err := url.Parse(b.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(b.URL) > 2000 {
			errs["url"] = "an http or https address, or blank"
		}
	}
	if len(p.Rooms) > 50 {
		errs["rooms"] = "at most 50 rooms"
	}
	var rooms []store.PushRoom
	for _, rm := range p.Rooms {
		name := trim(rm.Name)
		if len(name) > 100 {
			errs["rooms"] = "room names can be at most 100 characters"
		}
		if name != "" || rm.PlaceID > 0 {
			rooms = append(rooms, store.PushRoom{PlaceID: rm.PlaceID, Name: name})
		}
	}
	if len(errs) > 0 {
		return b, nil, errs
	}
	return b, rooms, nil
}

func (s *Server) apiPutBooking(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	var p pushBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPushBody))
	if err := dec.Decode(&p); err != nil {
		apiError(w, http.StatusBadRequest, "send the booking as JSON (see the calendar's PLAN.md, push API)")
		return
	}
	b, rooms, perr := readPush(p)
	if perr != nil {
		perr.write(w)
		return
	}
	b.Source, b.SourceID = tok.Source, r.PathValue("source_id")
	res, err := s.store.PutSourceBooking(b, rooms, p.Replaces)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	saved, err := s.store.GetBooking(res.ID)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	clashes, err := s.store.ConflictsWith(res.ID)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	conflicts := []apiBlockJSON{}
	seen := map[int64]bool{}
	for _, c := range clashes {
		other := c.B
		if other.ID == res.ID {
			other = c.A
		}
		if !seen[other.ID] {
			seen[other.ID] = true
			conflicts = append(conflicts, blockFor(other, tok))
		}
	}
	out := map[string]any{"id": res.ID, "created": res.Created, "booking": s.sourceBookingJSON(r, *saved), "conflicts": conflicts}
	if res.Replaced != 0 {
		out["replaced"] = res.Replaced
	}
	writeJSON(w, http.StatusOK, out)
}

// apiDeleteBooking answers 204 even when the booking was already gone.
func (s *Server) apiDeleteBooking(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	if err := s.store.DeleteSourceBooking(tok.Source, r.PathValue("source_id")); err != nil {
		s.apiServerError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GETs -------------------------------------------------------------------------

// apiSourceBookings lists the app's own bookings, all of them or those
// changed at or after ?updated_since= (a date, or a time in RFC 3339), so
// an app can check it's in step.
func (s *Server) apiSourceBookings(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	since := ""
	if v := strings.TrimSpace(r.URL.Query().Get("updated_since")); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			t, err = time.Parse(store.DateLayout, v)
		}
		if err != nil {
			apiError(w, http.StatusBadRequest, "updated_since must be a date like 2026-11-01 or a time like 2026-11-01T18:30:00Z")
			return
		}
		since = t.UTC().Format(time.DateTime)
	}
	list, err := s.store.SourceBookings(tok.Source, since)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiSourceBookingJSON, len(list))
	for i, b := range list {
		out[i] = s.sourceBookingJSON(r, b)
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": out})
}

// apiAvailability lists what's booked (any source, not cancelled) in the
// rooms named in ?rooms= (comma-separated) from ?from= to ?to=.
func (s *Server) apiAvailability(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	var names []string
	for _, n := range strings.Split(r.URL.Query().Get("rooms"), ",") {
		if n = strings.TrimSpace(n); n != "" && len(names) < 50 {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		apiError(w, http.StatusBadRequest, "name the rooms, like ?rooms=Lifecenter,Kitchen")
		return
	}
	from, to, ok := s.apiRange(w, r, true)
	if !ok {
		return
	}
	list, err := s.store.BusyInRoomsNamed(names, from, to)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiBlockJSON, len(list))
	for i, b := range list {
		out[i] = blockFor(b, tok)
	}
	writeJSON(w, http.StatusOK, map[string]any{"busy": out})
}

type apiRoomJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	PlaceID  int64  `json:"place_id,omitempty"`
	Path     string `json:"path,omitempty"`
	Capacity int    `json:"capacity,omitempty"`
	Bookable bool   `json:"bookable"`
}

// apiRooms lists the calendar's rooms (not archived), with the tracker's
// place IDs. bookable says whether people can ask for it on /request;
// apps may book any of them.
func (s *Server) apiRooms(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	list, err := s.store.ListRooms(false)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiRoomJSON, len(list))
	for i, rm := range list {
		out[i] = apiRoomJSON{ID: rm.ID, Name: rm.Name, PlaceID: rm.PlaceID, Path: rm.TrackerPath, Capacity: rm.Capacity,
			Bookable: rm.Bookable}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}
