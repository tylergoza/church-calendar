package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/church-calendar/internal/store"
	"github.com/tylergoza/church-calendar/internal/umtoken"
)

// The per-person read API, /api/v1/me/…, for the church MCP server (and
// later the website's members area). The gate (token check, local user)
// is me_auth.go; the shapes are APP_CONTRACTS.md in ../mcp_server.
//
// Everything here is what the person sees on the web pages: public and
// private bookings in full, locked ones as busy blocks (rooms and times
// only), whatever their role. No equipment and no phone numbers.

// meMaxItems bounds every list (the contract's cap).
const meMaxItems = 100

func (s *Server) meRoutes(mux *http.ServeMux) {
	s.meRoute(mux, "GET /api/v1/me/calendar", s.meCalendar)
	s.meRoute(mux, "GET /api/v1/me/calendar/{id}", s.meEvent)
	s.meRoute(mux, "GET /api/v1/me/rooms", s.meRooms)
	s.meRoute(mux, "GET /api/v1/me/availability", s.meAvailability)
	s.meRoute(mux, "GET /api/v1/me/bookings", s.meBookings)
	s.meCatchAll(mux)
}

// meUMConfig is the live single sign-on connection; the /me API is off
// without it.
func (s *Server) meUMConfig() (umtoken.Config, bool) {
	c := s.SSO()
	if !c.Enabled() || c.ClientID == "" {
		return umtoken.Config{}, false
	}
	return umtoken.Config{URL: c.InternalURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret}, true
}

// meLocalUser is what the SSO callback and grant checks do (sso.go).
func (s *Server) meLocalUser(u umtoken.User) (*store.User, error) {
	return s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, store.RoleFromSSO(u.Role))
}

// meLimit is ?limit= within 1..meMaxItems, meMaxItems when not given.
func meLimit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 || n > meMaxItems {
		return meMaxItems
	}
	return n
}

// meEventJSON is apiEventJSON without equipment (never on /me lists).
func (s *Server) meEventJSON(r *http.Request, e store.CalendarEntry) apiEventJSON {
	j := s.apiEventJSON(r, e)
	j.Equipment = nil
	return j
}

func (s *Server) meCalendar(w http.ResponseWriter, r *http.Request) {
	from, to, ok := s.apiRange(w, r, true)
	if !ok {
		return
	}
	list, err := s.calendarFor(r, from, to, store.AccessFor(meUser(r)))
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiEventJSON, 0, len(list))
	for _, e := range list {
		if len(out) == meLimit(r) {
			break
		}
		out = append(out, s.meEventJSON(r, e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// meEvent is 404 (JSON) for a booking the person can't see in full, busy
// ones included, so the answer doesn't give away that it's there.
func (s *Server) meEvent(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.CalendarBooking(pathID(r), store.AccessFor(meUser(r)))
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.meEventJSON(r, store.CalendarEntry{Booking: *b}))
}

type meRoomJSON struct {
	Name     string `json:"name"`
	Capacity int    `json:"capacity,omitempty"`
	// Whole marks the building itself, listed first among its rooms when
	// it can be booked whole as well.
	Whole bool `json:"whole,omitempty"`
}

type meBuildingJSON struct {
	Name     string       `json:"name"`
	Capacity int          `json:"capacity,omitempty"`
	Rooms    []meRoomJSON `json:"rooms"`
}

// meRooms is what the request form offers, by building. A room in no
// building, or a bookable building with no bookable rooms, is a building
// with rooms [] (bookable whole).
func (s *Server) meRooms(w http.ResponseWriter, r *http.Request) {
	bookable, err := s.bookableRooms()
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	all, err := s.store.ListRooms(false)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	loose, groups := groupRooms(all, bookable)
	out := make([]meBuildingJSON, 0, len(loose)+len(groups))
	for _, g := range groups {
		b := meBuildingJSON{Name: g.Name, Rooms: []meRoomJSON{}}
		if g.Whole != nil {
			b.Capacity = g.Whole.Capacity
			b.Rooms = append(b.Rooms, meRoomJSON{Name: g.Whole.Name, Capacity: g.Whole.Capacity, Whole: true})
		}
		for _, rm := range g.Rooms {
			b.Rooms = append(b.Rooms, meRoomJSON{Name: rm.Name, Capacity: rm.Capacity})
		}
		out = append(out, b)
	}
	for _, rm := range loose {
		out = append(out, meBuildingJSON{Name: rm.Name, Capacity: rm.Capacity, Rooms: []meRoomJSON{}})
	}
	slices.SortStableFunc(out, func(a, b meBuildingJSON) int {
		return compareStrings(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	writeJSON(w, http.StatusOK, map[string]any{"buildings": out})
}

type meBlockJSON struct {
	StartsOn  string   `json:"starts_on"`
	StartTime string   `json:"start_time"`
	EndsOn    string   `json:"ends_on"`
	EndTime   string   `json:"end_time"`
	Rooms     []string `json:"rooms"`
	Status    string   `json:"status"`
}

// meAvailability is busy times only (never titles, whatever the role) in
// the rooms asked for, or every bookable room. A room's family counts
// (its building, the rooms in it), as on the request form.
func (s *Server) meAvailability(w http.ResponseWriter, r *http.Request) {
	from, to, ok := s.apiRange(w, r, true)
	if !ok {
		return
	}
	bookable, err := s.bookableRooms()
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	byName := map[string]string{}
	var names []string
	for _, rm := range bookable {
		byName[strings.ToLower(rm.Name)] = rm.Name
		names = append(names, rm.Name)
	}
	if asked := r.URL.Query().Get("rooms"); strings.TrimSpace(asked) != "" {
		names = nil
		for _, n := range strings.Split(asked, ",") {
			if n = strings.TrimSpace(n); n == "" {
				continue
			}
			name, ok := byName[strings.ToLower(n)]
			if !ok {
				apiError(w, http.StatusBadRequest, "no bookable room is called "+strconv.Quote(n)+"; see the rooms list")
				return
			}
			names = append(names, name)
		}
	}
	list, err := s.store.BusyInRoomsNamed(names, from, to)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]meBlockJSON, 0, len(list))
	for _, b := range list {
		if len(out) == meLimit(r) {
			break
		}
		out = append(out, meBlockJSON{StartsOn: b.StartsOn, StartTime: b.StartTime, EndsOn: b.EndsOn,
			EndTime: b.EndTime, Rooms: roomNames(b.Rooms), Status: b.Status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"busy": out})
}

type meBookingJSON struct {
	ID int64 `json:"id"`
	// Type says which ID this is: "request" (a room request, /request/{id}
	// on the web) or "booking" (one the person added by hand as a
	// scheduler, /calendar/{id}).
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	StartsOn  string   `json:"starts_on"`
	StartTime string   `json:"start_time"`
	EndsOn    string   `json:"ends_on"`
	EndTime   string   `json:"end_time"`
	Rooms     []string `json:"rooms"`
	CanChange bool     `json:"can_change"`
	TakenOver bool     `json:"taken_over"`
	upcoming  bool
}

// meBookings is the My bookings page as JSON: the person's requests and
// the bookings they added, upcoming first (soonest at the top), then the
// rest newest first.
func (s *Server) meBookings(w http.ResponseWriter, r *http.Request) {
	u := meUser(r)
	today := s.today()
	reqs, err := s.store.RequestsBy(u.ID)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	var out []meBookingJSON
	for _, q := range reqs {
		b, taken, err := s.takenOver(&q)
		if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		last := q.EndsOn
		if last == "" {
			last = q.StartsOn
		}
		status := q.Status
		if status == "approved" && b != nil {
			status = b.Status // confirmed, or pending/tentative as the page's badge shows
		}
		out = append(out, meBookingJSON{ID: q.ID, Type: "request", Title: q.Title, Status: status,
			StartsOn: q.StartsOn, StartTime: q.StartTime, EndsOn: q.EndsOn, EndTime: q.EndTime,
			Rooms: roomNames(q.Rooms), CanChange: s.canChange(&q, taken), TakenOver: taken,
			upcoming: last >= today && q.Status != "declined" && q.Status != "withdrawn" && q.Status != "cancelled"})
	}
	made, err := s.store.BookingsOf(u.ID)
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	for _, b := range made {
		out = append(out, meBookingJSON{ID: b.ID, Type: "booking", Title: b.Title, Status: b.Status,
			StartsOn: b.StartsOn, StartTime: b.StartTime, EndsOn: b.EndsOn, EndTime: b.EndTime,
			Rooms: roomNames(b.Rooms), upcoming: b.LastDate() >= today && !b.Cancelled()})
	}
	slices.SortStableFunc(out, func(a, b meBookingJSON) int {
		switch {
		case a.upcoming && !b.upcoming:
			return -1
		case !a.upcoming && b.upcoming:
			return 1
		case a.upcoming:
			return compareStrings(a.StartsOn+a.StartTime, b.StartsOn+b.StartTime)
		}
		return compareStrings(b.StartsOn+b.StartTime, a.StartsOn+a.StartTime)
	})
	if len(out) > meMaxItems {
		out = out[:meMaxItems]
	}
	if out == nil {
		out = []meBookingJSON{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookings": out})
}
