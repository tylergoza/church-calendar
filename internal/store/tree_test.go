package store

import (
	"slices"
	"testing"
)

// Places: a site (1) > Main building (2) > 2nd floor (3, not imported) >
// Nursery (4); Main building > Kitchen (5); Annex (6) on its own.
func treePlaces() []TrackerPlace {
	return []TrackerPlace{
		{ID: 1, Label: "Campus", Name: "Campus", Kind: "site"},
		{ID: 2, Label: "Main building", Name: "Main building", Kind: "building", ParentID: 1, Bookable: true},
		{ID: 3, Label: "2nd floor", Name: "2nd floor", Kind: "floor", ParentID: 2},
		{ID: 4, Label: "Nursery", Name: "Nursery", Kind: "room", ParentID: 3, Bookable: true},
		{ID: 5, Label: "Kitchen", Name: "Kitchen", Kind: "room", ParentID: 2},
		{ID: 6, Label: "Annex", Name: "Annex", Kind: "building", ParentID: 1, Bookable: true},
	}
}

func roomByName(t *testing.T, st *Store, name string) Room {
	t.Helper()
	rooms, err := st.ListRooms(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no room %q", name)
	return Room{}
}

func TestImportRoomsKindParentBookable(t *testing.T) {
	st := openTest(t)
	places := treePlaces()
	if _, err := st.ImportRooms(places, []int64{2, 4, 5}); err != nil {
		t.Fatal(err)
	}
	main, nursery, kitchen := roomByName(t, st, "Main building"), roomByName(t, st, "Nursery"), roomByName(t, st, "Kitchen")
	if main.Kind != "building" || main.ParentID != 0 || !main.Bookable {
		t.Errorf("building = %+v", main)
	}
	if nursery.Kind != "room" || nursery.ParentID != main.ID || !nursery.Bookable {
		t.Errorf("nursery should sit in the building, past the floor and site: %+v", nursery)
	}
	if kitchen.ParentID != main.ID || kitchen.Bookable {
		t.Errorf("kitchen = %+v, want inside the building and not bookable", kitchen)
	}

	// The tracker changes: kitchen bookable, nursery moves to the annex,
	// the building is no longer bookable.
	places[4].Bookable = true
	places[3].ParentID = 6
	places[1].Bookable = false
	res, err := st.ImportRooms(places, []int64{6})
	if err != nil {
		t.Fatal(err)
	}
	annex := roomByName(t, st, "Annex")
	if got := roomByName(t, st, "Nursery"); got.ParentID != annex.ID {
		t.Errorf("nursery parent = %d, want the annex %d", got.ParentID, annex.ID)
	}
	if !roomByName(t, st, "Kitchen").Bookable || roomByName(t, st, "Main building").Bookable {
		t.Error("bookable flags should follow the tracker")
	}
	if !slices.Contains(res.Moved, "Nursery") || len(res.Flagged) != 2 || len(res.Added) != 1 {
		t.Errorf("result = %+v", res)
	}
	// Nothing changed: nothing reported.
	res, _ = st.ImportRooms(places, nil)
	if len(res.Moved)+len(res.Flagged)+len(res.Renamed)+len(res.Added) != 0 {
		t.Errorf("a repeat should report nothing: %+v", res)
	}
}

func TestFamilyBusyBothWays(t *testing.T) {
	st := openTest(t)
	if _, err := st.ImportRooms(treePlaces(), []int64{2, 4, 5, 6}); err != nil {
		t.Fatal(err)
	}
	main, nursery, kitchen, annex := roomByName(t, st, "Main building"), roomByName(t, st, "Nursery"), roomByName(t, st, "Kitchen"), roomByName(t, st, "Annex")
	day := Booking{StartsOn: "2026-11-07"}
	inNursery := addBooking(t, st, day, nursery.ID)

	busy, err := st.BusyInRooms([]int64{main.ID, kitchen.ID, annex.ID, nursery.ID}, "2026-11-07", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(busy[main.ID]) != 1 || busy[main.ID][0].ID != inNursery {
		t.Errorf("booking the nursery should make the building busy: %v", busy)
	}
	if len(busy[nursery.ID]) != 1 {
		t.Errorf("nursery busy = %v", busy[nursery.ID])
	}
	if len(busy[kitchen.ID]) != 0 || len(busy[annex.ID]) != 0 {
		t.Errorf("a sibling room and another building stay free: %v", busy)
	}

	// The other way round: the whole building booked.
	st2 := openTest(t)
	st2.ImportRooms(treePlaces(), []int64{2, 4, 5})
	main, nursery, kitchen = roomByName(t, st2, "Main building"), roomByName(t, st2, "Nursery"), roomByName(t, st2, "Kitchen")
	whole := addBooking(t, st2, day, main.ID)
	busy, _ = st2.BusyInRooms([]int64{nursery.ID, kitchen.ID}, "2026-11-07", "10:00", "", "11:00")
	if len(busy[nursery.ID]) != 1 || busy[nursery.ID][0].ID != whole {
		t.Errorf("booking the building should make its bookable rooms busy: %v", busy)
	}
	if len(busy[kitchen.ID]) != 0 {
		t.Errorf("the kitchen isn't bookable, so the building booking leaves it free: %v", busy)
	}
	// Moving a booking leaves itself out.
	if busy, _ := st2.BusyInRoomsExcept(whole, []int64{nursery.ID}, "2026-11-07", "", "", ""); len(busy) != 0 {
		t.Errorf("except = %v", busy)
	}
}

func TestFamilyConflicts(t *testing.T) {
	st := openTest(t)
	if _, err := st.ImportRooms(treePlaces(), []int64{2, 4, 5}); err != nil {
		t.Fatal(err)
	}
	main, nursery, kitchen := roomByName(t, st, "Main building"), roomByName(t, st, "Nursery"), roomByName(t, st, "Kitchen")
	day := Booking{StartsOn: "2026-11-07"}
	a := addBooking(t, st, day, nursery.ID)
	b := addBooking(t, st, day, main.ID)
	list, err := st.Conflicts("2026-11-01", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].A.ID != a || list[0].B.ID != b || list[0].Room.Name != "Nursery" {
		t.Fatalf("room inside a booked building = %+v", list)
	}
	// Building booked first, room second: still one clash, shown on the room.
	st2 := openTest(t)
	st2.ImportRooms(treePlaces(), []int64{2, 4, 5})
	main, nursery, kitchen = roomByName(t, st2, "Main building"), roomByName(t, st2, "Nursery"), roomByName(t, st2, "Kitchen")
	addBooking(t, st2, day, main.ID)
	addBooking(t, st2, day, nursery.ID, main.ID) // in both: still one pair
	list, _ = st2.Conflicts("2026-11-01", false)
	if len(list) != 1 {
		t.Fatalf("a pair should be listed once: %+v", list)
	}
	// Two rooms in one building do not clash.
	st3 := openTest(t)
	st3.ImportRooms(treePlaces(), []int64{2, 4, 5})
	nursery, kitchen = roomByName(t, st3, "Nursery"), roomByName(t, st3, "Kitchen")
	addBooking(t, st3, day, nursery.ID)
	addBooking(t, st3, day, kitchen.ID)
	if list, _ := st3.Conflicts("2026-11-01", false); len(list) != 0 {
		t.Errorf("siblings clashed: %+v", list)
	}
	_ = kitchen
}

func TestHandMadeParent(t *testing.T) {
	st := openTest(t)
	hall := addRoom(t, st, "Fellowship hall")
	id, err := st.CreateRoom(&Room{Name: "Stage", ParentID: hall, Bookable: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetRoom(id); got.ParentID != hall {
		t.Errorf("parent = %d", got.ParentID)
	}
	day := Booking{StartsOn: "2026-11-07"}
	addBooking(t, st, day, hall)
	if busy, _ := st.BusyInRooms([]int64{id}, "2026-11-07", "", "", ""); len(busy[id]) != 1 {
		t.Errorf("a room inside a hand-made room should be busy with it: %v", busy)
	}
	// Named lookups (apps pushing by name) follow the family too.
	list, err := st.BusyInRoomsNamed([]string{"Stage"}, "2026-11-07", "2026-11-07")
	if err != nil || len(list) != 1 {
		t.Errorf("named = %v, %v", list, err)
	}
}

// Only bookable rooms share busy time with their building (step 3a).
func TestFamilyOnlyBookableRooms(t *testing.T) {
	day := Booking{StartsOn: "2026-11-07"}

	// A planner booking in the kitchen (not bookable) doesn't block the
	// building, nor the building it.
	st := openTest(t)
	st.ImportRooms(treePlaces(), []int64{2, 4, 5})
	main, kitchen := roomByName(t, st, "Main building"), roomByName(t, st, "Kitchen")
	inKitchen := addBooking(t, st, day, kitchen.ID)
	if busy, _ := st.BusyInRooms([]int64{main.ID}, "2026-11-07", "", "", ""); len(busy[main.ID]) != 0 {
		t.Errorf("an unbookable room inside made the building busy: %v", busy)
	}
	whole := addBooking(t, st, day, main.ID)
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 0 {
		t.Errorf("building and unbookable room clashed: %+v", list)
	}
	// The room is still busy with its own bookings.
	if busy, _ := st.BusyInRooms([]int64{kitchen.ID}, "2026-11-07", "", "", ""); len(busy[kitchen.ID]) != 1 || busy[kitchen.ID][0].ID != inKitchen {
		t.Errorf("kitchen busy = %v", busy)
	}

	// Turning the kitchen bookable brings the clash; turning it off drops it.
	k, _ := st.GetRoom(kitchen.ID)
	k.Bookable = true
	if err := st.UpdateRoom(k); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 1 || list[0].A.ID != inKitchen || list[0].B.ID != whole {
		t.Errorf("bookable kitchen should clash with the building: %+v", list)
	}
	k.Bookable = false
	st.UpdateRoom(k)
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 0 {
		t.Errorf("turning bookable off should drop the clash: %+v", list)
	}

	// The same when the sync turns it off.
	places := treePlaces()
	places[4].Bookable = true
	st.ImportRooms(places, nil)
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 1 {
		t.Errorf("synced bookable kitchen should clash: %+v", list)
	}
	places[4].Bookable = false
	st.ImportRooms(places, nil)
	if list, _ := st.Conflicts("2026-11-01", false); len(list) != 0 {
		t.Errorf("synced unbookable kitchen should not clash: %+v", list)
	}
}

// A bookable room in an area that isn't bookable still pairs with its
// bookable building.
func TestFamilyUnbookableMiddle(t *testing.T) {
	st := openTest(t)
	hall, err := st.CreateRoom(&Room{Name: "Life Center", Bookable: true})
	if err != nil {
		t.Fatal(err)
	}
	area, _ := st.CreateRoom(&Room{Name: "East wing", ParentID: hall})
	stage, _ := st.CreateRoom(&Room{Name: "Stage", ParentID: area, Bookable: true})
	day := Booking{StartsOn: "2026-11-07"}
	inStage := addBooking(t, st, day, stage)
	busy, _ := st.BusyInRooms([]int64{hall, area}, "2026-11-07", "", "", "")
	if len(busy[hall]) != 1 || busy[hall][0].ID != inStage {
		t.Errorf("the building should be busy with the stage: %v", busy)
	}
	if len(busy[area]) != 1 {
		t.Errorf("the area is above the bookable stage, so it's busy too: %v", busy)
	}
	inArea := addBooking(t, st, day, area)
	if busy, _ := st.BusyInRoomsExcept(inArea, []int64{hall}, "2026-11-07", "", "", ""); len(busy[hall]) != 1 || busy[hall][0].ID != inStage {
		t.Errorf("a booking in the unbookable area shouldn't block the building: %v", busy)
	}
}
