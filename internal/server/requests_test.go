package server

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

func requestForm(title, date string, rooms ...int64) url.Values {
	v := url.Values{"name": {"Jo Visitor"}, "phone": {"(555) 123-4567"}, "contact": {"jo@example.org"}, "title": {title},
		"starts_on": {date}, "start_time": {"18:00"}, "end_time": {"20:00"}}
	for _, id := range rooms {
		v.Add("room", itoa(id))
	}
	return v
}

var requestLinkRe = regexp.MustCompile(`/request/(\d+)\?code=([A-Za-z0-9_-]+)`)

// sent pulls the status link out of the page a request lands on.
func sent(t *testing.T, body string) (id, code string) {
	t.Helper()
	m := requestLinkRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no status link on the page:\n%s", body)
	}
	return m[1], m[2]
}

func addRoom(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	id, err := st.CreateRoom(&store.Room{Name: name, Bookable: true})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// publicRequestsOff turns visitor requests off through the settings page
// (they're on by default).
func publicRequestsOff(t *testing.T, c *client, st *store.Store) {
	t.Helper()
	st.CreateUser("ada", "Ada", "a long password", store.RoleAdmin)
	c.as("ada").post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Grace"}}, 200)
}

func TestVisitorRequests(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	st.CreateRoom(&store.Room{Name: "Boiler room", Bookable: false})

	// On by default.
	body := c.get("/request", 200)
	if !strings.Contains(body, "Hall") || strings.Contains(body, "Boiler room") || !strings.Contains(body, `name="website"`) {
		t.Fatalf("request form: %s", body)
	}
	if body := c.get("/calendar", 200); !strings.Contains(body, `href="/request"`) {
		t.Error("calendar should offer requests")
	}

	if body := c.post("/request", "/request", url.Values{"name": {"Jo"}}, 422); !strings.Contains(body, "Pick at least one room") {
		t.Error("expected validation errors")
	}
	if body := c.post("/request", "/request", requestForm("Old", "2020-01-01", hall), 422); !strings.Contains(body, "from today on") {
		t.Error("expected a past-date error")
	}

	day := soon(5)
	id, code := sent(t, c.post("/request", "/request", requestForm("Movie night", day, hall), 200))
	if body := c.get("/request/"+id+"?code="+code, 200); !strings.Contains(body, "hasn't answered") {
		t.Error("expected the waiting status")
	}
	// Its pending booking blocks the hall straight away.
	if busy, _ := st.BusyInRooms([]int64{hall}, day, "19:00", "", "19:30"); len(busy[hall]) != 1 || busy[hall][0].Status != "pending" {
		t.Errorf("pending booking doesn't block the hall: %+v", busy)
	}
	c.get("/request/"+id, 404)
	c.get("/request/"+id+"?code=wrong", 404)
	sam.get("/request/"+id, 200) // schedulers may look

	// The honeypot looks like success but saves nothing.
	hp := requestForm("Spam", day, hall)
	hp.Set("website", "http://spam.example")
	c.post("/request", "/request", hp, 200)
	if list, _ := st.ListRequests("waiting"); len(list) != 1 {
		t.Fatalf("%d waiting requests, want 1", len(list))
	}

	// Scheduler sees it and turns it down with a note.
	if body := sam.get("/requests", 200); !strings.Contains(body, "Movie night") {
		t.Error("request missing from /requests")
	}
	sam.post("/requests/"+id, "/requests/"+id+"/decline", url.Values{"answer": {"The hall is being painted."}}, 200)
	if body := c.get("/request/"+id+"?code="+code, 200); !strings.Contains(body, "turned down") || !strings.Contains(body, "being painted") {
		t.Error("expected the note back on the status page")
	}
	sam.post("/requests", "/requests/"+id+"/approve", url.Values{}, 409)
	if busy, _ := st.BusyInRooms([]int64{hall}, day, "19:00", "", "19:30"); len(busy) != 0 {
		t.Errorf("declined request still blocks the hall: %+v", busy)
	}

	// Turned off by an admin (saved off stays off): visitors are sent to sign in.
	publicRequestsOff(t, c, st)
	if body := c.get("/request", 200); !strings.Contains(body, `action="/login"`) {
		t.Fatal("expected the sign-in page while public requests are off")
	}
	if body := c.get("/calendar", 200); strings.Contains(body, `href="/request"`) {
		t.Error("calendar offers requests while they're off")
	}
}

func TestVisitorPhone(t *testing.T) {
	c, sam, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	hall := addRoom(t, st, "Hall")
	day := soon(5)

	f := requestForm("No phone", day, hall)
	f.Del("phone")
	if body := c.post("/request", "/request", f, 422); !strings.Contains(body, "Give a phone number") {
		t.Error("visitors must give a phone number")
	}
	for _, bad := range []string{"123", "555-CALL-NOW", "+1 (555) 123-4567 ext 9", "1234567890123456"} {
		f.Set("phone", bad)
		if body := c.post("/request", "/request", f, 422); !strings.Contains(body, "phone number doesn") {
			t.Errorf("phone %q accepted", bad)
		}
	}
	f.Set("phone", "+44 20.7946.0958")
	f.Set("contact", "not an email")
	if body := c.post("/request", "/request", f, 422); !strings.Contains(body, "email address doesn") {
		t.Error("expected a loose email check")
	}
	f.Del("contact")
	id, _ := sent(t, c.post("/request", "/request", f, 200)) // email optional

	// Members need neither.
	m := requestForm("Member", soon(6), hall)
	m.Del("phone")
	m.Del("contact")
	c.as("mel").post("/request", "/request", m, 200)

	// Schedulers get tel: and mailto: links; the phone never reaches the API.
	if body := sam.get("/requests/"+id, 200); !strings.Contains(body, `href="tel:&#43;442079460958"`) {
		t.Errorf("expected a tel: link:\n%s", body)
	}
	read, _ := st.CreateReadToken("Website", true, 0)
	for _, path := range []string{"/api/v1/events?from=" + day + "&to=" + day, "/api/v1/events.ics"} {
		if body, _ := c.apiGet(path, read, 200); strings.Contains(body, "7946") {
			t.Errorf("%s gives away the phone number: %s", path, body)
		}
	}
	if body := c.get("/calendar.ics", 200); strings.Contains(body, "7946") {
		t.Error("the feed gives away the phone number")
	}
}

func TestRequestRateLimit(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	for i := 0; i < 5; i++ {
		c.post("/request", "/request", requestForm("Ask", soon(3), hall), 200)
	}
	if body := c.post("/request", "/request", requestForm("Ask", soon(3), hall), 429); !strings.Contains(body, "Too many requests") {
		t.Error("expected the rate limit")
	}
}

func TestApproveRequest(t *testing.T) {
	c, sam, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	hall := addRoom(t, st, "Hall")
	day := soon(4)
	book(t, st, store.Booking{Title: "Secret choir", StartsOn: day, StartTime: "21:00", EndTime: "22:30", Visibility: "private"}, hall)

	if body := c.as("mel").get("/request", 200); !strings.Contains(body, `value="Mel"`) || strings.Contains(body, `name="website"`) {
		t.Error("members get their name filled in and no honeypot")
	}
	id, code := sent(t, c.post("/request", "/request", requestForm("Bible study", day, hall), 200))
	if body := sam.get("/requests", 200); !strings.Contains(body, "Bible study") {
		t.Error("the Waiting tab is the approval queue")
	}

	sam.post("/requests/"+id, "/requests/"+id+"/approve", url.Values{"visibility": {"public"}, "answer": {"Key at the office"}}, 200)
	sam.post("/requests/"+id, "/requests/"+id+"/approve", url.Values{}, 409)
	list, _ := st.BookingsBetween(day, day, hall)
	n := 0
	for _, b := range list {
		if b.Source == store.SourceRequest {
			n++
			if b.Title != "Bible study" || b.Visibility != "public" || b.Status != "confirmed" {
				t.Errorf("booking = %+v", b)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d request bookings, want exactly 1", n)
	}
	if body := c.get("/request/"+id+"?code="+code, 200); !strings.Contains(body, "Approved") || !strings.Contains(body, "Key at the office") {
		t.Error("expected approved status")
	}
	// Answered requests can't be withdrawn.
	if body := c.post("/login", "/request/"+id+"/withdraw", url.Values{"code": {code}}, 200); !strings.Contains(body, "so it can&#39;t be withdrawn") {
		t.Error("expected a refusal")
	}
	if r, _ := st.GetRequest(mustID(t, id)); r.Status != "approved" {
		t.Errorf("status = %s", r.Status)
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMemberBooksStraightAway(t *testing.T) {
	c, sam, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	hall, library := addRoom(t, st, "Hall"), addRoom(t, st, "Library")
	day := soon(6)
	book(t, st, store.Booking{Title: "Secret choir", StartsOn: day, StartTime: "17:00", EndTime: "18:30", Visibility: "private"}, hall)
	mel := c.as("mel")

	// Free: booked, confirmed, and not in the Waiting tab.
	mel.post("/request", "/request", requestForm("Book club", day, library), 200)
	r, _ := st.ListRequests("approved")
	if len(r) != 1 || r[0].BookingID == 0 || r[0].DecidedBy != 0 {
		t.Fatalf("approved = %+v", r)
	}
	if b, _ := st.GetBooking(r[0].BookingID); b.Status != "confirmed" || b.CreatedBy == 0 {
		t.Errorf("member's booking = %+v", b)
	}
	if body := mel.get("/request", 200); !strings.Contains(body, "Book club") {
		t.Error("members see their own requests")
	}

	// Busy: refused with what's in the way (times, never titles), nothing saved.
	body := mel.post("/request", "/request", requestForm("Prayer group", day, hall, library), 409)
	if !strings.Contains(body, "Hall is taken then: 5:00 PM – 6:30 PM") || !strings.Contains(body, "Library is taken then") || strings.Contains(body, "Secret choir") {
		t.Errorf("expected what's in the way:\n%s", body)
	}
	if all, _ := st.RequestsBy(r[0].UserID); len(all) != 1 {
		t.Errorf("a refused booking was saved: %+v", all)
	}

	// A visitor's pending request blocks members too.
	c.post("/request", "/request", requestForm("Visitor", soon(9), hall), 200)
	if body := mel.post("/request", "/request", requestForm("Choir", soon(9), hall), 409); !strings.Contains(body, "(pending)") {
		t.Error("expected the pending booking in the way")
	}
	if w, _ := st.ListRequests("waiting"); len(w) != 1 {
		t.Fatalf("waiting = %d, want 1 (the visitor's)", len(w))
	}

	// A waiting request shows on /conflicts when a scheduler books over it.
	book(t, st, store.Booking{Title: "Funeral", StartsOn: soon(9), StartTime: "19:00", EndTime: "20:00"}, hall)
	if body := sam.get("/conflicts", 200); !strings.Contains(body, "Funeral") || !strings.Contains(body, "Visitor") {
		t.Error("a pending booking should show in conflicts")
	}
}

func TestRequestWithdraw(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall")
	id, code := sent(t, c.post("/request", "/request", requestForm("Ask", soon(3), hall), 200))
	c.post("/request/"+id+"?code="+code, "/request/"+id+"/withdraw", url.Values{"code": {"wrong"}}, 404)
	if body := c.post("/request/"+id+"?code="+code, "/request/"+id+"/withdraw", url.Values{"code": {code}}, 200); !strings.Contains(body, "You withdrew") {
		t.Error("expected withdrawn status")
	}
}

func TestRequestAvailability(t *testing.T) {
	c, _, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	hall, kitchen := addRoom(t, st, "Hall"), addRoom(t, st, "Kitchen")
	day := soon(4)
	book(t, st, store.Booking{Title: "Secret choir", StartsOn: day, StartTime: "17:00", EndTime: "18:30", Visibility: "public"}, hall)

	path := "/request/availability?starts_on=" + day + "&start_time=18:00&end_time=20:00"
	c.apiGet(path, "", 200) // visitors may check while public requests are on
	publicRequestsOff(t, c, st)
	if body, _ := c.apiGet(path, "", 403); !strings.Contains(body, "sign in") {
		t.Error("visitors can't check while public requests are off")
	}
	body := c.as("mel").get(path, 200)
	if strings.Contains(body, "Secret choir") {
		t.Error("availability must not carry titles")
	}
	var out availability
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	h, k := out.Rooms[itoa(hall)], out.Rooms[itoa(kitchen)]
	if !out.Ready || h.Free || len(h.Busy) != 1 || h.Busy[0] != "5:00 PM – 6:30 PM" || !k.Free {
		t.Errorf("availability = %+v", out)
	}
	if body := c.as("mel").get("/request/availability?starts_on=nope", 200); !strings.Contains(body, `"ready":false`) {
		t.Errorf("bad dates: %s", body)
	}
}

// A bookable building with no bookable rooms inside is one loose choice,
// not an empty group.
func TestGroupRoomsBuildingWithoutBookableRooms(t *testing.T) {
	all := []store.Room{
		{ID: 1, Name: "Annex", Bookable: true},
		{ID: 2, Name: "Annex kitchen", ParentID: 1},
		{ID: 3, Name: "Main building", Bookable: true},
		{ID: 4, Name: "Nursery", ParentID: 3, Bookable: true},
	}
	bookable := []store.Room{all[0], all[2], all[3]}
	loose, groups := groupRooms(all, bookable)
	if len(loose) != 1 || loose[0].Name != "Annex" {
		t.Errorf("loose = %+v, want only the annex", loose)
	}
	if len(groups) != 1 || groups[0].Name != "Main building" || groups[0].Whole == nil || len(groups[0].Rooms) != 1 {
		t.Errorf("groups = %+v", groups)
	}
}
