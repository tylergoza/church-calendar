package server

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

// The public calendar, moved here from the event planner. Anyone can open
// it; what they see follows store.Access. Signed-in people get the same
// pages with private bookings in full.

func (s *Server) access(r *http.Request) store.Access { return store.AccessFor(currentUser(r)) }

// Source filter groups: the apps that push, and everything made here.
var sourceGroups = []struct{ Value, Label string }{
	{"events", "Events"}, {"productions", "Productions"}, {"other", "Other"},
}

func sourceGroup(source string) string {
	switch source {
	case "events", "productions":
		return source
	}
	return "other"
}

// calItem is one entry on one day.
type calItem struct {
	store.CalendarEntry
	When     string // "2:00–4:00 pm", "All day", "from 5:30 pm"...
	BusyText string // "Lifecenter, Kitchen unavailable · 2:00–4:00 pm"
}

type calDay struct {
	Date    string
	Day     int
	InMonth bool
	Today   bool
	Items   []calItem
}

// calDays lays entries out day by day from first to last (inclusive). A
// booking over several days shows on each of them. keep picks the entries
// to show (nil for all).
func calDays(first, last time.Time, inMonth time.Month, today string, entries []store.CalendarEntry,
	keep func(store.CalendarEntry) bool) []*calDay {
	var days []*calDay
	byDate := map[string]*calDay{}
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		ds := d.Format(store.DateLayout)
		cd := &calDay{Date: ds, Day: d.Day(), InMonth: inMonth == 0 || d.Month() == inMonth, Today: ds == today}
		byDate[ds] = cd
		days = append(days, cd)
	}
	lastDS := last.Format(store.DateLayout)
	for _, e := range entries {
		if keep != nil && !keep(e) {
			continue
		}
		start, err := time.Parse(store.DateLayout, e.StartsOn)
		if err != nil {
			continue
		}
		for d := start; ; d = d.AddDate(0, 0, 1) {
			ds := d.Format(store.DateLayout)
			if ds > e.LastDate() || ds > lastDS {
				break
			}
			if cd := byDate[ds]; cd != nil {
				cd.Items = append(cd.Items, newCalItem(e, ds))
			}
		}
	}
	return days
}

func hasRoom(e store.CalendarEntry, room string) bool {
	for _, r := range e.Rooms {
		if strings.EqualFold(r.Name, room) {
			return true
		}
	}
	return false
}

// handleCalendar is the month: a grid on wide screens, a list of days on
// phones. Filter by room (by name, so places only an app knows work too)
// and by source. Busy blocks have no source, so the source filter leaves
// them out rather than give away where a locked booking came from.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	today := s.today()
	q := r.URL.Query()
	month, err := time.Parse("2006-01", q.Get("month"))
	if err != nil {
		month, _ = time.Parse("2006-01", today[:7])
	}
	room := strings.TrimSpace(q.Get("room"))
	source := q.Get("source")
	if source != "" && sourceGroup(source) != source {
		source = ""
	}
	first := month
	last := first.AddDate(0, 1, -1)
	gridStart := first.AddDate(0, 0, -int(first.Weekday()))
	gridEnd := last.AddDate(0, 0, 6-int(last.Weekday()))

	entries, err := s.store.Calendar(gridStart.Format(store.DateLayout), gridEnd.Format(store.DateLayout), s.access(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Rooms and sources in use this month, for the filters.
	seenRoom, seenSource := map[string]bool{}, map[string]bool{}
	var rooms []string
	for _, e := range entries {
		for _, rm := range e.Rooms {
			if k := strings.ToLower(rm.Name); !seenRoom[k] {
				seenRoom[k] = true
				rooms = append(rooms, rm.Name)
			}
		}
		if !e.Busy {
			seenSource[sourceGroup(e.Source)] = true
		}
	}
	sort.Slice(rooms, func(i, j int) bool { return strings.ToLower(rooms[i]) < strings.ToLower(rooms[j]) })
	var sources []struct{ Value, Label string }
	for _, g := range sourceGroups {
		if seenSource[g.Value] || g.Value == source {
			sources = append(sources, g)
		}
	}

	days := calDays(gridStart, gridEnd, first.Month(), today, entries, func(e store.CalendarEntry) bool {
		if room != "" && !hasRoom(e, room) {
			return false
		}
		return source == "" || (!e.Busy && sourceGroup(e.Source) == source)
	})
	var grid [][]*calDay
	var agenda []*calDay
	for i, d := range days {
		if i%7 == 0 {
			grid = append(grid, nil)
		}
		grid[len(grid)-1] = append(grid[len(grid)-1], d)
		if d.InMonth && len(d.Items) > 0 {
			agenda = append(agenda, d)
		}
	}

	link := func(m time.Time) string {
		v := url.Values{"month": {m.Format("2006-01")}}
		if room != "" {
			v.Set("room", room)
		}
		if source != "" {
			v.Set("source", source)
		}
		return "/calendar?" + v.Encode()
	}
	base := s.baseURL(r)
	s.render(w, r, http.StatusOK, "calendar/index", map[string]any{
		"Title":        first.Format("January 2006"),
		"PublicHeader": currentUser(r) == nil,
		"Month":        first.Format("2006-01"),
		"Heading":      first.Format("January 2006"),
		"Grid":         grid,
		"Agenda":       agenda,
		"Rooms":        rooms,
		"Room":         room,
		"Sources":      sources,
		"Source":       source,
		"Filtered":     room != "" || source != "",
		"PrevURL":      link(first.AddDate(0, -1, 0)),
		"NextURL":      link(first.AddDate(0, 1, 0)),
		"IsToday":      today[:7] == first.Format("2006-01"),
		"Feed":         base + "/calendar.ics",
		// html/template blanks webcal: links unless marked safe; it always
		// starts webcal:// so it can't be a script link.
		"WebcalURL": template.URL("webcal://" + strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://") + "/calendar.ics"),
		"Weekdays":  []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	})
}

// handleBookingBySource sends the apps' old calendar links to the booking
// they pushed.
func (s *Server) handleBookingBySource(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.BookingBySource(r.PathValue("source"), r.PathValue("source_id"))
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, bookingURL(id), http.StatusFound)
}

// Text ---------------------------------------------------------------------

// clock turns "17:30" into "5:30 pm".
func clock(hm string) string {
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return hm
	}
	return strings.ToLower(t.Format("3:04 pm"))
}

// timeRange is "2:00–4:00 pm", "11:00 am–1:00 pm", "5:30 pm" or "".
func timeRange(start, end string) string {
	if start == "" {
		return ""
	}
	if end == "" || end <= start {
		return clock(start)
	}
	a, b := clock(start), clock(end)
	if strings.HasSuffix(a, b[len(b)-2:]) {
		a = strings.TrimSuffix(a, " "+a[len(a)-2:])
	}
	return a + "–" + b
}

// dayWhen is the time to show for entry e on the day ds: a booking over
// several days starts, continues and ends across them.
func dayWhen(e store.CalendarEntry, ds string) string {
	if e.StartTime == "" {
		return "All day"
	}
	if e.EndsOn == "" || e.EndsOn == e.StartsOn {
		return timeRange(e.StartTime, e.EndTime)
	}
	switch ds {
	case e.StartsOn:
		return "from " + clock(e.StartTime)
	case e.EndsOn:
		if e.EndTime != "" {
			return "until " + clock(e.EndTime)
		}
	}
	return "All day"
}

func newCalItem(e store.CalendarEntry, ds string) calItem {
	it := calItem{CalendarEntry: e, When: dayWhen(e, ds)}
	if e.Busy {
		who := "Busy"
		if names := e.RoomNames(); names != "" {
			who = names + " unavailable"
		}
		if it.When == "All day" {
			it.BusyText = who + " · all day"
		} else {
			it.BusyText = who + " · " + it.When
		}
	}
	return it
}

// weekOf is the Sunday starting the week of the date in ?week=, or of today.
func (s *Server) weekOf(r *http.Request) time.Time {
	d, err := time.Parse(store.DateLayout, r.URL.Query().Get("week"))
	if err != nil {
		d = s.todayTime()
	}
	return d.AddDate(0, 0, -int(d.Weekday()))
}

// thisWeek is the Sunday starting this week, YYYY-MM-DD.
func (s *Server) thisWeek() string {
	d := s.todayTime()
	return d.AddDate(0, 0, -int(d.Weekday())).Format(store.DateLayout)
}

func weekLabel(start time.Time) string {
	end := start.AddDate(0, 0, 6)
	if start.Month() == end.Month() {
		return fmt.Sprintf("%s %d–%d, %d", start.Format("January"), start.Day(), end.Day(), end.Year())
	}
	if start.Year() == end.Year() {
		return fmt.Sprintf("%s – %s, %d", start.Format("Jan 2"), end.Format("Jan 2"), end.Year())
	}
	return start.Format("Jan 2, 2006") + " – " + end.Format("Jan 2, 2006")
}
