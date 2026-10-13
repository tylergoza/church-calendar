package store

import "testing"

// syncRooms makes rooms that follow tracker places 9 (Lifecenter) and 11
// (Kitchen).
func syncRooms(t *testing.T, st *Store) (hall, kitchen int64) {
	t.Helper()
	if _, err := st.ImportRooms([]TrackerPlace{
		{ID: 9, Label: "Lifecenter", Name: "Lifecenter", Kind: "building", Bookable: true},
		{ID: 11, Label: "Kitchen", Name: "Kitchen", Kind: "room", Bookable: true},
	}, []int64{9, 11}); err != nil {
		t.Fatal(err)
	}
	rooms, _ := st.ListRooms(true)
	for _, r := range rooms {
		switch r.PlaceID {
		case 9:
			hall = r.ID
		case 11:
			kitchen = r.ID
		}
	}
	return hall, kitchen
}

func equipmentByProduct(t *testing.T, st *Store, product int64) *Equipment {
	t.Helper()
	list, _ := st.ListEquipment(true)
	for i := range list {
		if list[i].ProductID == product {
			return &list[i]
		}
	}
	return nil
}

func TestSyncEquipmentAddsUpdatesArchives(t *testing.T) {
	st := openTest(t)
	hall, kitchen := syncRooms(t, st)
	res, err := st.SyncEquipment([]TrackerProduct{
		{ID: 7, Name: "Round tables", Booking: "count", Total: 50, Notes: "Seats eight", OfferedIn: []int64{9, 11, 404}},
		{ID: 8, Name: "Sound", Booking: "service", Total: 5, OfferedIn: []int64{9}},
		{ID: 9, Name: "Ladder", Booking: "", Total: 2, OfferedIn: []int64{9}}, // not offered
		{ID: 10, Name: "Tent", Booking: "count", Total: 1},                    // offered nowhere
		{ID: 12, Name: "Garden", Booking: "count", Total: 1, OfferedIn: []int64{404}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 3 || !res.Changed() {
		t.Fatalf("result = %+v", res)
	}
	tables := equipmentByProduct(t, st, 7)
	if tables == nil || tables.Mode != ModeCount || tables.Total != 50 || tables.Notes != "Seats eight" || len(tables.RoomIDs) != 2 {
		t.Fatalf("tables = %+v", tables)
	}
	if sound := equipmentByProduct(t, st, 8); sound == nil || sound.Total != 0 || !sound.Service() || len(sound.RoomIDs) != 1 || sound.RoomIDs[0] != hall {
		t.Errorf("sound = %+v", sound)
	}
	if equipmentByProduct(t, st, 9) != nil || equipmentByProduct(t, st, 10) != nil {
		t.Error("a product that isn't offered was added")
	}
	garden := equipmentByProduct(t, st, 12)
	if garden == nil || len(garden.RoomIDs) != 0 {
		t.Errorf("an offer in an unknown place should be skipped, leaving no rooms: %+v", garden)
	}

	// Again with nothing changed does nothing.
	same := []TrackerProduct{
		{ID: 7, Name: "Round tables", Booking: "count", Total: 50, Notes: "Seats eight", OfferedIn: []int64{9, 11}},
		{ID: 8, Name: "Sound", Booking: "service", OfferedIn: []int64{9}},
		{ID: 12, Name: "Garden", Booking: "count", Total: 1, OfferedIn: []int64{404}},
	}
	if res, _ := st.SyncEquipment(same); res.Changed() {
		t.Errorf("nothing changed, but %+v", res)
	}

	// Renamed, a new total, and moved to the kitchen only; Sound is gone.
	res, err = st.SyncEquipment([]TrackerProduct{
		{ID: 7, Name: "Tables", Booking: "count", Total: 60, Notes: "Seats eight", OfferedIn: []int64{11}},
		{ID: 12, Name: "Garden", Booking: "count", Total: 1, OfferedIn: []int64{404}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 1 || len(res.Archived) != 1 || res.Archived[0] != "Sound" {
		t.Errorf("result = %+v", res)
	}
	tables = equipmentByProduct(t, st, 7)
	if tables.Name != "Tables" || tables.Total != 60 || len(tables.RoomIDs) != 1 || tables.RoomIDs[0] != kitchen {
		t.Errorf("tables = %+v", tables)
	}
	if sound := equipmentByProduct(t, st, 8); !sound.Archived {
		t.Error("sound should be archived")
	}

	// Sound comes back: un-archived.
	if _, err := st.SyncEquipment([]TrackerProduct{{ID: 8, Name: "Sound", Booking: "service", OfferedIn: []int64{9}}}); err != nil {
		t.Fatal(err)
	}
	if sound := equipmentByProduct(t, st, 8); sound.Archived {
		t.Error("sound should be back")
	}
}

func TestSyncEquipmentKeepsBookingNames(t *testing.T) {
	st := openTest(t)
	syncRooms(t, st)
	st.SyncEquipment([]TrackerProduct{{ID: 7, Name: "Round tables", Booking: "count", Total: 50, OfferedIn: []int64{9}}})
	tables := equipmentByProduct(t, st, 7)
	hall, _ := st.ListRooms(false)
	b := addBooking(t, st, Booking{Title: "Supper", StartsOn: "2026-11-07", StartTime: "17:00", EndsOn: "2026-11-07", EndTime: "19:00"}, hall[0].ID)
	if err := st.SetBookingEquipment(b, []EquipmentLine{{EquipmentID: tables.ID, Quantity: 10}}); err != nil {
		t.Fatal(err)
	}

	// Renamed: the booking follows. Gone: archived, and the booking keeps it.
	st.SyncEquipment([]TrackerProduct{{ID: 7, Name: "Tables", Booking: "count", Total: 50, OfferedIn: []int64{9}}})
	if lines, _ := st.BookingEquipment(b); len(lines) != 1 || lines[0].Name != "Tables" {
		t.Errorf("after a rename = %+v", lines)
	}
	if _, err := st.SyncEquipment(nil); err != nil {
		t.Fatal(err)
	}
	lines, err := st.BookingEquipment(b)
	if err != nil || len(lines) != 1 || lines[0].Name != "Tables" || lines[0].Quantity != 10 {
		t.Errorf("after archiving = %+v, %v", lines, err)
	}
	if got, _ := st.OfferedWith([]int64{hall[0].ID}); len(got) != 0 {
		t.Errorf("archived equipment is still offered: %+v", got)
	}
}

func TestSyncEquipmentNameClash(t *testing.T) {
	st := openTest(t)
	hall, _ := syncRooms(t, st)
	hand := addEquipment(t, st, Equipment{Name: "round tables", Total: 5, RoomIDs: []int64{hall}})
	other := addEquipment(t, st, Equipment{Name: "Sound", Mode: ModeService})
	b := addBooking(t, st, Booking{Title: "Supper", StartsOn: "2026-11-07"}, hall)
	st.SetBookingEquipment(b, []EquipmentLine{{EquipmentID: hand, Quantity: 3}})

	res, err := st.SyncEquipment([]TrackerProduct{
		{ID: 7, Name: "Round tables", Booking: "count", Total: 50, OfferedIn: []int64{9}},
		{ID: 8, Name: "Sound", Booking: "service", OfferedIn: []int64{9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The hand-made row with the same name follows the product, keeping its
	// bookings; its details become the tracker's.
	e, _ := st.GetEquipment(hand)
	if e.ProductID != 7 || e.Total != 50 || e.Name != "Round tables" || len(res.Linked) != 2 || len(res.Added) != 0 {
		t.Errorf("linked = %+v, result %+v", e, res)
	}
	if lines, _ := st.BookingEquipment(b); len(lines) != 1 || lines[0].EquipmentID != hand || lines[0].Name != "Round tables" {
		t.Errorf("booking = %+v", lines)
	}
	if list, _ := st.ListEquipment(true); len(list) != 2 {
		t.Errorf("equipment = %d, want 2, nothing duplicated", len(list))
	}
	if o, _ := st.GetEquipment(other); o.ProductID != 8 || !o.Service() {
		t.Errorf("Sound should follow its product too: %+v", o)
	}

	// A product renamed onto a name another row has keeps its old name.
	res, err = st.SyncEquipment([]TrackerProduct{
		{ID: 7, Name: "Sound", Booking: "count", Total: 50, OfferedIn: []int64{9}},
		{ID: 8, Name: "Sound", Booking: "service", OfferedIn: []int64{9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := st.GetEquipment(hand); e.Name != "Round tables" || len(res.Skipped) == 0 {
		t.Errorf("rename onto a taken name: %+v, %+v", e, res)
	}
	// A new product whose name belongs to equipment following another product is skipped.
	res, _ = st.SyncEquipment([]TrackerProduct{
		{ID: 7, Name: "Round tables", Booking: "count", Total: 50, OfferedIn: []int64{9}},
		{ID: 8, Name: "Sound", Booking: "service", OfferedIn: []int64{9}},
		{ID: 20, Name: "Round tables", Booking: "count", Total: 1, OfferedIn: []int64{9}},
	})
	if len(res.Skipped) != 1 || equipmentByProduct(t, st, 20) != nil {
		t.Errorf("result = %+v", res)
	}
}
