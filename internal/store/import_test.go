package store

import (
	"slices"
	"testing"
)

func pushByName(t *testing.T, st *Store, sourceID string, names ...string) int64 {
	t.Helper()
	var rooms []PushRoom
	for _, n := range names {
		rooms = append(rooms, PushRoom{Name: n})
	}
	res, err := st.PutSourceBooking(Booking{Source: "events", SourceID: sourceID, Title: "Pushed",
		StartsOn: "2026-11-07", Status: "confirmed"}, rooms, 0)
	if err != nil {
		t.Fatal(err)
	}
	return res.ID
}

func TestImportRooms(t *testing.T) {
	st := openTest(t)
	kitchen := addRoom(t, st, "Kitchen") // typed in by hand
	nursery, kitchenPlace := TrackerPlace{ID: 4, Label: "104 – Nursery", Name: "Nursery", Path: "Main › 104 – Nursery"},
		TrackerPlace{ID: 7, Label: "Kitchen", Name: "Kitchen", Path: "Main › Kitchen"}
	hall := TrackerPlace{ID: 9, Label: "Lifecenter", Name: "Lifecenter", Path: "Main › Lifecenter"}
	places := []TrackerPlace{nursery, kitchenPlace, hall}

	// Pushed before the rooms were here: stored by name only.
	b1 := pushByName(t, st, "1", "nursery", "Lifecenter")
	b2 := pushByName(t, st, "2", "Nursery", "104 – Nursery") // two names for one room

	res, err := st.ImportRooms(places, []int64{4, 7})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Added, []string{"104 – Nursery"}) || !slices.Equal(res.Linked, []string{"Kitchen"}) {
		t.Errorf("added %v linked %v", res.Added, res.Linked)
	}
	if k, _ := st.GetRoom(kitchen); k.PlaceID != 7 || k.TrackerPath != "Main › Kitchen" {
		t.Errorf("hand-made Kitchen not linked to its place: %+v", k)
	}
	if res.Bookings != 2 {
		t.Errorf("linked %d booking rooms, want 2", res.Bookings)
	}
	got, _ := st.GetBooking(b1)
	if got.RoomNames() != "104 – Nursery, Lifecenter" || len(got.RoomIDs()) != 1 {
		t.Errorf("booking 1 rooms = %q ids %v", got.RoomNames(), got.RoomIDs())
	}
	if got, _ := st.GetBooking(b2); len(got.Rooms) != 1 || len(got.RoomIDs()) != 1 {
		t.Errorf("booking 2 rooms = %+v, want one linked room", got.Rooms)
	}

	// Running again changes nothing and never duplicates.
	res, err = st.ImportRooms(places, []int64{4, 7})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != nil || res.Linked != nil || res.Renamed != nil || res.Bookings != 0 {
		t.Errorf("second run did something: %+v", res)
	}
	if list, _ := st.ListRooms(true); len(list) != 2 {
		t.Errorf("rooms = %d, want 2", len(list))
	}

	// The tracker renames a place and deletes another; the hall is added.
	nursery.Label, nursery.Path = "104 – Little Lambs", "Main › 104 – Little Lambs"
	res, err = st.ImportRooms([]TrackerPlace{nursery, hall}, []int64{9})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Renamed, []string{"104 – Little Lambs"}) || !slices.Equal(res.Archived, []string{"Kitchen"}) ||
		!slices.Equal(res.Added, []string{"Lifecenter"}) || res.Bookings != 1 {
		t.Errorf("third run: %+v", res)
	}
	got, _ = st.GetBooking(b1)
	if got.RoomNames() != "104 – Little Lambs, Lifecenter" || len(got.RoomIDs()) != 2 {
		t.Errorf("booking 1 after rename = %q ids %v", got.RoomNames(), got.RoomIDs())
	}
	if k, _ := st.GetRoom(kitchen); !k.Archived {
		t.Error("room whose place is gone should be archived")
	}
}

func TestImportRoomsNameClash(t *testing.T) {
	st := openTest(t)
	addRoom(t, st, "Chapel")
	a := TrackerPlace{ID: 1, Label: "Chapel", Name: "Chapel"}
	b := TrackerPlace{ID: 2, Label: "Chapel", Name: "Chapel"} // same name, another building
	if _, err := st.ImportRooms([]TrackerPlace{a, b}, []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListRooms(true); len(list) != 1 || list[0].PlaceID != 1 {
		t.Errorf("rooms = %+v, want the hand-made Chapel linked to place 1 only", list)
	}

	// A rename onto a name another room has keeps the old name.
	addRoom(t, st, "Parlor")
	a.Label = "Parlor"
	res, err := st.ImportRooms([]TrackerPlace{a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Renamed != nil {
		t.Errorf("rename clash: %+v", res)
	}
}

// Adding or renaming a room by hand links bookings sent with its name.
func TestRoomLinksBookingsByName(t *testing.T) {
	st := openTest(t)
	b := pushByName(t, st, "1", "Pavilion", "Gym")
	addRoom(t, st, "pavilion")
	if got, _ := st.GetBooking(b); got.RoomNames() != "Gym, pavilion" || len(got.RoomIDs()) != 1 {
		t.Errorf("after add: %+v", got.Rooms)
	}
	r, _ := st.GetRoom(addRoom(t, st, "Old gym"))
	r.Name = "GYM"
	if err := st.UpdateRoom(r); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetBooking(b); got.RoomNames() != "GYM, pavilion" || len(got.RoomIDs()) != 2 {
		t.Errorf("after rename: %+v", got.Rooms)
	}
}
