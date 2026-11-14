package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
	"github.com/tylergoza/church-calendar/internal/umtoken"
)

// fakeTokenUM answers User Management's GET /api/v1/token for the tokens
// in its map; anything else (another audience, expired) is 401.
type fakeTokenUM struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	ok   map[string]umtoken.Answer
	down bool
}

func newFakeTokenUM(t *testing.T) *fakeTokenUM {
	f := &fakeTokenUM{ok: map[string]umtoken.Answer{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/token" {
			http.NotFound(w, r)
			return
		}
		f.hits.Add(1)
		f.mu.Lock()
		ans, ok := f.ok[r.Header.Get("X-Token")]
		down := f.down
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if id, sec, good := r.BasicAuth(); !good || id != "calendar" || sec != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		switch {
		case down:
			w.WriteHeader(http.StatusBadGateway)
		case !ok:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_token"}`))
		default:
			json.NewEncoder(w).Encode(ans)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTokenUM) give(token, sub, username, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok[token] = umtoken.Answer{User: umtoken.User{Sub: sub, Username: username, Name: strings.ToUpper(username[:1]) + username[1:], Role: role}, Via: "mcp"}
}

// meServer runs the calendar with single sign-on pointing at f (nil for
// none). The *Server is returned so tests can move the token cache's clock.
func meServer(t *testing.T, f *fakeTokenUM) (*Server, *store.Store, *client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	env := map[string]string{}
	if f != nil {
		env = map[string]string{"SSO_URL": f.srv.URL, "SSO_CLIENT_ID": "calendar", "SSO_CLIENT_SECRET": "s3cret"}
	}
	srv, err := New(Config{Env: envOf(env)}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, st, &client{t: t, base: ts.URL, http: &http.Client{}}
}

// meGet is apiGet that also checks the /me headers on every answer.
func (c *client) meGet(path, token string, want int) (string, http.Header) {
	c.t.Helper()
	body, h := c.apiGet(path, token, want)
	if h.Get("Set-Cookie") != "" {
		c.t.Errorf("GET %s set a cookie", path)
	}
	if h.Get("Cache-Control") != "private, no-store" {
		c.t.Errorf("GET %s: Cache-Control %q", path, h.Get("Cache-Control"))
	}
	if h.Get("Access-Control-Allow-Origin") != "" {
		c.t.Errorf("GET %s: CORS header on /me", path)
	}
	return body, h
}

func isJSONError(body string) bool {
	var e struct{ Error string }
	return json.Unmarshal([]byte(body), &e) == nil && e.Error != ""
}

func TestMeOffWithoutUserManagement(t *testing.T) {
	_, st, c := meServer(t, nil)
	tok, _ := st.CreateReadToken("Website", true, 0)
	for _, p := range []string{"/api/v1/me/calendar", "/api/v1/me/rooms", "/api/v1/me/nope"} {
		if body, _ := c.meGet(p, "umd_whatever", 404); isJSONError(body) {
			t.Errorf("%s: off should be a plain 404, got %s", p, body)
		}
	}
	// The read-token API is unchanged.
	c.apiGet("/api/v1/events", tok, 200)
	if body, _ := c.apiGet("/api/v1/nope", tok, 404); !isJSONError(body) {
		t.Error("the /api/ catch-all should still answer JSON")
	}
}

func TestMeTokenChecks(t *testing.T) {
	f := newFakeTokenUM(t)
	srv, st, c := meServer(t, f)
	f.give("umd_mel", "s-mel", "mel", "member")
	readTok, _ := st.CreateReadToken("Website", true, 0)

	// No token, a calendar token, a token for another audience, an expired
	// one: all 401 invalid_token, and nobody is added.
	for _, tok := range []string{"", readTok, "umd_for_events", "umd_expired"} {
		body, h := c.meGet("/api/v1/me/calendar", tok, 401)
		if !strings.Contains(body, "invalid_token") || h.Get("WWW-Authenticate") == "" {
			t.Errorf("token %q: %s", tok, body)
		}
	}
	if n, _ := st.CountUsers(); n != 0 {
		t.Errorf("refused tokens made %d users", n)
	}

	// A good token: the user is made like an SSO sign-in.
	f.hits.Store(0)
	now := time.Now()
	srv.meTokens.Now = func() time.Time { return now }
	c.meGet("/api/v1/me/calendar", "umd_mel", 200)
	c.meGet("/api/v1/me/rooms", "umd_mel", 200)
	if n := f.hits.Load(); n != 1 {
		t.Errorf("second call within 60 s asked User Management again: %d calls", n)
	}
	users, _ := st.ListUsers()
	if len(users) != 1 || users[0].SSOSubject != "s-mel" || users[0].Role != store.RoleMember || users[0].DisplayName != "Mel" {
		t.Fatalf("users = %+v", users)
	}
	var sessions int
	if err := st.DB.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("a /me call made %d sessions (%v)", sessions, err)
	}

	// After 60 s it's checked again, and a token gone bad is refused.
	now = now.Add(61 * time.Second)
	f.mu.Lock()
	delete(f.ok, "umd_mel")
	f.mu.Unlock()
	c.meGet("/api/v1/me/calendar", "umd_mel", 401)
	if n := f.hits.Load(); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}

	// The role follows the token, as at sign-in.
	f.give("umd_mel2", "s-mel", "mel", "scheduler")
	c.meGet("/api/v1/me/calendar", "umd_mel2", 200)
	if u, _ := st.GetUser(users[0].ID); u.Role != store.RoleScheduler {
		t.Errorf("role = %q, want scheduler", u.Role)
	}

	// Another delegate is refused; User Management down is 503, not 401.
	f.mu.Lock()
	f.ok["umd_other"] = umtoken.Answer{User: umtoken.User{Sub: "s-x", Username: "x"}, Via: "someapp"}
	f.mu.Unlock()
	c.meGet("/api/v1/me/calendar", "umd_other", 403)
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	if _, h := c.meGet("/api/v1/me/calendar", "umd_new", 503); h.Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()

	// Unknown /me paths are a plain 404 ("not built"), unlike /api/'s JSON one.
	if body, _ := c.meGet("/api/v1/me/nope", "umd_mel2", 404); isJSONError(body) {
		t.Errorf("unknown /me path: %s", body)
	}
	// The read-token API still works with SSO on.
	c.apiGet("/api/v1/events", readTok, 200)
}

// TestMeVisibility: what each role gets is what the web pages show a
// signed-in person: public and private in full, locked only as busy.
func TestMeVisibility(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f)
	f.give("umd_member", "s-1", "mel", "member")
	f.give("umd_sched", "s-2", "sam", "scheduler")
	f.give("umd_admin", "s-3", "ada", "admin")
	day := soon(3)
	ids := map[string]int64{}
	for _, v := range []string{"public", "private", "locked"} {
		rm := addRoom(t, st, "Room "+v)
		ids[v] = book(t, st, store.Booking{Title: "Title " + v, Description: "About " + v, Visibility: v,
			StartsOn: day, StartTime: "10:00", EndTime: "11:00"}, rm)
	}
	st.DB.Exec(`UPDATE bookings SET signup_label = 'Secret signup', signup_url = 'https://x.example/s', signup_state = 'open' WHERE id = ?`, ids["locked"])

	// The web page as a signed-in member, for comparison (a second server
	// on the same database, with the local login).
	st.CreateUser("pat", "", "a long password", store.RoleMember)
	web := startServer(t, Config{}, st).as("pat").get("/calendar?month="+day[:7], 200)

	for _, tok := range []string{"umd_member", "umd_sched", "umd_admin"} {
		body, _ := c.meGet("/api/v1/me/calendar?from="+day+"&to="+day, tok, 200)
		if strings.Contains(body, "Title locked") || strings.Contains(body, "About locked") || strings.Contains(body, "Secret signup") {
			t.Errorf("%s sees the locked booking's details: %s", tok, body)
		}
		var out struct{ Events []apiEventJSON }
		json.Unmarshal([]byte(body), &out)
		if len(out.Events) != 3 {
			t.Fatalf("%s: events = %+v", tok, out.Events)
		}
		for _, e := range out.Events {
			v := strings.TrimPrefix(e.Rooms[0], "Room ")
			full := v != "locked"
			if e.Busy == full || (e.Title != "") != full || (e.ID != 0) != full || (e.URL != "") != full {
				t.Errorf("%s/%s: %+v", tok, v, e)
			}
			if e.Busy != !strings.Contains(web, "Title "+v) {
				t.Errorf("%s/%s: API busy=%v differs from the web page", tok, v, e.Busy)
			}
		}
		for v, id := range ids {
			path := "/api/v1/me/calendar/" + itoa(id)
			if v == "locked" {
				if body, _ := c.meGet(path, tok, 404); !isJSONError(body) || strings.Contains(body, "Title") {
					t.Errorf("%s: locked detail = %s", tok, body)
				}
				continue
			}
			var e apiEventJSON
			body, _ := c.meGet(path, tok, 200)
			json.Unmarshal([]byte(body), &e)
			if e.Title != "Title "+v || e.Public != (v == "public") {
				t.Errorf("%s/%s detail = %+v", tok, v, e)
			}
		}
		c.meGet("/api/v1/me/calendar/999999", tok, 404)
	}
}

func TestMeRoomsAvailabilityBookings(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f)
	f.give("umd_mel", "s-mel", "mel", "member")
	f.give("umd_kim", "s-kim", "kim", "member")
	hall := addRoom(t, st, "Hall")
	main, _ := st.CreateRoom(&store.Room{Name: "Main", Bookable: true})
	st.CreateRoom(&store.Room{Name: "Nursery", Bookable: true, ParentID: main, Capacity: 12})
	st.CreateRoom(&store.Room{Name: "Closet"}) // not bookable
	day := soon(4)
	book(t, st, store.Booking{Title: "Secret choir", Visibility: "public", StartsOn: day, StartTime: "09:00", EndTime: "10:00"}, main)
	book(t, st, store.Booking{Title: "Visitor", Status: "pending", Visibility: "private", StartsOn: day, StartTime: "12:00", EndTime: "13:00"}, hall)

	// Rooms: what the request form offers, by building.
	body, _ := c.meGet("/api/v1/me/rooms", "umd_mel", 200)
	if strings.Contains(body, "Closet") {
		t.Errorf("not bookable room listed: %s", body)
	}
	var rooms struct {
		Buildings []meBuildingJSON `json:"buildings"`
	}
	json.Unmarshal([]byte(body), &rooms)
	if len(rooms.Buildings) != 2 || rooms.Buildings[0].Name != "Hall" || len(rooms.Buildings[0].Rooms) != 0 ||
		rooms.Buildings[1].Name != "Main" || len(rooms.Buildings[1].Rooms) != 2 || rooms.Buildings[1].Rooms[1].Capacity != 12 {
		t.Errorf("rooms = %s", body)
	}

	// Availability: busy times only, the family counts, pending carries
	// its status, no titles; an unknown room is a 400.
	body, _ = c.meGet("/api/v1/me/availability?from="+day+"&to="+day+"&rooms=nursery,%20Hall", "umd_mel", 200)
	if strings.Contains(body, "Secret") || strings.Contains(body, "Visitor") || strings.Contains(body, "title") {
		t.Errorf("availability shows details: %s", body)
	}
	var av struct{ Busy []meBlockJSON }
	json.Unmarshal([]byte(body), &av)
	if len(av.Busy) != 2 || av.Busy[0].Rooms[0] != "Main" || av.Busy[1].Status != "pending" {
		t.Errorf("busy = %s", body)
	}
	if body, _ := c.meGet("/api/v1/me/availability?from="+day+"&to="+day+"&rooms=Nope", "umd_mel", 400); !isJSONError(body) {
		t.Errorf("unknown room: %s", body)
	}
	body, _ = c.meGet("/api/v1/me/availability?from="+soon(30)+"&to="+soon(30), "umd_mel", 200)
	if !strings.Contains(body, `"busy": []`) {
		t.Errorf("empty list should be []: %s", body)
	}

	// Bookings: only the caller's, never null.
	if body, _ := c.meGet("/api/v1/me/bookings", "umd_mel", 200); !strings.Contains(body, `"bookings": []`) {
		t.Errorf("no bookings yet: %s", body)
	}
	users, _ := st.ListUsers()
	var mel, kim int64
	for _, u := range users {
		if u.Username == "mel" {
			mel = u.ID
		}
	}
	c.meGet("/api/v1/me/rooms", "umd_kim", 200)
	users, _ = st.ListUsers()
	for _, u := range users {
		if u.Username == "kim" {
			kim = u.ID
		}
	}
	for _, r := range []struct {
		who   int64
		title string
		day   string
	}{{mel, "Book club", soon(10)}, {mel, "Old club", soon(-10)}, {kim, "Kim's party", soon(11)}} {
		req := store.Request{Name: "x", UserID: r.who, Title: r.title, StartsOn: r.day, StartTime: "18:00", EndTime: "19:00"}
		if _, err := st.BookRequest(&req, []int64{hall}); err != nil {
			t.Fatal(err)
		}
	}
	body, _ = c.meGet("/api/v1/me/bookings", "umd_mel", 200)
	var bk struct{ Bookings []meBookingJSON }
	json.Unmarshal([]byte(body), &bk)
	if strings.Contains(body, "Kim") || len(bk.Bookings) != 2 || bk.Bookings[0].Title != "Book club" ||
		bk.Bookings[0].Status != "confirmed" || !bk.Bookings[0].CanChange || bk.Bookings[0].Type != "request" ||
		bk.Bookings[1].CanChange || bk.Bookings[0].Rooms[0] != "Hall" {
		t.Errorf("mel's bookings = %s", body)
	}
}

// TestMeFieldNames checks the JSON keys against APP_CONTRACTS.md's
// examples for the calendar (the MCP server's tools read these names).
func TestMeFieldNames(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f)
	f.give("umd_mel", "s-mel", "mel", "member")
	hall := addRoom(t, st, "Hall")
	day := soon(5)
	full := book(t, st, store.Booking{Title: "Choir", Kind: "rehearsal", Description: "Sing", Visibility: "public",
		StartsOn: day, StartTime: "19:00", EndTime: "20:30"}, hall)
	st.DB.Exec(`UPDATE bookings SET signup_label = 'Dinner', signup_url = 'https://x.example/s', signup_state = 'open', signup_closes_at = '2099-01-01T00:00' WHERE id = ?`, full)
	book(t, st, store.Booking{Title: "Hidden", Visibility: "locked", StartsOn: day, StartTime: "09:00", EndTime: "12:00"}, hall)
	users, _ := st.ListUsers()
	if len(users) == 0 {
		c.meGet("/api/v1/me/rooms", "umd_mel", 200)
		users, _ = st.ListUsers()
	}
	req := store.Request{Name: "x", UserID: users[0].ID, Title: "Lock-in", StartsOn: soon(20), StartTime: "18:00", EndTime: "20:00"}
	if _, err := st.BookRequest(&req, []int64{hall}); err != nil {
		t.Fatal(err)
	}

	keysOf := func(m map[string]any) string {
		var ks []string
		for k := range m {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		return strings.Join(ks, ",")
	}
	// hasKeys: every contract key is there (extra keys are allowed).
	hasKeys := func(what string, m map[string]any, want string) {
		t.Helper()
		for _, k := range strings.Split(want, ",") {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: no %q in %s", what, k, keysOf(m))
			}
		}
	}
	get := func(path string) map[string]any {
		body, _ := c.meGet(path, "umd_mel", 200)
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	cal := get("/api/v1/me/calendar?from=" + day + "&to=" + day)
	events := cal["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %v", events)
	}
	busy, ev := events[0].(map[string]any), events[1].(map[string]any)
	hasKeys("busy event", busy, "busy,starts_on,start_time,ends_on,end_time,rooms")
	for _, k := range []string{"id", "title", "description", "kind", "source", "url", "signup"} {
		if _, ok := busy[k]; ok {
			t.Errorf("busy event has %q", k)
		}
	}
	const eventKeys = "id,busy,source,title,kind,status,description,starts_on,start_time,ends_on,end_time,rooms,public,url,signup"
	hasKeys("event", ev, eventKeys)
	hasKeys("signup", ev["signup"].(map[string]any), "label,url,state,closes_at")
	hasKeys("one event", get("/api/v1/me/calendar/"+itoa(full)), eventKeys)

	rooms := get("/api/v1/me/rooms")["buildings"].([]any)
	hasKeys("building", rooms[0].(map[string]any), "name,rooms")

	blocks := get("/api/v1/me/availability?from=" + day + "&to=" + day)["busy"].([]any)
	hasKeys("busy block", blocks[0].(map[string]any), "starts_on,start_time,ends_on,end_time,rooms,status")
	if _, ok := blocks[0].(map[string]any)["title"]; ok {
		t.Error("busy block has a title")
	}

	bookings := get("/api/v1/me/bookings")["bookings"].([]any)
	hasKeys("booking", bookings[0].(map[string]any), "id,title,status,starts_on,start_time,ends_on,end_time,rooms,can_change,taken_over")
}
