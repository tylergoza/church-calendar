package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

func addEquipment(t *testing.T, st *store.Store, name, mode string, total int, rooms ...int64) int64 {
	t.Helper()
	id, err := st.CreateEquipment(&store.Equipment{Name: name, Mode: mode, Total: total, RoomIDs: rooms})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func withEquip(f url.Values, id int64, qty string) url.Values {
	f.Set("equip_"+itoa(id), qty)
	return f
}

func TestEquipmentOffersMatchPickedRooms(t *testing.T) {
	c, _, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	mel := c.as("mel")
	annex, _ := st.CreateRoom(&store.Room{Name: "Annex", Bookable: true})
	nursery, _ := st.CreateRoom(&store.Room{Name: "Nursery", Bookable: true, ParentID: annex})
	forBuilding := addEquipment(t, st, "Tables", store.ModeCount, 10, annex)
	forRoom := addEquipment(t, st, "Cribs", store.ModeCount, 4, nursery)

	// Without JavaScript the form lists everything offered anywhere.
	body := mel.get("/request", 200)
	if !strings.Contains(body, "Tables") || !strings.Contains(body, "Cribs") || !strings.Contains(body, "Equipment and help") {
		t.Errorf("form: %s", body)
	}

	// The building booked whole gets what's offered with the building only.
	f := withEquip(withEquip(requestForm("Lunch", soon(5), annex), forBuilding, "3"), forRoom, "2")
	mel.post("/request", "/request", f, 200)
	reqs, _ := st.RequestsBy(mustUser(t, st, "mel"))
	lines, _ := st.RequestEquipment(reqs[0].ID)
	if len(lines) != 1 || lines[0].EquipmentID != forBuilding || lines[0].Quantity != 3 {
		t.Fatalf("whole building lines = %+v", lines)
	}
	// A room inside it gets only what's offered with that room.
	f = withEquip(withEquip(requestForm("Crèche", soon(6), nursery), forBuilding, "3"), forRoom, "2")
	mel.post("/request", "/request", f, 200)
	reqs, _ = st.RequestsBy(mustUser(t, st, "mel"))
	for _, rq := range reqs {
		if rq.Title == "Crèche" {
			lines, _ = st.RequestEquipment(rq.ID)
		}
	}
	if len(lines) != 1 || lines[0].EquipmentID != forRoom || lines[0].Quantity != 2 {
		t.Fatalf("room lines = %+v", lines)
	}
}

func mustUser(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	list, err := st.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range list {
		if u.Username == name {
			return u.ID
		}
	}
	t.Fatalf("no user %s", name)
	return 0
}

func TestMemberOverTheTotalIsBookedAndFlagged(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Chairs", store.ModeCount, 40, hall)
	sound := addEquipment(t, st, "Sound", store.ModeService, 0, hall)
	day := soon(5)

	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	mel := c.as("mel")
	f := withEquip(withEquip(requestForm("Concert", day, hall), chairs, "30"), sound, "1")
	f.Set("equip_note_"+itoa(sound), "two mics")
	if body := mel.post("/request", "/request", f, 200); strings.Contains(body, "short of") {
		t.Errorf("30 of 40 should not be short: %s", body)
	}

	// Another booking at the same time, over the total.
	other := withEquip(withEquip(requestForm("Lecture", day, addRoom(t, st, "Annex")), chairs, "1"), sound, "1")
	// Chairs aren't offered in the Annex, so the line is ignored: offer them there too.
	annex := other["room"][0]
	annexID := mustID(t, annex)
	e, _ := st.GetEquipment(chairs)
	e.RoomIDs = append(e.RoomIDs, annexID)
	st.UpdateEquipment(e)
	other = withEquip(requestForm("Lecture", day, annexID), chairs, "20")
	body := mel.post("/request", "/request", other, 200)
	if !strings.Contains(body, "We may be short of Chairs; a scheduler will be in touch.") {
		t.Errorf("expected the short note: %s", body)
	}
	id, _ := sent(t, body)
	req, _ := st.GetRequest(mustID(t, id))
	if req.Status != "approved" {
		t.Errorf("status = %q, want booked", req.Status)
	}
	lines, _ := st.BookingEquipment(req.BookingID)
	if len(lines) != 1 || lines[0].Short != 10 {
		t.Errorf("lines = %+v", lines)
	}
	// The service never runs short, and a note is kept.
	first, _ := st.RequestsBy(mustUser(t, st, "mel"))
	for _, rq := range first {
		if rq.Title == "Concert" {
			ls, _ := st.BookingEquipment(rq.BookingID)
			for _, l := range ls {
				if l.EquipmentID == sound && (l.Short != 0 || l.Note != "two mics") {
					t.Errorf("service line = %+v", l)
				}
			}
		}
	}
}

func TestEquipmentChecks(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Chairs", store.ModeCount, 40, hall)
	sound := addEquipment(t, st, "Sound", store.ModeService, 0, hall)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	mel := c.as("mel")
	day := soon(5)

	// More than there are: refused, values kept.
	body := mel.post("/request", "/request", withEquip(requestForm("Big", day, hall), chairs, "41"), 422)
	if !strings.Contains(body, "There are only 40 of Chairs") || !strings.Contains(body, `value="41"`) {
		t.Errorf("over the total: %s", body)
	}
	// A long note is refused.
	f := withEquip(requestForm("Noisy", day, hall), sound, "1")
	f.Set("equip_note_"+itoa(sound), strings.Repeat("x", 201))
	if body := mel.post("/request", "/request", f, 422); !strings.Contains(body, "under 200 characters") {
		t.Errorf("long note: %s", body)
	}
	// Nothing picked, and 0, are fine.
	mel.post("/request", "/request", withEquip(requestForm("Plain", day, hall), chairs, "0"), 200)
	// A busy room is still refused whatever the equipment.
	if body := mel.post("/request", "/request", withEquip(requestForm("Clash", day, hall), chairs, "5"), 409); !strings.Contains(body, "Hall is taken then") {
		t.Errorf("busy: %s", body)
	}
}

func TestVisitorRequestWithEquipmentIsPending(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Chairs", store.ModeCount, 10, hall)
	body := c.post("/request", "/request", withEquip(requestForm("Party", soon(5), hall), chairs, "12"), 422)
	if !strings.Contains(body, "There are only 10") {
		t.Fatalf("expected refusal: %s", body)
	}
	body = c.post("/request", "/request", withEquip(requestForm("Party", soon(5), hall), chairs, "8"), 200)
	id, _ := sent(t, body)
	req, _ := st.GetRequest(mustID(t, id))
	if req.Status != "waiting" {
		t.Errorf("status = %q", req.Status)
	}
	if lines, _ := st.BookingEquipment(req.BookingID); len(lines) != 1 || lines[0].Quantity != 8 {
		t.Errorf("lines = %+v", lines)
	}
}

func leftOf(t *testing.T, body string, id int64) int {
	t.Helper()
	var out availability
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	e, ok := out.Equipment[itoa(id)]
	if !ok {
		t.Fatalf("no equipment %d in %s", id, body)
	}
	return e.Left
}

func TestEquipmentAvailabilityCounts(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall, annex := addRoom(t, st, "Hall"), addRoom(t, st, "Annex")
	chairs := addEquipment(t, st, "Chairs", store.ModeCount, 40, hall, annex)
	sound := addEquipment(t, st, "Sound", store.ModeService, 0, hall)
	day := soon(4)
	path := "/request/availability?starts_on=" + day + "&start_time=18:00&end_time=20:00"

	if body := c.get(path, 200); leftOf(t, body, chairs) != 40 || strings.Contains(body, `"equipment":{"1":{"total":40,"left":40},"`) || strings.Contains(body, `"`+itoa(sound)+`":{"total"`) {
		t.Errorf("empty: %s", body)
	}
	mk := func(status string, qty int, room int64, start string) int64 {
		b := store.Booking{Title: "x", StartsOn: day, StartTime: start, EndTime: "20:00", Status: status, Visibility: "private"}
		id, err := st.CreateBooking(&b, []int64{room})
		if err != nil {
			t.Fatal(err)
		}
		st.SetBookingEquipment(id, []store.EquipmentLine{{EquipmentID: chairs, Quantity: qty}, {EquipmentID: sound, Quantity: 1}})
		return id
	}
	pending := mk("pending", 5, hall, "18:00")
	mk("tentative", 7, annex, "19:00")
	cancelled := mk("confirmed", 20, annex, "18:00")
	if body := c.get(path, 200); leftOf(t, body, chairs) != 8 {
		t.Errorf("all three: %s", body)
	}
	if err := st.CancelBooking(cancelled, 0, ""); err != nil {
		t.Fatal(err)
	}
	if body := c.get(path, 200); leftOf(t, body, chairs) != 28 {
		t.Errorf("cancelled should free it: %s", body)
	}
	// Editing a booking doesn't count itself.
	if body := c.get(path+"&except="+itoa(pending), 200); leftOf(t, body, chairs) != 33 {
		t.Errorf("except: %s", body)
	}
	// The edit form shows the same.
	if body := sam.get("/bookings/"+itoa(pending)+"/edit", 200); !strings.Contains(body, "33 of 40 not asked for then") {
		t.Errorf("edit form: %s", body)
	}
}

func TestHandMadeBookingFormSavesEquipment(t *testing.T) {
	_, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Chairs", store.ModeCount, 40, hall)
	if body := sam.get("/bookings/new", 200); !strings.Contains(body, "Equipment and help") {
		t.Fatalf("new form: %s", body)
	}
	f := withEquip(bookingForm("Rehearsal", soon(3), hall), chairs, "12")
	sam.post("/bookings/new", "/bookings", f, 200)
	list, _ := st.BookingsBetween(soon(3), soon(3), 0)
	if len(list) != 1 {
		t.Fatalf("%d bookings", len(list))
	}
	id := list[0].ID
	if lines, _ := st.BookingEquipment(id); len(lines) != 1 || lines[0].Quantity != 12 {
		t.Fatalf("lines = %+v", lines)
	}
	// Edit changes it; clearing removes it.
	f = withEquip(bookingForm("Rehearsal", soon(3), hall), chairs, "15")
	sam.post("/bookings/"+itoa(id)+"/edit", "/bookings/"+itoa(id), f, 200)
	if lines, _ := st.BookingEquipment(id); len(lines) != 1 || lines[0].Quantity != 15 {
		t.Fatalf("after edit = %+v", lines)
	}
	sam.post("/bookings/"+itoa(id)+"/edit", "/bookings/"+itoa(id), bookingForm("Rehearsal", soon(3), hall), 200)
	if lines, _ := st.BookingEquipment(id); len(lines) != 0 {
		t.Fatalf("after clearing = %+v", lines)
	}
}

func TestOwnerEditsEquipment(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	chairs := addEquipment(t, st, "Chairs", store.ModeCount, 40, hall)
	day := soon(6)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	mel := c.as("mel")
	id, _ := sent(t, mel.post("/request", "/request", withEquip(requestForm("Supper", day, hall), chairs, "10"), 200))
	if body := mel.get("/request/"+id+"/edit", 200); !strings.Contains(body, `value="10"`) || !strings.Contains(body, "40 of 40") {
		t.Errorf("edit form should prefill and not count itself: %s", body)
	}
	mel.post("/request/"+id+"/edit", "/request/"+id+"/edit", withEquip(requestForm("Supper", day, hall), chairs, "45"), 422)
	mel.post("/request/"+id+"/edit", "/request/"+id+"/edit", withEquip(requestForm("Supper", day, hall), chairs, "25"), 200)
	req, _ := st.GetRequest(mustID(t, id))
	if lines, _ := st.RequestEquipment(req.ID); len(lines) != 1 || lines[0].Quantity != 25 {
		t.Errorf("request lines = %+v", lines)
	}
	if lines, _ := st.BookingEquipment(req.BookingID); len(lines) != 1 || lines[0].Quantity != 25 {
		t.Errorf("booking lines = %+v", lines)
	}
}
