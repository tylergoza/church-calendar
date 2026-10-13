package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tylergoza/church-calendar/internal/recur"
)

func addEquipment(t *testing.T, st *Store, e Equipment) int64 {
	t.Helper()
	if e.Mode == "" {
		e.Mode = ModeCount
	}
	id, err := st.CreateEquipment(&e)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigration003(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := openAt(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username) VALUES (1, 'mel')`,
		`INSERT INTO rooms (id, name) VALUES (1, 'Hall')`,
		`INSERT INTO bookings (id, source, title, starts_on, status) VALUES (1, 'request', 'Supper', '2026-11-07', 'pending')`,
		`INSERT INTO booking_rooms (booking_id, room_id, room_name) VALUES (1, 1, 'Hall')`,
		`INSERT INTO requests (id, code, name, contact, title, starts_on, booking_id) VALUES (1, 'c1', 'Jo', '', 'Supper', '2026-11-07', 1)`,
		`INSERT INTO request_rooms (request_id, room_id) VALUES (1, 1)`,
	} {
		if _, err := st.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if r, err := st.GetRequest(1); err != nil || r.BookingID != 1 || r.RoomNames() != "Hall" {
		t.Errorf("old request = %+v, %v", r, err)
	}
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", Total: 40, RoomIDs: []int64{1}})
	if err := st.SetBookingEquipment(1, []EquipmentLine{{EquipmentID: chairs, Quantity: 30}}); err != nil {
		t.Fatal(err)
	}
	if lines, err := st.BookingEquipment(1); err != nil || len(lines) != 1 || lines[0].Name != "Chairs" || lines[0].Short != 0 {
		t.Errorf("lines = %+v, %v", lines, err)
	}
}

func TestEquipmentSaveAndOffer(t *testing.T) {
	st := openTest(t)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", Total: 40, RoomIDs: []int64{hall}})
	sound := addEquipment(t, st, Equipment{Name: "Sound", Mode: ModeService, Total: 9, RoomIDs: []int64{hall, kitchen}})
	addEquipment(t, st, Equipment{Name: "Inflatables", Total: 2}) // offered nowhere

	if _, err := st.CreateEquipment(&Equipment{Name: "chairs", Mode: ModeCount}); !errors.Is(err, ErrEquipmentNameTaken) {
		t.Errorf("same name: %v", err)
	}
	if _, err := st.CreateEquipment(&Equipment{Name: "Odd", Mode: "lots"}); err == nil {
		t.Error("a bad mode was saved")
	}
	if e, _ := st.GetEquipment(sound); e.Total != 0 || !e.Service() || len(e.RoomIDs) != 2 {
		t.Errorf("a service has no total: %+v", e)
	}
	got, err := st.OfferedWith([]int64{kitchen})
	if err != nil || len(got) != 1 || got[0].ID != sound {
		t.Errorf("offered with the kitchen = %+v, %v", got, err)
	}
	if got, _ := st.OfferedWith([]int64{hall}); len(got) != 2 {
		t.Errorf("offered with the hall = %+v", got)
	}
	if got, _ := st.OfferedWith(nil); len(got) != 0 {
		t.Errorf("no rooms, nothing offered: %+v", got)
	}
	e, _ := st.GetEquipment(chairs)
	e.Archived = true
	if err := st.UpdateEquipment(e); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OfferedWith([]int64{hall}); len(got) != 1 {
		t.Errorf("archived equipment is offered: %+v", got)
	}
}

// A building offer is for the building booked whole, not the rooms in it.
func TestEquipmentOfferExactRoom(t *testing.T) {
	st := openTest(t)
	st.ImportRooms(treePlaces(), []int64{2, 4})
	main, nursery := roomByName(t, st, "Main building"), roomByName(t, st, "Nursery")
	addEquipment(t, st, Equipment{Name: "Inflatables", Total: 2, RoomIDs: []int64{main.ID}})
	addEquipment(t, st, Equipment{Name: "Cribs", Total: 6, RoomIDs: []int64{nursery.ID}})
	if got, _ := st.OfferedWith([]int64{nursery.ID}); len(got) != 1 || got[0].Name != "Cribs" {
		t.Errorf("room inside = %+v", got)
	}
	if got, _ := st.OfferedWith([]int64{main.ID}); len(got) != 1 || got[0].Name != "Inflatables" {
		t.Errorf("building whole = %+v", got)
	}
}

func TestEquipmentShortages(t *testing.T) {
	st := openTest(t)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", Total: 40, RoomIDs: []int64{hall, kitchen}})
	sound := addEquipment(t, st, Equipment{Name: "Sound", Mode: ModeService, RoomIDs: []int64{hall, kitchen}})
	evening := Booking{StartsOn: "2026-11-07", StartTime: "18:00", EndTime: "21:00"}

	a := addBooking(t, st, evening, hall)
	st.SetBookingEquipment(a, []EquipmentLine{{EquipmentID: chairs, Quantity: 30}, {EquipmentID: sound, Quantity: 5, Note: "2 mics"}})
	pencilled := evening
	pencilled.Status = "tentative"
	b := addBooking(t, st, pencilled, kitchen)
	st.SetBookingEquipment(b, []EquipmentLine{{EquipmentID: chairs, Quantity: 20}, {EquipmentID: sound, Quantity: 1}})

	lines, err := st.BookingEquipment(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Name != "Chairs" || lines[0].Short != 10 {
		t.Fatalf("chairs should be short by 10 (pencilled in counts): %+v", lines)
	}
	if lines[1].Short != 0 || lines[1].Quantity != 1 || lines[1].Note != "2 mics" {
		t.Errorf("a service is 1 and never short: %+v", lines[1])
	}
	// Not asked for then, leaving out the booking itself (editing it).
	asked, _ := st.EquipmentAskedFor("2026-11-07", "19:00", "", "20:00", a)
	if asked[chairs] != 20 || asked[sound] != 0 {
		t.Errorf("asked = %v", asked)
	}
	if list, _ := st.ShortBookings("2026-11-01"); len(list) != 2 {
		t.Errorf("short list = %+v", list)
	}
	if clash, _ := st.EquipmentClashes(a); len(clash[chairs]) != 1 || clash[chairs][0].ID != b || len(clash[sound]) != 0 {
		t.Errorf("clashes = %+v", clash)
	}
	// A later booking doesn't overlap.
	later := addBooking(t, st, Booking{StartsOn: "2026-11-07", StartTime: "21:00", EndTime: "22:00"}, hall)
	st.SetBookingEquipment(later, []EquipmentLine{{EquipmentID: chairs, Quantity: 40}})
	if lines, _ := st.BookingEquipment(later); lines[0].Short != 0 {
		t.Errorf("a later booking isn't short: %+v", lines)
	}
	// Cancelling frees it.
	if err := st.CancelBooking(b, 0, ""); err != nil {
		t.Fatal(err)
	}
	if lines, _ := st.BookingEquipment(a); lines[0].Short != 0 {
		t.Errorf("cancelled booking still counted: %+v", lines)
	}
	if list, _ := st.ShortBookings("2026-11-01"); len(list) != 0 {
		t.Errorf("short list after cancelling = %+v", list)
	}
}

func TestRequestEquipmentCopied(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", Total: 10, RoomIDs: []int64{hall}})
	sound := addEquipment(t, st, Equipment{Name: "Sound", Mode: ModeService, RoomIDs: []int64{hall}})
	r := Request{Name: "Jo", Phone: "555", Title: "Supper", StartsOn: "2026-11-07",
		Equipment: []EquipmentLine{{EquipmentID: chairs, Quantity: 12}, {EquipmentID: sound, Quantity: 1, Note: "Mic"}}}
	if err := st.CreateRequest(&r, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	if lines, _ := st.RequestEquipment(r.ID); len(lines) != 2 {
		t.Errorf("request lines = %+v", lines)
	}
	lines, _ := st.BookingEquipment(r.BookingID)
	if len(lines) != 2 || lines[0].Quantity != 12 || lines[0].Short != 2 {
		t.Errorf("pending booking lines = %+v (pending counts, 12 of 10 is short by 2)", lines)
	}

	// A member over the total is still booked.
	m := Request{Name: "Mel", UserID: 0, Title: "Prayer", StartsOn: "2026-11-07",
		Equipment: []EquipmentLine{{EquipmentID: chairs, Quantity: 5}}}
	if _, err := st.BookRequest(&m, nil); err != nil {
		t.Fatalf("booking with equipment short: %v", err)
	}

	// The owner changes it: equipment replaced on request and booking, and
	// the In use toggle stays on lines kept.
	if err := st.SetLineInUse(r.BookingID, chairs, true, 0); err != nil {
		t.Fatal(err)
	}
	r.Equipment = []EquipmentLine{{EquipmentID: chairs, Quantity: 8}}
	if _, err := st.UpdateOwnRequest(&r, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	lines, _ = st.BookingEquipment(r.BookingID)
	if len(lines) != 1 || lines[0].Quantity != 8 || !lines[0].InUse {
		t.Errorf("after edit = %+v", lines)
	}
	if rl, _ := st.RequestEquipment(r.ID); len(rl) != 1 {
		t.Errorf("request after edit = %+v", rl)
	}
}

func TestEquipmentInUse(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", Total: 40, RoomIDs: []int64{hall}})
	a := addBooking(t, st, Booking{StartsOn: "2020-01-01"}, hall) // long over
	st.SetBookingEquipment(a, []EquipmentLine{{EquipmentID: chairs, Quantity: 30}})
	if err := st.SetLineInUse(a, chairs, true, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLineInUse(a, 999, true, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such line: %v", err)
	}
	list, err := st.InUseLines()
	if err != nil || len(list) != 1 || list[0].Booking.ID != a || list[0].Booking.RoomNames() != "Hall" || list[0].Line.InUseChangedAt == "" {
		t.Fatalf("in use after the booking ended = %+v, %v", list, err)
	}
	st.SetLineInUse(a, chairs, false, 0)
	if list, _ := st.InUseLines(); len(list) != 0 {
		t.Errorf("taken down = %+v", list)
	}

	// Renaming keeps booking names in step; archiving keeps the line.
	e, _ := st.GetEquipment(chairs)
	e.Name, e.Archived = "Folding chairs", true
	st.UpdateEquipment(e)
	if lines, _ := st.BookingEquipment(a); lines[0].Name != "Folding chairs" {
		t.Errorf("lines = %+v", lines)
	}
}

func TestEquipmentPlaces(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", ProductID: 7, Total: 50})
	st.SetEquipmentPlaces(chairs, []EquipmentPlace{{Path: "Storage B", Quantity: 20}, {Path: "Main › Hall", RoomID: hall, Quantity: 30}})
	st.SetEquipmentPlaces(chairs, []EquipmentPlace{{Path: "Storage B", Quantity: 20}, {Path: "Main › Hall", RoomID: hall, Quantity: 30}})
	places, err := st.EquipmentPlaces()
	if err != nil || len(places[chairs]) != 2 || places[chairs][0].RoomID != hall || places[chairs][1].RoomID != 0 {
		t.Errorf("places = %+v, %v", places, err)
	}
}

func TestSeriesCopiesEquipment(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, Equipment{Name: "Chairs", Total: 40, RoomIDs: []int64{hall}})
	a := addBooking(t, st, Booking{StartsOn: "2026-11-02", StartTime: "18:00", EndTime: "19:00"}, hall)
	st.SetBookingEquipment(a, []EquipmentLine{{EquipmentID: chairs, Quantity: 12}})
	st.SetLineInUse(a, chairs, true, 0)
	sid, err := st.MakeSeries(a, recur.Rule{Freq: recur.Weekly, Every: 1, Count: 3}, "2026-11-01")
	if err != nil {
		t.Fatal(err)
	}
	dates, _ := st.Occurrences(sid)
	for _, d := range dates {
		lines, _ := st.BookingEquipment(d.ID)
		if len(lines) != 1 || lines[0].Quantity != 12 || (d.ID != a && lines[0].InUse) {
			t.Errorf("date %s lines = %+v", d.StartsOn, lines)
		}
	}
}
