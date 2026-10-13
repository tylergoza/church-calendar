package store

import (
	"errors"
	"testing"
)

func addRequest(t *testing.T, st *Store, rooms ...int64) *Request {
	t.Helper()
	r := &Request{Name: "Jo", Contact: "jo@example.org", Title: "Movie night", StartsOn: "2026-11-07", StartTime: "18:00", EndTime: "21:00"}
	if err := st.CreateRequest(r, rooms); err != nil {
		t.Fatal(err)
	}
	if r.ID == 0 || len(r.Code) < 32 {
		t.Fatalf("request = %+v", r)
	}
	return r
}

func countBookings(t *testing.T, st *Store, source string) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT count(*) FROM bookings WHERE source = ?`, source).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApproveRequestOnce(t *testing.T) {
	st := openTest(t)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	r := addRequest(t, st, hall, kitchen)

	id, err := st.ApproveRequest(r.ID, 0, "Enjoy", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveRequest(r.ID, 0, "", "private"); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("second approve: err = %v, want ErrAlreadyDecided", err)
	}
	if err := st.DeclineRequest(r.ID, 0, "no"); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("decline after approve: err = %v", err)
	}
	if n := countBookings(t, st, SourceRequest); n != 1 {
		t.Fatalf("%d request bookings, want 1", n)
	}
	b, err := st.GetBooking(id)
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != "Movie night" || b.Visibility != "public" || b.StartTime != "18:00" || b.EndTime != "21:00" || len(b.Rooms) != 2 {
		t.Errorf("booking = %+v", b)
	}
	got, _ := st.GetRequest(r.ID)
	if got.Status != "approved" || got.BookingID != id || got.Answer != "Enjoy" {
		t.Errorf("request = %+v", got)
	}
	if rid, _ := st.RequestForBooking(id); rid != r.ID {
		t.Errorf("RequestForBooking = %d", rid)
	}
	if _, err := st.ApproveRequest(999, 0, "", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing request: err = %v", err)
	}
}

func TestDeclineAndWithdraw(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Hall")
	a, b := addRequest(t, st, hall), addRequest(t, st, hall)
	if err := st.DeclineRequest(a.ID, 0, "Taken that night"); err != nil {
		t.Fatal(err)
	}
	if err := st.WithdrawRequest(a.ID); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("withdraw declined: err = %v", err)
	}
	if err := st.WithdrawRequest(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.WithdrawRequest(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: err = %v", err)
	}
	if list, _ := st.ListRequests("declined"); len(list) != 1 || list[0].Answer != "Taken that night" || list[0].RoomNames() != "Hall" {
		t.Errorf("declined = %+v", list)
	}
	// Both pending bookings were cancelled: kept, but the hall is free.
	if busy, _ := st.BusyInRooms([]int64{hall}, "2026-11-07", "18:00", "", "21:00"); len(busy) != 0 {
		t.Errorf("hall still busy: %+v", busy)
	}
	for _, r := range []*Request{a, b} {
		if bk, err := st.GetBooking(r.BookingID); err != nil || bk.Status != "cancelled" {
			t.Errorf("booking %d = %+v, %v", r.BookingID, bk, err)
		}
	}
}

func TestPendingRequestBlocks(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Hall")
	r := addRequest(t, st, hall)
	b, err := st.GetBooking(r.BookingID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "pending" || b.Source != SourceRequest || b.Visibility != "private" || b.CreatedBy != 0 || b.RoomNames() != "Hall" {
		t.Errorf("pending booking = %+v", b)
	}
	if busy, _ := st.BusyInRooms([]int64{hall}, "2026-11-07", "20:00", "", "22:00"); len(busy[hall]) != 1 {
		t.Errorf("pending booking doesn't block: %+v", busy)
	}
	// A member can't book over it, and nothing is saved.
	m := &Request{Name: "Mel", UserID: 0, Title: "Prayer", StartsOn: "2026-11-07", StartTime: "20:00", EndTime: "22:00"}
	busy, err := st.BookRequest(m, []int64{hall})
	if !errors.Is(err, ErrRoomsBusy) || len(busy[hall]) != 1 || m.ID != 0 {
		t.Fatalf("BookRequest over pending: %v, %+v, %+v", err, busy, m)
	}
	if n := countBookings(t, st, SourceRequest); n != 1 {
		t.Errorf("%d request bookings, want 1", n)
	}
	// Approving flips the same booking.
	id, err := st.ApproveRequest(r.ID, 0, "", "public")
	if err != nil || id != r.BookingID {
		t.Fatalf("approve = %d, %v; want booking %d", id, err, r.BookingID)
	}
	if b, _ := st.GetBooking(id); b.Status != "confirmed" || b.Visibility != "public" {
		t.Errorf("approved booking = %+v", b)
	}
	// Free times book straight away, confirmed, made by the member.
	m = &Request{Name: "Mel", Title: "Prayer", StartsOn: "2026-11-08", StartTime: "20:00"}
	if _, err := st.BookRequest(m, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetRequest(m.ID); got.Status != "approved" || got.BookingID == 0 || got.DecidedBy != 0 {
		t.Errorf("member request = %+v", got)
	}
	if b, _ := st.GetBooking(m.BookingID); b.Status != "confirmed" || b.Visibility != "private" {
		t.Errorf("member booking = %+v", b)
	}
}

func TestBusyInRooms(t *testing.T) {
	st := openTest(t)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	addBooking(t, st, Booking{Title: "Choir", StartsOn: "2026-11-07", StartTime: "17:00", EndTime: "18:30"}, hall)
	addBooking(t, st, Booking{Title: "Gone", StartsOn: "2026-11-07", StartTime: "19:00", EndTime: "20:00", Status: "cancelled"}, kitchen)
	addBooking(t, st, Booking{Title: "Retreat", StartsOn: "2026-11-05", EndsOn: "2026-11-06"}, kitchen)

	cases := []struct {
		name                  string
		on, from, ends, until string
		hallBusy, kitchenBusy bool
	}{
		{"overlaps choir", "2026-11-07", "18:00", "", "21:00", true, false},
		{"after choir, cancelled ignored", "2026-11-07", "18:30", "", "21:00", false, false},
		{"all day", "2026-11-07", "", "", "", true, false},
		{"evening before choir's day, no end", "2026-11-06", "18:00", "", "", false, true},
		{"from the night before into the day", "2026-11-06", "20:00", "2026-11-07", "17:30", true, true},
		{"after the retreat", "2026-11-08", "", "", "", false, false},
	}
	for _, c := range cases {
		busy, err := st.BusyInRooms([]int64{hall, kitchen}, c.on, c.from, c.ends, c.until)
		if err != nil {
			t.Fatal(err)
		}
		if (len(busy[hall]) > 0) != c.hallBusy || (len(busy[kitchen]) > 0) != c.kitchenBusy {
			t.Errorf("%s: busy = %+v", c.name, busy)
		}
	}
}
