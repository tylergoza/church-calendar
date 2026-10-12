package server

import (
	"net/http"

	"github.com/tylergoza/church-calendar/internal/store"
)

// homeWeeks is how far ahead the front page lists bookings.
const homeWeeks = 4

// agendaDay is one day of the front page's list.
type agendaDay struct {
	Date     string
	Today    bool
	Bookings []store.Booking
}

// handleHome is the front page: the next few weeks, day by day, with the
// visibility rules applied in the template. The month grid replaces it with
// the public calendar (PLAN.md step 3). A booking over several days shows
// on each of them.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	today := s.todayTime()
	from, to := today.Format(store.DateLayout), today.AddDate(0, 0, homeWeeks*7-1).Format(store.DateLayout)
	list, err := s.store.BookingsBetween(from, to, 0)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var days []agendaDay
	for d := today; d.Format(store.DateLayout) <= to; d = d.AddDate(0, 0, 1) {
		ds := d.Format(store.DateLayout)
		day := agendaDay{Date: ds, Today: ds == from}
		for _, b := range list {
			if b.Status != "cancelled" && b.StartsOn <= ds && b.LastDate() >= ds {
				day.Bookings = append(day.Bookings, b)
			}
		}
		if len(day.Bookings) > 0 {
			days = append(days, day)
		}
	}
	s.render(w, r, http.StatusOK, "home", map[string]any{
		"PublicHeader": currentUser(r) == nil, "Days": days, "Weeks": homeWeeks,
	})
}
