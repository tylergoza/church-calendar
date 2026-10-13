package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// apiDo sends method to path with a bearer token and a JSON body ("" for
// none), and decodes the answer into out when given.
func (c *client) apiDo(method, path, token, body string, wantStatus int, out any) string {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: status %d, want %d\n%s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, raw)
		}
	}
	return string(raw)
}

var regexpToken = regexp.MustCompile(`cal_[A-Za-z0-9_-]{20,}`)

type putAnswer struct {
	ID        int64
	Created   bool
	Replaced  int64
	Booking   apiSourceBookingJSON
	Conflicts []apiBlockJSON
}

func sourceToken(t *testing.T, st *store.Store, source string, seePrivate bool) string {
	t.Helper()
	tok, err := st.CreateSourceToken("App", source, seePrivate, 0)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func pushJSON(day string, extra string) string {
	return `{"title": "Fall Dinner", "kind": "Fellowship", "starts_on": "` + day + `", "start_time": "17:30", "end_time": "20:00",
		"rooms": [{"place_id": 41, "name": "Big room"}, {"name": "pavilion"}, {"name": "Shed"}],
		"visibility": "public", "description": "Potluck.", "url": "https://events.example.org/events/12"` + extra + `}`
}

func TestPushIdempotentAndRooms(t *testing.T) {
	c, st := newTestServer(t)
	life := addRoom(t, st, "Lifecenter")
	st.DB.Exec(`UPDATE rooms SET tracker_place_id = 41 WHERE id = ?`, life)
	pav := addRoom(t, st, "Pavilion")
	tok := sourceToken(t, st, "events", false)
	day := soon(5)

	var first, second putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/12", tok, pushJSON(day, ""), 200, &first)
	c.apiDo("PUT", "/api/v1/bookings/events/12", tok, pushJSON(day, ""), 200, &second)
	if !first.Created || second.Created || first.ID != second.ID {
		t.Fatalf("first %+v, second %+v", first, second)
	}
	var n int
	st.DB.QueryRow(`SELECT count(*) FROM bookings`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d bookings after two identical pushes", n)
	}
	b, _ := st.GetBooking(first.ID)
	if b.Source != "events" || b.SourceID != "12" || b.Title != "Fall Dinner" || b.Status != "confirmed" {
		t.Fatalf("stored %+v", b)
	}
	// Matched by place ID (under the calendar's name), by name in any case,
	// and an unknown place kept by name with no room.
	got := map[string]int64{}
	for _, r := range b.Rooms {
		got[r.Name] = r.RoomID
	}
	if got["Lifecenter"] != life || got["Pavilion"] != pav || got["Shed"] != 0 || len(got) != 3 {
		t.Fatalf("rooms %+v", b.Rooms)
	}

	// The app's page link goes through by-source.
	c.get("/calendar/by-source/events/12", 200)

	// A change replaces, rooms and all.
	c.apiDo("PUT", "/api/v1/bookings/events/12", tok, `{"title": "Moved", "starts_on": "`+day+`", "rooms": [{"name": "Pavilion"}], "visibility": "public"}`, 200, nil)
	b, _ = st.GetBooking(first.ID)
	if b.Title != "Moved" || len(b.Rooms) != 1 || b.StartTime != "" {
		t.Fatalf("after change %+v", b)
	}

	// Delete, and again: both 204.
	c.apiDo("DELETE", "/api/v1/bookings/events/12", tok, "", 204, nil)
	c.apiDo("DELETE", "/api/v1/bookings/events/12", tok, "", 204, nil)
	if _, err := st.GetBooking(first.ID); err != store.ErrNotFound {
		t.Fatalf("still there: %v", err)
	}
}

func TestPushTokens(t *testing.T) {
	c, st := newTestServer(t)
	events := sourceToken(t, st, "events", false)
	read, _ := st.CreateReadToken("Website", true, 0)
	body := pushJSON(soon(3), "")

	c.apiDo("PUT", "/api/v1/bookings/events/1", "", body, 401, nil)
	c.apiDo("PUT", "/api/v1/bookings/events/1", "cal_nope", body, 401, nil)
	c.apiDo("PUT", "/api/v1/bookings/events/1", read, body, 403, nil)
	c.apiDo("PUT", "/api/v1/bookings/productions/1", events, body, 403, nil)
	c.apiDo("DELETE", "/api/v1/bookings/productions/1", events, "", 403, nil)
	c.apiDo("GET", "/api/v1/bookings/productions", events, "", 403, nil)
	c.apiDo("PUT", "/api/v1/bookings/calendar/1", events, body, 403, nil)
	// Source tokens still can't use the read API.
	c.apiGet("/api/v1/events", events, 403)

	// An app can't delete a hand-made booking through its source.
	id := book(t, st, store.Booking{Title: "Choir", StartsOn: soon(3)})
	c.apiDo("DELETE", "/api/v1/bookings/events/"+itoa(id), events, "", 204, nil)
	if _, err := st.GetBooking(id); err != nil {
		t.Fatal("hand-made booking was deleted")
	}
}

func TestPushValidation(t *testing.T) {
	c, st := newTestServer(t)
	tok := sourceToken(t, st, "events", false)
	day := soon(3)

	var bad struct {
		Error  string
		Fields map[string]string
	}
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, `{"starts_on": "soon", "start_time": "6pm", "visibility": "secret", "url": "javascript:alert(1)"}`, 400, &bad)
	for _, f := range []string{"starts_on", "start_time", "visibility", "url"} {
		if bad.Fields[f] == "" || !strings.Contains(bad.Error, f+":") {
			t.Errorf("no message for %s: %+v", f, bad)
		}
	}
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, `not json`, 400, nil)
	c.apiDo("PUT", "/api/v1/bookings/events/has%20space", tok, pushJSON(day, ""), 400, nil)

	// Times the event planner's form allows are tidied, not refused: an end
	// with no start is all day; an end before the start is dropped.
	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/2", tok, `{"title": "A", "starts_on": "`+day+`", "end_time": "10:00", "ends_on": "`+day+`"}`, 200, &a)
	if a.Booking.EndTime != "" || a.Booking.EndsOn != "" || a.Booking.Visibility != "private" {
		t.Errorf("tidied %+v", a.Booking)
	}
	c.apiDo("PUT", "/api/v1/bookings/events/3", tok, `{"title": "B", "starts_on": "`+day+`", "start_time": "22:00", "end_time": "01:00"}`, 200, &a)
	if a.Booking.StartTime != "22:00" || a.Booking.EndTime != "" {
		t.Errorf("tidied %+v", a.Booking)
	}
}

func TestPushLockedHasNoDetails(t *testing.T) {
	c, st := newTestServer(t)
	tok := sourceToken(t, st, "events", false)
	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/9", tok, pushJSON(soon(2), `, "visibility": "locked"`), 200, &a)
	b, _ := st.GetBooking(a.ID)
	if b.Visibility != "locked" || b.Title != "" || b.Kind != "" || b.Description != "" {
		t.Fatalf("locked booking kept details: %+v", b)
	}
}

func TestPushConflicts(t *testing.T) {
	c, st := newTestServer(t)
	hall := addRoom(t, st, "Hall")
	day := soon(6)
	book(t, st, store.Booking{Title: "Elders", Visibility: "private", StartsOn: day, StartTime: "18:00", EndTime: "19:00"}, hall)
	book(t, st, store.Booking{Title: "Open night", Visibility: "public", StartsOn: day, StartTime: "19:30", EndTime: "21:00"}, hall)
	book(t, st, store.Booking{Title: "Morning", Visibility: "public", StartsOn: day, StartTime: "08:00", EndTime: "09:00"}, hall)
	body := `{"title": "Dinner", "starts_on": "` + day + `", "start_time": "17:30", "end_time": "20:00", "rooms": [{"name": "Hall"}], "visibility": "public"}`

	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/1", sourceToken(t, st, "events", false), body, 200, &a)
	if len(a.Conflicts) != 2 {
		t.Fatalf("conflicts %+v", a.Conflicts)
	}
	for _, k := range a.Conflicts {
		switch {
		case k.Busy && (k.ID != 0 || k.Title != "" || k.Source != ""):
			t.Errorf("busy conflict carries details: %+v", k)
		case !k.Busy && k.Title != "Open night":
			t.Errorf("only the public one shows in full: %+v", k)
		}
	}
	// A token that may see private bookings sees both in full.
	c.apiDo("PUT", "/api/v1/bookings/events/1", sourceToken(t, st, "events", true), body, 200, &a)
	if len(a.Conflicts) != 2 || a.Conflicts[0].Busy || a.Conflicts[1].Busy {
		t.Fatalf("conflicts %+v", a.Conflicts)
	}
	// The clash is on /conflicts for schedulers too, nothing was refused.
	if cs, _ := st.ConflictsWith(a.ID); len(cs) != 2 {
		t.Fatalf("store conflicts %d", len(cs))
	}
	// Cancelled: no longer clashes.
	c.apiDo("PUT", "/api/v1/bookings/events/1", sourceToken(t, st, "events", false), strings.Replace(body, `"public"}`, `"public", "status": "cancelled"}`, 1), 200, &a)
	if len(a.Conflicts) != 0 {
		t.Fatalf("cancelled booking still clashes: %+v", a.Conflicts)
	}
}

func TestPushReplaces(t *testing.T) {
	c, st := newTestServer(t)
	hall := addRoom(t, st, "Hall")
	day := soon(8)
	tok := sourceToken(t, st, "events", false)

	// A request booking: the request moves to the new booking.
	req := &store.Request{Name: "Mel", Contact: "mel@example.org", Title: "Supper", StartsOn: day}
	if err := st.CreateRequest(req, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	old, err := st.ApproveRequest(req.ID, 0, "", "private")
	if err != nil {
		t.Fatal(err)
	}
	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/20", tok, pushJSON(day, `, "replaces": `+itoa(old)), 200, &a)
	if a.Replaced != old {
		t.Fatalf("replaced %d, want %d", a.Replaced, old)
	}
	if _, err := st.GetBooking(old); err != store.ErrNotFound {
		t.Fatal("old booking still there")
	}
	if q, _ := st.GetRequest(req.ID); q.BookingID != a.ID || q.Status != "approved" {
		t.Fatalf("request now %+v", q)
	}
	// Sent again (a retry): the booking is gone, the push still works.
	a = putAnswer{}
	c.apiDo("PUT", "/api/v1/bookings/events/20", tok, pushJSON(day, `, "replaces": `+itoa(old)), 200, &a)
	if a.Replaced != 0 {
		t.Fatal("retry replaced something")
	}

	// Another app's booking is never replaced.
	other := book(t, st, store.Booking{Source: "productions", SourceID: "5", StartsOn: day})
	a = putAnswer{}
	c.apiDo("PUT", "/api/v1/bookings/events/21", tok, pushJSON(day, `, "replaces": `+itoa(other)), 200, &a)
	if _, err := st.GetBooking(other); err != nil || a.Replaced != 0 {
		t.Fatal("another app's booking was replaced")
	}

	// A booking made by hand is replaced the same way.
	hand := book(t, st, store.Booking{Title: "Choir", StartsOn: day}, hall)
	c.apiDo("PUT", "/api/v1/bookings/events/22", tok, pushJSON(day, `, "replaces": `+itoa(hand)), 200, &a)
	if a.Replaced != hand {
		t.Fatal("hand-made booking not replaced")
	}
}

func TestAvailabilityRoomsAndSourceList(t *testing.T) {
	c, st := newTestServer(t)
	hall := addRoom(t, st, "Hall")
	addRoom(t, st, "Kitchen")
	day := soon(4)
	book(t, st, store.Booking{Title: "Elders", Visibility: "private", StartsOn: day, StartTime: "18:00"}, hall)
	book(t, st, store.Booking{Title: "Gone", Visibility: "public", StartsOn: day, Status: "cancelled"}, hall)
	tok := sourceToken(t, st, "events", false)
	c.apiDo("PUT", "/api/v1/bookings/events/7", tok, `{"title": "Ours", "starts_on": "`+day+`", "rooms": [{"name": "Shed"}], "visibility": "private"}`, 200, nil)

	var av struct{ Busy []apiBlockJSON }
	c.apiDo("GET", "/api/v1/availability?rooms=hall,shed&from="+day+"&to="+day, tok, "", 200, &av)
	if len(av.Busy) != 2 {
		t.Fatalf("busy %+v", av.Busy)
	}
	for _, b := range av.Busy {
		if b.Rooms[0] == "Hall" && (!b.Busy || b.Title != "") {
			t.Errorf("another's private booking shows: %+v", b)
		}
		if b.Rooms[0] == "Shed" && (b.Busy || b.Title != "Ours" || b.Source != "events" || b.SourceID != "7") {
			t.Errorf("an app sees its own in full: %+v", b)
		}
	}
	c.apiDo("GET", "/api/v1/availability?from="+day, tok, "", 400, nil)
	// Read tokens may ask too.
	read, _ := st.CreateReadToken("Website", false, 0)
	c.apiDo("GET", "/api/v1/availability?rooms=Hall&from="+day+"&to="+day, read, "", 200, nil)

	var rooms struct{ Rooms []apiRoomJSON }
	c.apiDo("GET", "/api/v1/rooms", tok, "", 200, &rooms)
	if len(rooms.Rooms) != 2 || rooms.Rooms[0].Name != "Hall" || !rooms.Rooms[0].Bookable {
		t.Fatalf("rooms %+v", rooms.Rooms)
	}

	var list struct{ Bookings []apiSourceBookingJSON }
	c.apiDo("GET", "/api/v1/bookings/events", tok, "", 200, &list)
	if len(list.Bookings) != 1 || list.Bookings[0].SourceID != "7" || list.Bookings[0].UpdatedAt == "" {
		t.Fatalf("list %+v", list.Bookings)
	}
	c.apiDo("GET", "/api/v1/bookings/events?updated_since="+url.QueryEscape(list.Bookings[0].UpdatedAt), tok, "", 200, &list)
	if len(list.Bookings) != 1 {
		t.Fatal("updated_since is inclusive")
	}
	c.apiDo("GET", "/api/v1/bookings/events?updated_since=2999-01-01", tok, "", 200, &list)
	if len(list.Bookings) != 0 {
		t.Fatal("nothing changed after 2999")
	}
	c.apiDo("GET", "/api/v1/bookings/events?updated_since=yesterday", tok, "", 400, nil)
}

func TestAppTokensAndConnectedApps(t *testing.T) {
	c, st := adminClient(t, Config{})
	body := c.post("/admin/tokens", "/admin/tokens", url.Values{"kind": {"source"}, "source": {"events"}, "name": {"Event planner"}, "see_private": {"1"}}, 200)
	if !strings.Contains(body, "Connections page") {
		t.Error("expected app token help")
	}
	tok := regexpToken.FindString(body)
	if tok == "" {
		t.Fatal("no token shown")
	}
	c.post("/admin/tokens", "/admin/tokens", url.Values{"kind": {"source"}, "source": {"calendar"}, "name": {"Sneaky"}}, 422)
	apps, _ := st.ListTokens(store.TokenSource)
	if len(apps) != 1 || apps[0].Source != "events" || !apps[0].SeePrivate {
		t.Fatalf("app tokens %+v", apps)
	}
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, pushJSON(soon(2), ""), 200, nil)
	page := c.get("/admin/apps", 200)
	if !strings.Contains(page, "Event planner") || !strings.Contains(page, "Production planner") || !strings.Contains(page, "1 coming up") {
		t.Errorf("connected apps page:\n%s", page)
	}

	st.CreateUser("sam", "", "a long password", store.RoleScheduler)
	c.as("sam").get("/admin/apps", 403)
}

func TestMakeThisAnEvent(t *testing.T) {
	env := map[string]string{"EVENTS_URL": "https://events.example.org"}
	c, st := adminClient(t, Config{Env: func(k string) string { return env[k] }})
	hall := addRoom(t, st, "Hall")
	id := book(t, st, store.Booking{Title: "Choir & friends", StartsOn: soon(3), StartTime: "18:00", EndTime: "19:00"}, hall)
	body := c.get(bookingURL(id), 200)
	want := "https://events.example.org/events/new?end_time=19%3a00&amp;from_calendar=" + itoa(id)
	if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) || !strings.Contains(body, "rooms=Hall") {
		t.Errorf("no Make this an event link in:\n%s", body)
	}
	// Not for another app's booking, nor without the event planner set.
	other := book(t, st, store.Booking{Title: "Theirs", Source: "events", SourceID: "4", StartsOn: soon(3), Visibility: "public"})
	if strings.Contains(c.get(bookingURL(other), 200), "Make this an event") {
		t.Error("offered for another app's booking")
	}
	env["EVENTS_URL"] = ""
	if strings.Contains(c.get(bookingURL(id), 200), "Make this an event") {
		t.Error("offered without the event planner")
	}
}
