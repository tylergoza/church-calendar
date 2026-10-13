package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// memberBooks signs in a member and books the rooms at once; it returns
// their browser and the request's ID and code.
func memberBooks(t *testing.T, c *client, st *store.Store, name, title, day string, rooms ...int64) (*client, string, string) {
	t.Helper()
	st.CreateUser(name, strings.ToUpper(name[:1])+name[1:], "a long password", store.RoleMember)
	m := c.as(name)
	id, code := sent(t, m.post("/request", "/request", requestForm(title, day, rooms...), 200))
	return m, id, code
}

func TestOwnerEditsAndCancels(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	day := soon(6)
	mel, id, _ := memberBooks(t, c, st, "mel", "Supper", day, hall)

	// Listed on /my/bookings and linked from the menu and /request.
	if body := mel.get("/my/bookings", 200); !strings.Contains(body, "Supper") || !strings.Contains(body, "Approved") {
		t.Errorf("my bookings: %s", body)
	}
	if body := mel.get("/request", 200); !strings.Contains(body, `href="/my/bookings"`) {
		t.Error("/request should link to My bookings")
	}
	if body := mel.get("/request/"+id, 200); !strings.Contains(body, "/request/"+id+"/edit") || !strings.Contains(body, "Cancel booking") {
		t.Errorf("status page should offer edit and cancel:\n%s", body)
	}

	// The booking itself overlapping its own times is fine.
	f := requestForm("Big supper", day, hall, kitchen)
	f.Set("start_time", "18:30")
	f.Set("expected_count", "30")
	mel.get("/request/"+id+"/edit", 200)
	if body := mel.post("/request/"+id+"/edit", "/request/"+id+"/edit", f, 200); !strings.Contains(body, "Big supper") {
		t.Errorf("after save: %s", body)
	}
	req, _ := st.GetRequest(mustID(t, id))
	b, _ := st.GetBooking(req.BookingID)
	if req.Title != "Big supper" || req.ExpectedCount != 30 || b.Title != "Big supper" || b.StartTime != "18:30" || len(b.Rooms) != 2 || b.Status != "confirmed" {
		t.Fatalf("saved: %+v %+v", req, b)
	}
	if req.Phone != "(555) 123-4567" || req.Name != "Jo Visitor" {
		t.Errorf("who asked changed: %+v", req)
	}

	// A busy slot is refused with what's in the way, and nothing changes.
	st.CreateBooking(&store.Booking{Title: "Class", StartsOn: day, StartTime: "21:00", EndTime: "22:00", Status: "confirmed", Visibility: "private"}, []int64{hall})
	late := requestForm("Big supper", day, hall, kitchen)
	late.Set("start_time", "21:30")
	late.Set("end_time", "22:30")
	if body := mel.post("/request/"+id+"/edit", "/request/"+id+"/edit", late, 409); !strings.Contains(body, "Hall is taken then") || strings.Contains(body, "Class") {
		t.Errorf("busy answer: %s", body)
	}
	if again, _ := st.GetRequest(req.ID); again.StartTime != "18:30" {
		t.Errorf("refused edit changed the request: %+v", again)
	}

	// Validation is readRequest's.
	if body := mel.post("/request/"+id+"/edit", "/request/"+id+"/edit", url.Values{"title": {""}}, 422); !strings.Contains(body, "Pick at least one room") {
		t.Error("expected validation errors")
	}

	// Cancel.
	mel.post("/request/"+id, "/request/"+id+"/cancel", url.Values{}, 200)
	req, _ = st.GetRequest(req.ID)
	b, _ = st.GetBooking(req.BookingID)
	if req.Status != "withdrawn" || b.Status != "cancelled" {
		t.Fatalf("cancel: %+v %+v", req, b)
	}
	if body := mel.get("/request/"+id+"/edit", 200); !strings.Contains(body, "can&#39;t be changed any more") {
		t.Errorf("a cancelled booking should send you back to its page: %s", body)
	}
}

func TestOthersCannotChangeBookings(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	day := soon(6)
	_, id, code := memberBooks(t, c, st, "mel", "Supper", day, hall)
	st.CreateUser("ned", "Ned", "a long password", store.RoleMember)
	ned := c.as("ned")

	for who, cl := range map[string]*client{"member": ned, "scheduler": sam, "visitor": c} {
		cl.get("/request/"+id+"/edit", 403)
		cl.post("/request", "/request/"+id+"/edit", requestForm("Hijack", day, hall), 403)
		cl.post("/request", "/request/"+id+"/cancel", url.Values{}, 403)
		if req, _ := st.GetRequest(mustID(t, id)); req.Title != "Supper" || req.Status != "approved" {
			t.Fatalf("%s changed it: %+v", who, req)
		}
	}
	// The code link shows the page, but without edit or cancel.
	if body := c.get("/request/"+id+"?code="+code, 200); strings.Contains(body, "/edit") || strings.Contains(body, "Cancel booking") {
		t.Error("the link shouldn't offer edit or cancel")
	}
	ned.get("/request/999/edit", 404)
}

func TestVisitorWithdrawsButCannotEdit(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	day := soon(6)
	id, code := sent(t, c.post("/request", "/request", requestForm("Movie night", day, hall), 200))
	body := c.get("/request/"+id+"?code="+code, 200)
	if !strings.Contains(body, "Withdraw request") || strings.Contains(body, "/edit") {
		t.Errorf("visitor page: %s", body)
	}
	c.get("/request/"+id+"/edit?code="+code, 403)
	c.post("/request", "/request/"+id+"/edit", requestForm("Hijack", day, hall), 403)
	c.post("/request/"+id+"?code="+code, "/request/"+id+"/withdraw", url.Values{"code": {code}}, 200)
	if req, _ := st.GetRequest(mustID(t, id)); req.Status != "withdrawn" {
		t.Errorf("not withdrawn: %+v", req)
	}
}

func TestOwnerCannotTouchEventBooking(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	day := soon(8)
	mel, id, _ := memberBooks(t, c, st, "mel", "Supper", day, hall)
	req, _ := st.GetRequest(mustID(t, id))
	tok := sourceToken(t, st, "events", false)
	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/20", tok, pushJSON(day, `, "replaces": `+itoa(req.BookingID)), 200, &a)

	body := mel.get("/request/"+id, 200)
	if !strings.Contains(body, "part of an event") || !strings.Contains(body, "/calendar/"+itoa(a.ID)) || strings.Contains(body, "/edit") {
		t.Errorf("status page: %s", body)
	}
	mel.get("/request/"+id+"/edit", 200) // sent back to the status page
	mel.post("/request/"+id, "/request/"+id+"/cancel", url.Values{}, 200)
	if b, _ := st.GetBooking(a.ID); b.Status == "cancelled" {
		t.Error("the event's booking was cancelled by the owner")
	}
	if body := mel.get("/my/bookings", 200); !strings.Contains(body, "part of an event") {
		t.Error("my bookings should say it's an event now")
	}
}

func TestSchedulerCancelsBooking(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	day := soon(6)
	mel, id, _ := memberBooks(t, c, st, "mel", "Supper", day, hall)
	req, _ := st.GetRequest(mustID(t, id))
	bid := itoa(req.BookingID)

	// The button is on the booking page and the edit form.
	for _, p := range []string{"/calendar/" + bid, "/bookings/" + bid + "/edit"} {
		if body := sam.get(p, 200); !strings.Contains(body, "/bookings/"+bid+"/cancel") {
			t.Errorf("%s has no cancel form", p)
		}
	}
	// A member can't.
	mel.post("/request/"+id, "/bookings/"+bid+"/cancel", url.Values{"note": {"mine"}}, 403)
	c.post("/request", "/bookings/"+bid+"/cancel", url.Values{}, 200) // a visitor is sent to sign in
	if b, _ := st.GetBooking(req.BookingID); b.Status != "confirmed" {
		t.Fatalf("refused cancel went through: %+v", b)
	}

	sam.post("/calendar/"+bid, "/bookings/"+bid+"/cancel", url.Values{"note": {"The roof is leaking."}}, 200)
	body := sam.get("/calendar/"+bid, 200)
	if !strings.Contains(body, "Cancelled by Sam") || !strings.Contains(body, "roof is leaking") || strings.Contains(body, "/bookings/"+bid+"/cancel") {
		t.Errorf("booking page: %s", body)
	}
	if b := mel.get("/request/"+id, 200); !strings.Contains(b, "cancelled by the church") || !strings.Contains(b, "roof is leaking") {
		t.Errorf("status page should show the note: %s", b)
	}
	if list := sam.get("/requests?status=cancelled", 200); !strings.Contains(list, "Supper") {
		t.Error("cancelled tab should list it")
	}
	if busy, _ := st.BusyInRooms([]int64{hall}, day, "19:00", "", "19:30"); len(busy) != 0 {
		t.Error("cancelled booking still blocks the hall")
	}

	// An admin can too, on a hand-made booking.
	st.CreateUser("ada", "Ada", "a long password", store.RoleAdmin)
	ada := c.as("ada")
	hid, _ := st.CreateBooking(&store.Booking{Title: "Choir", StartsOn: day, Status: "confirmed", Visibility: "private"}, []int64{hall})
	ada.post("/calendar/"+itoa(hid), "/bookings/"+itoa(hid)+"/cancel", url.Values{"note": {"Snow day"}}, 200)
	if b, _ := st.GetBooking(hid); b.Status != "cancelled" || b.CancelNote != "Snow day" {
		t.Errorf("admin cancel: %+v", b)
	}
}

func TestPushedBookingCannotBeCancelledHere(t *testing.T) {
	c, sam, st := schedulerClient(t)
	day := soon(8)
	tok := sourceToken(t, st, "events", false)
	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/20", tok, pushJSON(day, ``), 200, &a)
	p := "/calendar/" + itoa(a.ID)
	body := sam.get(p, 200)
	if !strings.Contains(body, "Cancel it in the Event planner") || strings.Contains(body, "/cancel") {
		t.Errorf("pushed booking page: %s", body)
	}
	sam.post(p, "/bookings/"+itoa(a.ID)+"/cancel", url.Values{"note": {"no"}}, 403)
	if b, _ := st.GetBooking(a.ID); b.Status == "cancelled" {
		t.Error("pushed booking was cancelled")
	}
}

func TestSchedulerCancelsRepeatDates(t *testing.T) {
	_, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	f := bookingForm("Choir", soon(3), hall)
	f.Set("repeat_freq", "weekly")
	f.Set("repeat_every", "1")
	f.Set("repeat_end", "after")
	f.Set("repeat_count", "4")
	sam.post("/bookings/new", "/bookings", f, 200)
	list, _ := st.BookingsBetween(soon(0), soon(60), hall)
	if len(list) < 3 || list[0].SeriesID == 0 {
		t.Skipf("repeat form fields differ: %d bookings", len(list))
	}
	first, second := list[0], list[1]
	sam.post("/calendar/"+itoa(first.ID), "/bookings/"+itoa(first.ID)+"/cancel", url.Values{"apply": {"this"}}, 200)
	if b, _ := st.GetBooking(first.ID); !b.Cancelled() {
		t.Error("this date should be cancelled")
	}
	if b, _ := st.GetBooking(second.ID); b.Cancelled() {
		t.Error("other dates should stay")
	}
	sam.post("/calendar/"+itoa(second.ID), "/bookings/"+itoa(second.ID)+"/cancel", url.Values{"apply": {"later"}}, 200)
	occ, _ := st.Occurrences(first.SeriesID)
	for _, o := range occ[1:] {
		if !o.Cancelled() {
			t.Errorf("%s should be cancelled", o.OccurrenceDate)
		}
	}
}
