package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tylergoza/church-calendar/internal/store"
)

// The read API lets other sites and apps (the church website, a phone's
// calendar) read the calendar, in the same shape as the event planner's
// so the website only has to change the address and token. It takes a
// read token made on /admin/tokens, sent as "Authorization: Bearer cal_…".
// What a token sees follows store.Access: public bookings in full, private
// ones too when the token may see them, and locked ones only as busy.
//
// The push API, for apps sending their own bookings, is in api_push.go.
//
// These routes ignore sessions and cookies (see isCookieFree), so they
// need no CSRF protection and never set a cookie.

const (
	maxAPIRangeDays     = 400
	defaultAPIRangeDays = 90
)

func (s *Server) apiRoutes(mux *http.ServeMux) {
	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.apiHandler(h)) }
	api("GET /api/v1/events", s.requireReadToken(false, s.apiEvents))
	api("GET /api/v1/events/{id}", s.requireReadToken(false, s.apiEvent))
	api("GET /api/v1/events.ics", s.requireReadToken(true, s.apiICS))
	api("GET /calendar.ics", s.publicICS)
	s.pushRoutes(api)
	api("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	api("OPTIONS /calendar.ics", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	api("/api/", func(w http.ResponseWriter, r *http.Request) { apiError(w, http.StatusNotFound, "no such endpoint") })
}

// apiHandler adds CORS headers: anyone may read with GET.
func (s *Server) apiHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Max-Age", "86400")
		next(w, r)
	}
}

type tokenHandler func(w http.ResponseWriter, r *http.Request, tok *store.Token)

// requireReadToken checks the bearer token is a read token. allowQuery
// also takes ?token=, for the .ics feed: calendar apps subscribe by URL
// and can't send headers.
func (s *Server) requireReadToken(allowQuery bool, next tokenHandler) http.HandlerFunc {
	return s.requireToken(store.TokenRead, allowQuery, next)
}

// requireToken checks the bearer token is of the kind given ("" for any).
func (s *Server) requireToken(kind string, allowQuery bool, next tokenHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok && allowQuery {
			raw = r.URL.Query().Get("token")
			ok = raw != ""
		}
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
			apiError(w, http.StatusUnauthorized, "send an API token as: Authorization: Bearer <token>")
			return
		}
		tok, err := s.store.AuthenticateToken(strings.TrimSpace(raw))
		if errors.Is(err, store.ErrNotFound) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="api", error="invalid_token"`)
			apiError(w, http.StatusUnauthorized, "that API token isn't valid; it may have been revoked")
			return
		} else if err != nil {
			s.apiServerError(w, r, err)
			return
		}
		switch {
		case kind == store.TokenRead && tok.Kind != store.TokenRead:
			apiError(w, http.StatusForbidden, "that's an app's token; reading the calendar needs a read token")
			return
		case kind == store.TokenSource && tok.Kind != store.TokenSource:
			apiError(w, http.StatusForbidden, "that's a read token; sending bookings needs an app's token")
			return
		}
		next(w, r, tok)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) apiServerError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	s.log.Error("api", "path", r.URL.Path, "err", err)
	apiError(w, http.StatusInternalServerError, "something went wrong")
}

// JSON ---------------------------------------------------------------------

// apiEventJSON is the event planner's shape, plus source.
type apiEventJSON struct {
	ID          int64    `json:"id,omitempty"` // omitted when busy
	Busy        bool     `json:"busy"`
	Source      string   `json:"source,omitempty"` // omitted when busy
	Title       string   `json:"title,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Status      string   `json:"status,omitempty"`
	StartsOn    string   `json:"starts_on"`
	StartTime   string   `json:"start_time"`
	EndsOn      string   `json:"ends_on"`
	EndTime     string   `json:"end_time"`
	Rooms       []string `json:"rooms"`
	Description string   `json:"description,omitempty"`
	Public      bool     `json:"public"`
	URL         string   `json:"url,omitempty"` // omitted when busy
}

func roomNames(rooms []store.BookingRoom) []string {
	out := make([]string, len(rooms))
	for i, r := range rooms {
		out[i] = r.Name
	}
	return out
}

func (s *Server) apiEventJSON(r *http.Request, e store.CalendarEntry) apiEventJSON {
	j := apiEventJSON{Busy: e.Busy, StartsOn: e.StartsOn, StartTime: e.StartTime, EndsOn: e.EndsOn,
		EndTime: e.EndTime, Rooms: roomNames(e.Rooms)}
	if !e.Busy {
		j.ID, j.Source, j.Title, j.Kind, j.Status = e.ID, e.Source, e.Title, e.Kind, e.Status
		j.Description, j.Public = e.Description, e.Visibility == "public"
		j.URL = s.baseURL(r) + bookingURL(e.ID)
	}
	return j
}

// apiRange reads from and to (default: today, and 90 days later). It
// writes the error and returns false when they are unusable.
func (s *Server) apiRange(w http.ResponseWriter, r *http.Request, asJSON bool) (from, to string, ok bool) {
	fail := func(msg string) (string, string, bool) {
		if asJSON {
			apiError(w, http.StatusBadRequest, msg)
		} else {
			http.Error(w, msg, http.StatusBadRequest)
		}
		return "", "", false
	}
	q := r.URL.Query()
	from = q.Get("from")
	if from == "" {
		from = s.today()
	}
	f, err := time.Parse(store.DateLayout, from)
	if err != nil {
		return fail("from must be a date like 2026-11-01")
	}
	to = q.Get("to")
	if to == "" {
		to = f.AddDate(0, 0, defaultAPIRangeDays).Format(store.DateLayout)
	}
	t, err := time.Parse(store.DateLayout, to)
	if err != nil {
		return fail("to must be a date like 2026-11-01")
	}
	if t.Before(f) {
		return fail("to can't be before from")
	}
	if t.Sub(f) > maxAPIRangeDays*24*time.Hour {
		return fail(fmt.Sprintf("the range can be at most %d days", maxAPIRangeDays))
	}
	return from, to, true
}

// calendarFor lists the range, keeping only bookings in ?room= when given.
func (s *Server) calendarFor(r *http.Request, from, to string, a store.Access) ([]store.CalendarEntry, error) {
	list, err := s.store.Calendar(from, to, a)
	room := strings.TrimSpace(r.URL.Query().Get("room"))
	if err != nil || room == "" {
		return list, err
	}
	var out []store.CalendarEntry
	for _, e := range list {
		if hasRoom(e, room) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	from, to, ok := s.apiRange(w, r, true)
	if !ok {
		return
	}
	list, err := s.calendarFor(r, from, to, tok.Access())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	out := make([]apiEventJSON, 0, len(list))
	for _, e := range list {
		out = append(out, s.apiEventJSON(r, e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// apiEvent gives 404 for a booking the token can't see in full, so the
// answer doesn't reveal that it exists.
func (s *Server) apiEvent(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	b, err := s.store.CalendarBooking(pathID(r), tok.Access())
	if err != nil {
		s.apiServerError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.apiEventJSON(r, store.CalendarEntry{Booking: *b}))
}

// iCalendar ------------------------------------------------------------------

func (s *Server) apiICS(w http.ResponseWriter, r *http.Request, tok *store.Token) {
	s.writeICS(w, r, tok.Access())
}

// publicICS is the no-token feed: what a visitor sees. Calendar apps poll
// it, so it may be cached for five minutes.
func (s *Server) publicICS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	s.writeICS(w, r, store.AccessPublic)
}

func (s *Server) writeICS(w http.ResponseWriter, r *http.Request, a store.Access) {
	from, to, ok := s.apiRange(w, r, false)
	if !ok {
		return
	}
	list, err := s.calendarFor(r, from, to, a)
	if err != nil {
		s.log.Error("ics", "err", err)
		http.Error(w, "something went wrong", http.StatusInternalServerError)
		return
	}
	base := s.baseURL(r)
	host := base[strings.Index(base, "://")+3:]
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Write([]byte(buildICS(s.SiteName(), host, base, list, time.Now())))
}

// buildICS renders entries as an iCalendar feed. Timed bookings are written
// in UTC converted from the server's local time zone.
func buildICS(calName, host, baseURL string, list []store.CalendarEntry, now time.Time) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(foldICS(s)) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//Church Calendar//EN")
	line("CALSCALE:GREGORIAN")
	line("X-WR-CALNAME:" + escapeICS(calName))
	stamp := now.UTC().Format("20060102T150405Z")
	for _, e := range list {
		line("BEGIN:VEVENT")
		if e.Busy {
			sum := sha256.Sum256([]byte(strings.Join([]string{e.StartsOn, e.StartTime, e.EndsOn, e.EndTime,
				strings.Join(roomNames(e.Rooms), "\x00")}, "|")))
			line("UID:busy-" + hex.EncodeToString(sum[:8]) + "@" + host)
		} else {
			line("UID:booking-" + strconv.FormatInt(e.ID, 10) + "@" + host)
		}
		line("DTSTAMP:" + stamp)
		icsTimes(line, e)
		if e.Busy {
			if names := e.RoomNames(); names != "" {
				line("SUMMARY:" + escapeICS("Busy: "+names))
			} else {
				line("SUMMARY:Busy")
			}
			line("TRANSP:OPAQUE")
		} else {
			line("SUMMARY:" + escapeICS(e.Title))
			if names := e.RoomNames(); names != "" {
				line("LOCATION:" + escapeICS(names))
			}
			if e.Description != "" {
				line("DESCRIPTION:" + escapeICS(e.Description))
			}
			line("URL:" + baseURL + bookingURL(e.ID))
		}
		if e.NotYet() {
			line("STATUS:TENTATIVE")
		}
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

func icsTimes(line func(string), e store.CalendarEntry) {
	const day = "20060102"
	start, err := time.ParseInLocation(store.DateLayout, e.StartsOn, time.Local)
	if err != nil {
		return
	}
	last, err := time.ParseInLocation(store.DateLayout, e.LastDate(), time.Local)
	if err != nil || last.Before(start) {
		last = start
	}
	allDay := func() {
		line("DTSTART;VALUE=DATE:" + start.Format(day))
		line("DTEND;VALUE=DATE:" + last.AddDate(0, 0, 1).Format(day)) // exclusive
	}
	if e.StartTime == "" {
		allDay()
		return
	}
	const utc = "20060102T150405Z"
	st, err := time.ParseInLocation(store.DateLayout+" 15:04", e.StartsOn+" "+e.StartTime, time.Local)
	if err != nil {
		allDay()
		return
	}
	line("DTSTART:" + st.UTC().Format(utc))
	if e.EndTime != "" {
		if en, err := time.ParseInLocation(store.DateLayout+" 15:04", e.LastDate()+" "+e.EndTime, time.Local); err == nil && en.After(st) {
			line("DTEND:" + en.UTC().Format(utc))
		}
	}
}

// escapeICS escapes a TEXT value: backslash, comma, semicolon, newline.
func escapeICS(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`, `;`, `\;`, "\n", `\n`).Replace(s)
}

// foldICS ends the line with CRLF, folding it so no line is over 75 octets
// (continuations start with a space) and never splitting a character.
func foldICS(s string) string {
	var b strings.Builder
	limit := 75
	n := 0
	for _, r := range s {
		size := utf8.RuneLen(r)
		if n+size > limit {
			b.WriteString("\r\n ")
			n, limit = 0, 74
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}
