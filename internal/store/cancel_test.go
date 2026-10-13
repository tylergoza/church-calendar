package store

import (
	"errors"
	"testing"
)

// memberRequest books a member's request at once.
func memberRequest(t *testing.T, st *Store, userID int64, day string, rooms ...int64) *Request {
	t.Helper()
	r := &Request{Name: "Mel", Title: "Supper", StartsOn: day, StartTime: "18:00", EndTime: "20:00", UserID: userID}
	if busy, err := st.BookRequest(r, rooms); err != nil {
		t.Fatalf("book: %v %v", err, busy)
	}
	return r
}

func TestUpdateOwnRequest(t *testing.T) {
	st := openTest(t)
	mel, _ := st.CreateUser("mel", "Mel", "a long password", RoleMember)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	r := memberRequest(t, st, mel, "2026-11-07", hall)

	// A later booking in the kitchen blocks a move there; nothing changes.
	other := &Request{Name: "Jo", Title: "Class", StartsOn: "2026-11-07", StartTime: "19:00", EndTime: "21:00"}
	if err := st.CreateRequest(other, []int64{kitchen}); err != nil {
		t.Fatal(err)
	}
	edit := Request{ID: r.ID, Title: "Big supper", StartsOn: "2026-11-07", StartTime: "18:30", EndTime: "20:30", Details: "tables"}
	busy, err := st.UpdateOwnRequest(&edit, []int64{hall, kitchen})
	if !errors.Is(err, ErrRoomsBusy) || len(busy[kitchen]) != 1 {
		t.Fatalf("busy move: %v %v", err, busy)
	}
	got, _ := st.GetRequest(r.ID)
	b, _ := st.GetBooking(r.BookingID)
	if got.Title != "Supper" || got.StartTime != "18:00" || len(got.Rooms) != 1 || b.Title != "Supper" || len(b.Rooms) != 1 {
		t.Fatalf("a refused edit changed things: %+v %+v", got, b)
	}

	// Overlapping only itself is fine; the booking follows.
	if busy, err := st.UpdateOwnRequest(&edit, []int64{hall}); err != nil {
		t.Fatalf("self overlap: %v %v", err, busy)
	}
	got, _ = st.GetRequest(r.ID)
	b, _ = st.GetBooking(r.BookingID)
	if got.Title != "Big supper" || got.Details != "tables" || b.Title != "Big supper" || b.StartTime != "18:30" || b.Status != "confirmed" {
		t.Fatalf("edit: %+v %+v", got, b)
	}
}

func TestUpdateOwnWaitingRequestIsBooked(t *testing.T) {
	st := openTest(t)
	mel, _ := st.CreateUser("mel", "Mel", "a long password", RoleMember)
	hall := addRoom(t, st, "Hall")
	r := &Request{Name: "Mel", Title: "Supper", StartsOn: "2026-11-07", StartTime: "18:00", EndTime: "20:00", UserID: mel}
	if err := st.CreateRequest(r, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	edit := *r
	edit.Title = "Late supper"
	if _, err := st.UpdateOwnRequest(&edit, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRequest(r.ID)
	b, _ := st.GetBooking(r.BookingID)
	if got.Status != "approved" || b.Status != "confirmed" || b.CreatedBy != mel {
		t.Fatalf("not booked: %+v %+v", got, b)
	}
}

func TestOwnRequestCancelAndTakenOver(t *testing.T) {
	st := openTest(t)
	mel, _ := st.CreateUser("mel", "Mel", "a long password", RoleMember)
	hall := addRoom(t, st, "Hall")
	r := memberRequest(t, st, mel, "2026-11-07", hall)
	if err := st.CancelOwnRequest(r.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetRequest(r.ID)
	b, _ := st.GetBooking(r.BookingID)
	if got.Status != "withdrawn" || b.Status != "cancelled" {
		t.Fatalf("cancel: %+v %+v", got, b)
	}
	if busy, _ := st.BusyInRooms([]int64{hall}, "2026-11-07", "19:00", "", "19:30"); len(busy) != 0 {
		t.Error("cancelled booking still blocks")
	}
	if err := st.CancelOwnRequest(r.ID); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("cancel twice: %v", err)
	}
	if _, err := st.UpdateOwnRequest(&Request{ID: r.ID, Title: "x", StartsOn: "2026-11-07"}, []int64{hall}); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("edit cancelled: %v", err)
	}

	// A planner takes the booking over: the owner can't touch it.
	r2 := memberRequest(t, st, mel, "2026-11-08", hall)
	pushed, err := st.PutSourceBooking(Booking{Source: "events", SourceID: "9", Title: "Supper", StartsOn: "2026-11-08",
		StartTime: "18:00", EndTime: "20:00", Visibility: "private", Status: "confirmed"}, []PushRoom{{Name: "Hall"}}, r2.BookingID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRequest(r2.ID)
	if got.BookingID != pushed.ID {
		t.Fatalf("request not moved: %+v", got)
	}
	if err := st.CancelOwnRequest(r2.ID); !errors.Is(err, ErrTakenOver) {
		t.Errorf("cancel taken over: %v", err)
	}
	if _, err := st.UpdateOwnRequest(&Request{ID: r2.ID, Title: "x", StartsOn: "2026-11-08"}, []int64{hall}); !errors.Is(err, ErrTakenOver) {
		t.Errorf("edit taken over: %v", err)
	}
	if b, _ := st.GetBooking(pushed.ID); b.Status != "confirmed" || b.Title != "Supper" {
		t.Errorf("planner's booking touched: %+v", b)
	}
}

func TestCancelBooking(t *testing.T) {
	st := openTest(t)
	sam, _ := st.CreateUser("sam", "Sam", "a long password", RoleScheduler)
	mel, _ := st.CreateUser("mel", "Mel", "a long password", RoleMember)
	hall := addRoom(t, st, "Hall")

	r := memberRequest(t, st, mel, "2026-11-07", hall)
	if err := st.CancelBooking(r.BookingID, sam, "Roof repairs."); err != nil {
		t.Fatal(err)
	}
	b, _ := st.GetBooking(r.BookingID)
	got, _ := st.GetRequest(r.ID)
	if b.Status != "cancelled" || b.CancelNote != "Roof repairs." || b.CancelledBy != sam || got.Status != "cancelled" || got.Answer != "Roof repairs." {
		t.Fatalf("cancel: %+v %+v", b, got)
	}
	if err := st.CancelBooking(r.BookingID, sam, ""); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("twice: %v", err)
	}
	if err := st.CancelBooking(999, sam, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	// Another app's booking is refused.
	pushed, err := st.PutSourceBooking(Booking{Source: "events", SourceID: "1", Title: "Dinner", StartsOn: "2026-11-09",
		Visibility: "private", Status: "confirmed"}, []PushRoom{{Name: "Hall"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CancelBooking(pushed.ID, sam, ""); !errors.Is(err, ErrTakenOver) {
		t.Errorf("pushed: %v", err)
	}
}

func TestCancelSeriesFrom(t *testing.T) {
	st := openTest(t)
	sam, _ := st.CreateUser("sam", "Sam", "a long password", RoleScheduler)
	hall := addRoom(t, st, "Hall")
	first := addBooking(t, st, Booking{Title: "Choir", StartsOn: "2026-11-01", StartTime: "18:00", EndTime: "20:00"}, hall)
	sid, err := st.MakeSeries(first, weekly("2026-11-01"), "2026-10-12")
	if err != nil {
		t.Fatal(err)
	}
	occ, _ := st.Occurrences(sid)
	if err := st.CancelBooking(occ[1].ID, sam, "one off"); err != nil { // this date only
		t.Fatal(err)
	}
	if occ, _ = st.Occurrences(sid); occ[1].Status != "cancelled" || occ[2].Status == "cancelled" {
		t.Fatalf("single cancel: %+v", occ)
	}
	n, err := st.CancelSeriesFrom(sid, occ[2].OccurrenceDate, sam, "season over")
	if err != nil || n != len(occ)-2 {
		t.Fatalf("cancelled %d (%v), want %d", n, err, len(occ)-2)
	}
	occ, _ = st.Occurrences(sid)
	if occ[0].Status == "cancelled" || occ[1].CancelNote != "one off" || occ[2].CancelNote != "season over" {
		t.Fatalf("after: %+v", occ)
	}
	// The repeat has ended, so topping up makes nothing new.
	if n, _ := st.ExtendSeries("2027-03-01"); n != 0 {
		t.Errorf("extended %d dates after cancelling later ones", n)
	}
}
