package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// bookWithEquipment books a public event made by owner (0 for nobody) in
// the room, asking for quantity of equipment.
func bookWithEquipment(t *testing.T, st *store.Store, title, day string, owner, room, equipment int64, quantity int) int64 {
	t.Helper()
	id := book(t, st, store.Booking{Title: title, StartsOn: day, StartTime: "18:00", EndTime: "20:00", Visibility: "public",
		Source: store.SourceCalendar, CreatedBy: owner}, room)
	if err := st.SetBookingEquipment(id, []store.EquipmentLine{{EquipmentID: equipment, Quantity: quantity}}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestEquipmentOnlyForOwnerAndSchedulers(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Folding chairs", store.ModeCount, 40, hall)
	mel := mustUserID(t, st, "mel")
	mustUserID(t, st, "ned")
	day := soon(3)
	id := bookWithEquipment(t, st, "Supper", day, mel, hall, chairs, 12)
	page := "/calendar/" + itoa(id)

	melC, nedC := c.as("mel"), c.as("ned")
	for who, cl := range map[string]*client{"owner": melC, "scheduler": sam} {
		if body := cl.get(page, 200); !strings.Contains(body, "Folding chairs") || !strings.Contains(body, "12 ×") {
			t.Errorf("%s should see the equipment:\n%s", who, body)
		}
	}
	for who, cl := range map[string]*client{"visitor": c, "other member": nedC} {
		body := cl.get(page, 200)
		if !strings.Contains(body, "Supper") {
			t.Fatalf("%s should still see the public booking: %s", who, body)
		}
		if strings.Contains(body, "Folding chairs") || strings.Contains(body, "Equipment and help") {
			t.Errorf("%s must not see equipment:\n%s", who, body)
		}
	}

	// /my/bookings: the owner's own, and only theirs.
	if body := melC.get("/my/bookings", 200); !strings.Contains(body, "12 Folding chairs") {
		t.Errorf("owner's list: %s", body)
	}
	if body := nedC.get("/my/bookings", 200); strings.Contains(body, "Folding chairs") || strings.Contains(body, "Supper") {
		t.Errorf("other member's list: %s", body)
	}

	// Busy blocks never carry it: a locked booking shows members, its owner included, only rooms and times.
	locked := book(t, st, store.Booking{Title: "Hidden vigil", StartsOn: day, StartTime: "09:00", EndTime: "10:00", Visibility: "locked",
		Source: store.SourceCalendar, CreatedBy: mel}, hall)
	st.SetBookingEquipment(locked, []store.EquipmentLine{{EquipmentID: chairs, Quantity: 5}})
	for who, cl := range map[string]*client{"owner": melC, "other member": nedC} {
		if body := cl.get("/calendar/"+itoa(locked), 200); strings.Contains(body, "Folding chairs") || strings.Contains(body, "Hidden vigil") {
			t.Errorf("%s sees a busy block's details:\n%s", who, body)
		}
	}
	// The calendar pages and the feed never list equipment.
	if body := sam.get("/calendar?month="+day[:7], 200); strings.Contains(body, "Folding chairs") {
		t.Error("the month view shows equipment")
	}
	if body, _ := c.apiGet("/calendar.ics?from="+day+"&to="+day, "", 200); strings.Contains(body, "Folding chairs") || !strings.Contains(body, "Supper") {
		t.Errorf("the feed: %s", body)
	}
}

// mustUserID makes a member (once) and returns their ID.
func mustUserID(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	st.CreateUser(name, strings.ToUpper(name[:1])+name[1:], "a long password", store.RoleMember)
	return mustUser(t, st, name)
}

func TestShortByShowsForSchedulers(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall, annex := addRoom(t, st, "Hall"), addRoom(t, st, "Annex")
	chairs := addEquipment(t, st, "Folding chairs", store.ModeCount, 40, hall, annex)
	mel := mustUserID(t, st, "mel")
	day := soon(3)
	a := bookWithEquipment(t, st, "Supper", day, mel, hall, chairs, 30)
	b := bookWithEquipment(t, st, "Concert", day, 0, annex, chairs, 20)

	body := sam.get("/calendar/"+itoa(a), 200)
	if !strings.Contains(body, "short by 10") || !strings.Contains(body, "equip-short") {
		t.Errorf("scheduler should see short by 10:\n%s", body)
	}
	if !strings.Contains(body, `href="/calendar/`+itoa(b)+`"`) || !strings.Contains(body, "Concert") {
		t.Errorf("the other booking asking for chairs should be linked:\n%s", body)
	}
	if body := c.as("mel").get("/calendar/"+itoa(a), 200); strings.Contains(body, "short by") || strings.Contains(body, "Concert") {
		t.Errorf("the owner shouldn't see shortages or who else asked:\n%s", body)
	}
}

func TestInUseToggleSchedulersOnly(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Folding chairs", store.ModeCount, 40, hall)
	sound := addEquipment(t, st, "Sound", store.ModeService, 0, hall)
	mel := mustUserID(t, st, "mel")
	day := soon(-3) // already over
	id := bookWithEquipment(t, st, "Supper", day, mel, hall, chairs, 12)
	st.SetBookingEquipment(id, []store.EquipmentLine{{EquipmentID: chairs, Quantity: 12}, {EquipmentID: sound, Quantity: 1}})
	page := "/calendar/" + itoa(id)
	toggle := page + "/equipment/" + itoa(chairs) + "/in-use"

	body := sam.get(page, 200)
	if !strings.Contains(body, "Mark in use") || strings.Contains(body, "/equipment/"+itoa(sound)+"/in-use") {
		t.Errorf("scheduler should get a toggle on the counted line only:\n%s", body)
	}
	if !strings.Contains(body, "40 in storage") && !strings.Contains(body, "In storage") {
		t.Errorf("nothing is in use: %s", body)
	}
	melC := c.as("mel")
	if body := melC.get(page, 200); strings.Contains(body, "Mark in use") || strings.Contains(body, "/in-use") {
		t.Error("the owner shouldn't get the toggle")
	}
	melC.post("/my/bookings", toggle, url.Values{"in_use": {"1"}}, 403)
	c.post("/request", toggle, url.Values{"in_use": {"1"}}, 200) // a visitor lands on the sign-in page
	if lines, _ := st.BookingEquipment(id); lines[0].InUse {
		t.Fatal("a non-scheduler turned it on")
	}

	// A scheduler turns it on; it goes back to where they were (local paths only).
	sam.post(page, toggle, url.Values{"in_use": {"1"}, "back": {"//evil.example"}}, 200)
	lines, _ := st.BookingEquipment(id)
	if !lines[0].InUse || lines[0].InUseChangedBy == 0 {
		t.Fatalf("lines = %+v", lines)
	}
	// The booking ended days ago, but it stays in use.
	body = sam.get(page, 200)
	if !strings.Contains(body, "Put in storage") || !strings.Contains(body, "In use in Hall") || !strings.Contains(body, "28 in storage") {
		t.Errorf("booking page after turning it on:\n%s", body)
	}
	if body := sam.get("/setups", 200); !strings.Contains(body, "Still set up") || !strings.Contains(body, "Folding chairs") ||
		!strings.Contains(body, "12 in Hall") || !strings.Contains(body, "Put in storage") {
		t.Errorf("/setups should list it under Still set up:\n%s", body)
	}
	admin := mustAdmin(t, c, st)
	if body := admin.get("/equipment", 200); !strings.Contains(body, "In use in Hall") || !strings.Contains(body, "28 in storage") {
		t.Errorf("/equipment where now:\n%s", body)
	}

	// Services have no toggle; back to storage empties Still set up.
	sam.post(page, page+"/equipment/"+itoa(sound)+"/in-use", url.Values{"in_use": {"1"}}, 404)
	sam.post("/setups", toggle, url.Values{"in_use": {"0"}, "back": {"/setups"}}, 200)
	if body := sam.get("/setups", 200); !strings.Contains(body, "Nothing is marked In use") {
		t.Errorf("after putting it away:\n%s", body)
	}
}

func mustAdmin(t *testing.T, c *client, st *store.Store) *client {
	t.Helper()
	st.CreateUser("ada", "Ada", "a long password", store.RoleAdmin)
	return c.as("ada")
}

func TestSetupsListsTheNextTwoWeeks(t *testing.T) {
	_, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Folding chairs", store.ModeCount, 40, hall)
	bookWithEquipment(t, st, "Soon supper", soon(2), 0, hall, chairs, 10)
	bookWithEquipment(t, st, "Far supper", soon(30), 0, hall, chairs, 10)
	book(t, st, store.Booking{Title: "Plain meeting", StartsOn: soon(2), StartTime: "09:00", EndTime: "10:00", Visibility: "public"}, hall)
	body := sam.get("/setups", 200)
	if !strings.Contains(body, "Soon supper") || strings.Contains(body, "Far supper") || strings.Contains(body, "Plain meeting") {
		t.Errorf("setups:\n%s", body)
	}
	if !strings.Contains(body, `href="/setups"`) {
		t.Error("the menu should link to Setups")
	}
}

func TestToggleOffWithTheTracker(t *testing.T) {
	f := &equipTracker{
		places:   strings.Join([]string{syncSite, syncMain, syncNursery}, ","),
		products: tablesProduct,
		items: map[string]string{"7": `{"id": 1, "product_id": 7, "quantity": 30, "place": {"id": 4, "path": "Campus › Main building › 104 – Nursery"}},
			{"id": 2, "product_id": 7, "quantity": 20, "place": {"id": 99, "path": "Campus › Storage B"}}`},
	}
	srv := f.server(t)
	st := srv.store
	if _, err := srv.SyncTracker(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := startServer(t, f.config(), st)
	st.CreateUser("sam", "Sam", "a long password", store.RoleScheduler)
	sam := c.as("sam")
	nursery := roomNamed(t, st, "104 – Nursery")
	tables := equipmentNamed(t, st, "Round tables")
	id := bookWithEquipment(t, st, "Supper", soon(2), 0, nursery.ID, tables.ID, 10)
	page := "/calendar/" + itoa(id)

	body := sam.get(page, 200)
	if strings.Contains(body, "Mark in use") || strings.Contains(body, "Put in storage") || strings.Contains(body, "/in-use") {
		t.Errorf("no toggle with the tracker on:\n%s", body)
	}
	if !strings.Contains(body, `30 in <a href="/rooms/`) || !strings.Contains(body, "20 in Campus › Storage B") || !strings.Contains(body, "set in the tracker") {
		t.Errorf("places from the tracker:\n%s", body)
	}
	sam.post(page, page+"/equipment/"+itoa(tables.ID)+"/in-use", url.Values{"in_use": {"1"}}, 403)
	if lines, _ := st.BookingEquipment(id); lines[0].InUse {
		t.Fatal("turned on with the tracker connected")
	}
	if body := sam.get("/setups", 200); strings.Contains(body, "/in-use") || !strings.Contains(body, "30 in") || !strings.Contains(body, `/rooms/`+itoa(nursery.ID)) {
		t.Errorf("/setups with the tracker: equipment sitting in a calendar room:\n%s", body)
	}
	admin := mustAdmin(t, c, st)
	if body := admin.get("/equipment", 200); !strings.Contains(body, "30 in") || !strings.Contains(body, "Storage B") {
		t.Errorf("/equipment places:\n%s", body)
	}
}

func TestShortFilterOnRequests(t *testing.T) {
	_, sam, st := schedulerClient(t)
	hall, annex, lounge := addRoom(t, st, "Hall"), addRoom(t, st, "Annex"), addRoom(t, st, "Lounge")
	chairs := addEquipment(t, st, "Folding chairs", store.ModeCount, 40, hall, annex, lounge)
	day := soon(3)
	bookWithEquipment(t, st, "Supper", day, 0, hall, chairs, 30)
	bookWithEquipment(t, st, "Concert", day, 0, annex, chairs, 20)
	bookWithEquipment(t, st, "Fine meeting", soon(9), 0, lounge, chairs, 10)

	body := sam.get("/requests?status=short", 200)
	if !strings.Contains(body, "Supper") || !strings.Contains(body, "Concert") || strings.Contains(body, "Fine meeting") {
		t.Errorf("short list:\n%s", body)
	}
	if !strings.Contains(body, "short by 10") {
		t.Errorf("how short:\n%s", body)
	}
	if body := sam.get("/requests", 200); !strings.Contains(body, `href="/requests?status=short"`) {
		t.Error("the Short tab is missing")
	}
}
