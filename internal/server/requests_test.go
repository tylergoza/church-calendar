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
	v := url.Values{"name": {"Jo Visitor"}, "contact": {"jo@example.org"}, "title": {title},
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

func addRoom(t *testing.T, st *store.Store, name string, needsApproval bool) int64 {
	t.Helper()
	id, err := st.CreateRoom(&store.Room{Name: name, Bookable: true, NeedsApproval: needsApproval})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// publicRequests turns visitor requests on through the settings page.
func publicRequests(t *testing.T, c *client, st *store.Store) {
	t.Helper()
	st.CreateUser("ada", "Ada", "a long password", store.RoleAdmin)
	c.as("ada").post("/admin/settings", "/admin/settings", url.Values{"site_name": {"Grace"}, "public_requests": {"1"}}, 200)
}

func TestVisitorRequests(t *testing.T) {
	c, sam, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall", true)
	st.CreateRoom(&store.Room{Name: "Boiler room", Bookable: false})

	// Off by default: visitors are sent to sign in.
	if body := c.get("/request", 200); !strings.Contains(body, `action="/login"`) {
		t.Fatal("expected the sign-in page while public requests are off")
	}
	if body := c.get("/calendar", 200); strings.Contains(body, `href="/request"`) {
		t.Error("calendar offers requests while they're off")
	}

	publicRequests(t, c, st)
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
}

func TestRequestRateLimit(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall", true)
	publicRequests(t, c, st)
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
	hall := addRoom(t, st, "Hall", true)
	day := soon(4)
	book(t, st, store.Booking{Title: "Secret choir", StartsOn: day, StartTime: "17:00", EndTime: "18:30", Visibility: "private"}, hall)

	mel := c.as("mel")
	if body := mel.get("/request", 200); !strings.Contains(body, `value="Mel"`) || strings.Contains(body, `name="website"`) {
		t.Error("members get their name filled in and no honeypot")
	}
	id, code := sent(t, mel.post("/request", "/request", requestForm("Bible study", day, hall), 200))
	if body := mel.get("/request", 200); !strings.Contains(body, "Bible study") {
		t.Error("members see their own requests")
	}
	if body := sam.get("/requests", 200); !strings.Contains(body, "Rooms busy") {
		t.Error("expected the clash marker")
	}
	if body := sam.get("/requests/"+id, 200); !strings.Contains(body, "Secret choir") {
		t.Error("schedulers see what clashes")
	}

	sam.post("/requests/"+id, "/requests/"+id+"/approve", url.Values{"visibility": {"public"}, "answer": {"Key at the office"}}, 200)
	sam.post("/requests/"+id, "/requests/"+id+"/approve", url.Values{}, 409)
	list, _ := st.BookingsBetween(day, day, hall)
	n := 0
	for _, b := range list {
		if b.Source == store.SourceRequest {
			n++
			if b.Title != "Bible study" || b.Visibility != "public" {
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
	c, _, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	open, strict := addRoom(t, st, "Library", false), addRoom(t, st, "Hall", true)
	day := soon(6)
	mel := c.as("mel")

	_, _ = sent(t, mel.post("/request", "/request", requestForm("Book club", day, open), 200))
	if r, _ := st.ListRequests("approved"); len(r) != 1 || r[0].BookingID == 0 || r[0].DecidedBy != 0 {
		t.Fatalf("approved = %+v", r)
	}
	// Taken now (by the book club): this one waits.
	mel.post("/request", "/request", requestForm("Prayer group", day, open), 200)
	// A room that needs approval: waits.
	mel.post("/request", "/request", requestForm("Choir", soon(7), strict), 200)
	if r, _ := st.ListRequests("waiting"); len(r) != 2 {
		t.Fatalf("waiting = %+v", r)
	}

	// Visitors always wait, even for rooms that don't need approval.
	publicRequests(t, c, st)
	c.post("/request", "/request", requestForm("Visitor", soon(9), open), 200)
	if r, _ := st.ListRequests("waiting"); len(r) != 3 {
		t.Fatalf("waiting = %d, want 3", len(r))
	}
}

func TestRequestWithdraw(t *testing.T) {
	c, _, st := schedulerClient(t)
	hall := addRoom(t, st, "Hall", true)
	publicRequests(t, c, st)
	id, code := sent(t, c.post("/request", "/request", requestForm("Ask", soon(3), hall), 200))
	c.post("/request/"+id+"?code="+code, "/request/"+id+"/withdraw", url.Values{"code": {"wrong"}}, 404)
	if body := c.post("/request/"+id+"?code="+code, "/request/"+id+"/withdraw", url.Values{"code": {code}}, 200); !strings.Contains(body, "You withdrew") {
		t.Error("expected withdrawn status")
	}
}

func TestRequestAvailability(t *testing.T) {
	c, _, st := schedulerClient(t)
	st.CreateUser("mel", "Mel", "a long password", store.RoleMember)
	hall, kitchen := addRoom(t, st, "Hall", true), addRoom(t, st, "Kitchen", true)
	day := soon(4)
	book(t, st, store.Booking{Title: "Secret choir", StartsOn: day, StartTime: "17:00", EndTime: "18:30", Visibility: "public"}, hall)

	path := "/request/availability?starts_on=" + day + "&start_time=18:00&end_time=20:00"
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
