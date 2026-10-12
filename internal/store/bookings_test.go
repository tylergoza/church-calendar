package store

import (
	"errors"
	"testing"
	"time"

	"github.com/tylergoza/church-calendar/internal/recur"
)

func addRoom(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	id, err := st.CreateRoom(&Room{Name: name, Bookable: true, NeedsApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func addBooking(t *testing.T, st *Store, b Booking, rooms ...int64) int64 {
	t.Helper()
	if b.Status == "" {
		b.Status = "confirmed"
	}
	if b.Title == "" {
		b.Title = "Booking"
	}
	id, err := st.CreateBooking(&b, rooms)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRooms(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Lifecenter")
	if _, err := st.CreateRoom(&Room{Name: "lifecenter"}); !errors.Is(err, ErrRoomNameTaken) {
		t.Errorf("duplicate name (any case): %v", err)
	}
	b := addBooking(t, st, Booking{StartsOn: "2026-11-01"}, hall)
	r, _ := st.GetRoom(hall)
	r.Name, r.Archived, r.Capacity = "Life Center", true, 200
	if err := st.UpdateRoom(r); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetBooking(b); got.RoomNames() != "Life Center" {
		t.Errorf("booking room name = %q, want it renamed too", got.RoomNames())
	}
	if list, _ := st.ListRooms(false); len(list) != 0 {
		t.Error("archived rooms should be hidden")
	}
	if list, _ := st.ListRooms(true); len(list) != 1 || list[0].Capacity != 200 {
		t.Errorf("ListRooms(true) = %+v", list)
	}
}

// One test per overlap rule, each in a fresh database.
func TestConflictRules(t *testing.T) {
	cases := []struct {
		name string
		a, b Booking
		want bool
	}{
		{"timed overlap", Booking{StartsOn: "2026-11-07", StartTime: "17:00", EndTime: "19:00"},
			Booking{StartsOn: "2026-11-07", StartTime: "18:30", EndTime: "20:00"}, true},
		{"back to back is fine", Booking{StartsOn: "2026-11-07", StartTime: "17:00", EndTime: "19:00"},
			Booking{StartsOn: "2026-11-07", StartTime: "19:00", EndTime: "20:00"}, false},
		{"different days", Booking{StartsOn: "2026-11-07", StartTime: "17:00", EndTime: "19:00"},
			Booking{StartsOn: "2026-11-08", StartTime: "17:00", EndTime: "19:00"}, false},
		{"all day against timed", Booking{StartsOn: "2026-11-07"},
			Booking{StartsOn: "2026-11-07", StartTime: "09:00", EndTime: "10:00"}, true},
		{"all day against the next day", Booking{StartsOn: "2026-11-07"},
			Booking{StartsOn: "2026-11-08", StartTime: "00:00", EndTime: "01:00"}, false},
		{"multi-day covers a middle day", Booking{StartsOn: "2026-11-06", EndsOn: "2026-11-09"},
			Booking{StartsOn: "2026-11-08", StartTime: "10:00", EndTime: "11:00"}, true},
		{"multi-day timed ends before", Booking{StartsOn: "2026-11-06", StartTime: "18:00", EndsOn: "2026-11-08", EndTime: "12:00"},
			Booking{StartsOn: "2026-11-08", StartTime: "13:00", EndTime: "14:00"}, false},
		{"multi-day timed starts after", Booking{StartsOn: "2026-11-06", StartTime: "18:00", EndsOn: "2026-11-08", EndTime: "12:00"},
			Booking{StartsOn: "2026-11-06", StartTime: "09:00", EndTime: "17:00"}, false},
		{"no end time runs to the end of the day", Booking{StartsOn: "2026-11-07", StartTime: "18:00"},
			Booking{StartsOn: "2026-11-07", StartTime: "21:00", EndTime: "22:00"}, true},
		{"cancelled ignored", Booking{StartsOn: "2026-11-07", Status: "cancelled"},
			Booking{StartsOn: "2026-11-07"}, false},
		{"tentative counts", Booking{StartsOn: "2026-11-07", Status: "tentative"},
			Booking{StartsOn: "2026-11-07"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := openTest(t)
			hall := addRoom(t, st, "Lifecenter")
			a := addBooking(t, st, c.a, hall)
			addBooking(t, st, c.b, hall)
			got, err := st.ConflictsWith(a)
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) > 0) != c.want {
				t.Errorf("conflict = %v, want %v", len(got) > 0, c.want)
			}
		})
	}
}

func TestConflictRooms(t *testing.T) {
	st := openTest(t)
	hall, kitchen := addRoom(t, st, "Lifecenter"), addRoom(t, st, "Kitchen")
	day := Booking{StartsOn: "2026-11-07"}
	a := addBooking(t, st, day, hall, kitchen)
	b := addBooking(t, st, day, kitchen)
	addBooking(t, st, day) // no rooms
	// A place the calendar doesn't know, as an app would send it.
	c := addBooking(t, st, day)
	st.DB.Exec(`INSERT INTO booking_rooms (booking_id, room_id, room_name) VALUES (?, NULL, 'Pavilion')`, c)
	d := addBooking(t, st, day)
	st.DB.Exec(`INSERT INTO booking_rooms (booking_id, room_id, room_name) VALUES (?, NULL, 'Pavilion')`, d)

	list, err := st.Conflicts("2026-11-01", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].A.ID != a || list[0].B.ID != b || list[0].Room.Name != "Kitchen" {
		t.Fatalf("conflicts = %+v, want only a and b in the Kitchen (unknown rooms aren't checked)", list)
	}
	if list[0].A.RoomNames() != "Kitchen, Lifecenter" {
		t.Errorf("rooms loaded = %q", list[0].A.RoomNames())
	}
	// Ended before from: left out.
	if list, _ := st.Conflicts("2026-11-08", false); len(list) != 0 {
		t.Error("past clashes should be left out")
	}
	// That's fine hides it, unless asked for.
	if err := st.SetConflictOK(b, a, true, 0); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 0 {
		t.Error("marked pair should be hidden")
	}
	if list, _ := st.ConflictsWith(a); len(list) != 0 {
		t.Error("marked pair should be hidden for the booking too")
	}
	if list, _ := st.Conflicts("2026-11-01", true); len(list) != 1 || !list[0].OK {
		t.Error("marked pair should show with withOK")
	}
	st.SetConflictOK(a, b, false, 0)
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 1 {
		t.Error("unmarking should bring it back")
	}
	if st.SetConflictOK(a, a, true, 0) == nil {
		t.Error("a booking can't clash with itself")
	}
	if err := st.SetConflictOK(a, 999, true, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown booking: %v", err)
	}
}

func weekly(start string) recur.Rule {
	d, _ := recur.ParseDate(start)
	return recur.Rule{Freq: recur.Weekly, Every: 1, Weekdays: []time.Weekday{d.Weekday()}}
}

func TestSeries(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Lifecenter")
	// Sunday 2026-11-01, every week for 4 times.
	first := addBooking(t, st, Booking{Title: "Choir", StartsOn: "2026-11-01", StartTime: "18:00", EndTime: "20:00", Visibility: "public"}, hall)
	rule := weekly("2026-11-01")
	rule.Count = 4
	sid, err := st.MakeSeries(first, rule, "2026-10-12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MakeSeries(first, rule, "2026-10-12"); !errors.Is(err, ErrAlreadyInSeries) {
		t.Errorf("second MakeSeries: %v", err)
	}
	occ, _ := st.Occurrences(sid)
	if len(occ) != 4 || occ[3].StartsOn != "2026-11-22" || occ[3].Title != "Choir" || occ[3].RoomNames() != "Lifecenter" || occ[3].Visibility != "public" {
		t.Fatalf("occurrences = %+v", occ)
	}
	// Topping up again makes nothing new.
	if n, _ := st.ExtendSeries("2026-10-13"); n != 0 {
		t.Errorf("ExtendSeries made %d more", n)
	}

	// This and later: the 2nd date's change reaches the 3rd and 4th, not the 1st.
	title, rooms := "Choir practice", []int64{}
	if n, err := st.ApplyForward(occ[1].ID, BookingChange{Title: &title, Rooms: &rooms}); err != nil || n != 3 {
		t.Fatalf("ApplyForward = %d, %v", n, err)
	}
	occ, _ = st.Occurrences(sid)
	if occ[0].Title != "Choir" || occ[0].RoomNames() != "Lifecenter" || occ[2].Title != "Choir practice" || len(occ[2].Rooms) != 0 {
		t.Errorf("after ApplyForward: %+v", occ)
	}
	if sr, _ := st.GetSeries(sid); sr.TemplateBookingID != occ[1].ID {
		t.Error("the changed date should become the template")
	}

	// Deleting one date skips it for good.
	if err := st.DeleteBooking(occ[2].ID); err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(`UPDATE series SET generated_through = '' WHERE id = ?`, sid)
	st.ExtendSeries("2026-10-12")
	if occ, _ = st.Occurrences(sid); len(occ) != 3 {
		t.Errorf("deleted date came back: %d dates", len(occ))
	}

	// End from the 2nd date: only the 1st is left.
	if n, err := st.EndSeriesFrom(sid, occ[1].OccurrenceDate); err != nil || n != 2 {
		t.Fatalf("EndSeriesFrom = %d, %v", n, err)
	}
	if occ, _ = st.Occurrences(sid); len(occ) != 1 || occ[0].ID != first {
		t.Errorf("after ending: %+v", occ)
	}
	// Deleting the last date takes the series with it.
	st.DeleteBooking(first)
	if _, err := st.GetSeries(sid); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty series should be gone: %v", err)
	}
}

func TestSeriesMultiDayAndHorizon(t *testing.T) {
	st := openTest(t)
	// A Friday-to-Sunday retreat, monthly by date, no end.
	id := addBooking(t, st, Booking{StartsOn: "2026-11-06", EndsOn: "2026-11-08", Status: "cancelled"})
	sid, err := st.MakeSeries(id, recur.Rule{Freq: recur.Monthly, Every: 1, MonthDay: 6}, "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	occ, _ := st.Occurrences(sid)
	// Nov through Apr: May 6 is past the horizon, six months on from Nov 1.
	if len(occ) != 6 || occ[1].StartsOn != "2026-12-06" || occ[1].EndsOn != "2026-12-08" {
		t.Fatalf("occurrences = %d, second %s–%s", len(occ), occ[1].StartsOn, occ[1].EndsOn)
	}
	if occ[1].Status != "confirmed" {
		t.Error("a cancelled template should still make confirmed dates")
	}
	// A month on, one more date.
	if n, _ := st.ExtendSeries("2026-12-01"); n != 1 {
		t.Errorf("top-up made %d, want 1", n)
	}
}
