package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

// apiGet sends a GET with a bearer token ("" for none).
func (c *client) apiGet(path, token string, wantStatus int) (string, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body), resp.Header
}

func book(t *testing.T, st *store.Store, b store.Booking, rooms ...int64) int64 {
	t.Helper()
	if b.Status == "" {
		b.Status = "confirmed"
	}
	id, err := st.CreateBooking(&b, rooms)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// TestVisibilityTable checks each cell of PLAN.md's visibility table: who
// reads (visitor, read token with or without private access, member)
// against each visibility. Full shows the title; busy shows only the room.
func TestVisibilityTable(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	mel := c.as("mel")
	day := soon(2)
	month := day[:7]
	for _, v := range []string{"public", "private", "locked"} {
		rm, _ := st.CreateRoom(&store.Room{Name: "Room " + v})
		book(t, st, store.Booking{Title: "Title " + v, Description: "About " + v, Visibility: v,
			StartsOn: day, StartTime: "10:00", EndTime: "11:00"}, rm)
	}
	pubTok, _ := st.CreateReadToken("Website", false, 0)
	privTok, _ := st.CreateReadToken("Office", true, 0)

	type view struct{ title, busy bool }
	page := func(cl *client) func(v string) view {
		body := cl.get("/calendar?month="+month, 200)
		return func(v string) view {
			return view{strings.Contains(body, "Title "+v), strings.Contains(body, "Room "+v+" unavailable")}
		}
	}
	api := func(token string) func(v string) view {
		body, _ := c.apiGet("/api/v1/events?from="+day+"&to="+day, token, 200)
		var out struct{ Events []apiEventJSON }
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		return func(v string) view {
			for _, e := range out.Events {
				if len(e.Rooms) == 1 && e.Rooms[0] == "Room "+v {
					if e.Busy && (e.ID != 0 || e.Title != "" || e.Description != "" || e.Source != "" || e.URL != "") {
						t.Errorf("busy %s entry carries details: %+v", v, e)
					}
					return view{e.Title == "Title "+v, e.Busy}
				}
			}
			t.Fatalf("no entry for %s in %s", v, body)
			return view{}
		}
	}
	ics := func() func(v string) view {
		body, _ := c.apiGet("/calendar.ics?from="+day+"&to="+day, "", 200)
		return func(v string) view {
			return view{strings.Contains(body, "Title "+v), strings.Contains(body, "Busy: Room "+v)}
		}
	}

	readers := []struct {
		name string
		see  func() func(string) view
		want map[string]bool // visibility -> full
	}{
		{"visitor", func() func(string) view { return page(c) }, map[string]bool{"public": true, "private": false, "locked": false}},
		{"visitor feed", ics, map[string]bool{"public": true, "private": false, "locked": false}},
		{"token", func() func(string) view { return api(pubTok) }, map[string]bool{"public": true, "private": false, "locked": false}},
		{"private token", func() func(string) view { return api(privTok) }, map[string]bool{"public": true, "private": true, "locked": false}},
		{"member", func() func(string) view { return page(mel) }, map[string]bool{"public": true, "private": true, "locked": false}},
	}
	for _, rd := range readers {
		see := rd.see()
		for _, v := range []string{"public", "private", "locked"} {
			t.Run(rd.name+"/"+v, func(t *testing.T) {
				got := see(v)
				if full := rd.want[v]; got.title != full || got.busy == full {
					t.Errorf("got %+v, want full=%v", got, full)
				}
			})
		}
	}
}

func TestBookingPagesByVisibility(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	mel := c.as("mel")
	day := soon(2)
	rm, _ := st.CreateRoom(&store.Room{Name: "Chapel"})
	pub := book(t, st, store.Booking{Title: "Open house", Visibility: "public", StartsOn: day}, rm)
	priv := book(t, st, store.Booking{Title: "Elders", Visibility: "private", StartsOn: day}, rm)
	locked := book(t, st, store.Booking{Title: "Secret", Visibility: "locked", StartsOn: day, Source: "events", SourceID: "7",
		URL: "https://events.example.org/events/7"}, rm)
	gone := book(t, st, store.Booking{Title: "Called off", Visibility: "public", StartsOn: day, Status: "cancelled"}, rm)

	c.get(bookingURL(pub), 200)
	for _, id := range []int64{priv, locked, gone} {
		c.get(bookingURL(id), 404)
	}
	mel.get(bookingURL(priv), 200)
	body := mel.get(bookingURL(locked), 200)
	if strings.Contains(body, "Secret") || strings.Contains(body, "Event planner") || strings.Contains(body, "events.example.org") {
		t.Errorf("a member's view of a locked booking should show only rooms and times:\n%s", body)
	}
	if body := c.get("/calendar?month="+day[:7], 200); strings.Contains(body, "Called off") {
		t.Error("cancelled bookings stay off the calendar")
	}
}

func TestBySourceRedirect(t *testing.T) {
	c, st := newTestServer(t)
	id := book(t, st, store.Booking{Source: "events", SourceID: "12", Title: "Dinner", Visibility: "public", StartsOn: soon(1)})
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.http.Get(c.base + "/calendar/by-source/events/12")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != bookingURL(id) {
		t.Errorf("got %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c.get("/calendar/by-source/events/99", 404)
}

func TestMonthFilters(t *testing.T) {
	c, st := newTestServer(t)
	hall, _ := st.CreateRoom(&store.Room{Name: "Lifecenter"})
	kitchen, _ := st.CreateRoom(&store.Room{Name: "Kitchen"})
	month := time.Now().AddDate(0, 1, 0).Format("2006-01")
	book(t, st, store.Booking{Title: "Retreat", Visibility: "public", StartsOn: month + "-10", EndsOn: month + "-12"}, hall)
	book(t, st, store.Booking{Source: "events", SourceID: "3", Title: "Bake sale", Visibility: "public", StartsOn: month + "-11"}, kitchen)
	book(t, st, store.Booking{Source: "events", SourceID: "4", Visibility: "locked", StartsOn: month + "-11"}, kitchen)

	body := c.get("/calendar?month="+month, 200)
	if n := strings.Count(body, ">Retreat<"); n != 6 { // three days, in the grid and in the list
		t.Errorf("a three-day booking should show on each day in both views, got %d", n)
	}
	if !strings.Contains(body, `href="webcal://`) {
		t.Error("expected the webcal link")
	}
	if !strings.Contains(body, `<option value="events"`) || !strings.Contains(body, `<option value="other"`) {
		t.Error("expected the source filter")
	}
	body = c.get("/calendar?month="+month+"&room=lifecenter", 200)
	if strings.Contains(body, "Bake sale") || !strings.Contains(body, "Retreat") {
		t.Error("room filter")
	}
	body = c.get("/calendar?month="+month+"&source=events", 200)
	if strings.Contains(body, ">Retreat<") || !strings.Contains(body, "Bake sale") || strings.Contains(body, "Kitchen unavailable") {
		t.Error("source filter shows only that source and leaves busy blocks out")
	}
}

func TestReadAPI(t *testing.T) {
	c, st := newTestServer(t)
	hall, _ := st.CreateRoom(&store.Room{Name: "Lifecenter"})
	kitchen, _ := st.CreateRoom(&store.Room{Name: "Kitchen"})
	day := soon(3)
	pub := book(t, st, store.Booking{Source: "events", SourceID: "5", Title: "Dinner", Kind: "Fellowship", Visibility: "public",
		StartsOn: day, StartTime: "17:30", EndTime: "20:00", Description: "Potluck"}, hall)
	priv := book(t, st, store.Booking{Title: "Committee", Visibility: "private", StartsOn: day}, kitchen)
	tok, _ := st.CreateReadToken("Website", false, 0)

	_, h := c.apiGet("/api/v1/events", "", 401)
	if h.Get("Access-Control-Allow-Origin") != "*" {
		t.Error("expected CORS")
	}
	c.apiGet("/api/v1/events", "cal_nope", 401)
	st.DB.Exec(`INSERT INTO tokens (name, kind, source, hash, prefix) VALUES ('Planner', 'source', 'events', ?, 'cal_src')`, hashForTest("cal_source"))
	c.apiGet("/api/v1/events", "cal_source", 403)

	body, _ := c.apiGet("/api/v1/events?room=Lifecenter", tok, 200)
	var out struct{ Events []apiEventJSON }
	json.Unmarshal([]byte(body), &out)
	if len(out.Events) != 1 {
		t.Fatalf("room filter: %s", body)
	}
	e := out.Events[0]
	if e.ID != pub || e.Source != "events" || e.Title != "Dinner" || e.Kind != "Fellowship" || !e.Public ||
		e.Description != "Potluck" || !strings.HasSuffix(e.URL, bookingURL(pub)) || e.StartTime != "17:30" {
		t.Errorf("event = %+v", e)
	}

	c.apiGet("/api/v1/events/"+itoa(pub), tok, 200)
	c.apiGet("/api/v1/events/"+itoa(priv), tok, 404)
	for _, q := range []string{"from=soon", "from=2026-01-02&to=2026-01-01", "from=2026-01-01&to=2027-06-01"} {
		c.apiGet("/api/v1/events?"+q, tok, 400)
	}
	c.apiGet("/api/v1/nothing", tok, 404)

	// The token feed takes ?token=; the public feed may be cached.
	if body, _ := c.apiGet("/api/v1/events.ics?token="+url.QueryEscape(tok), "", 200); !strings.Contains(body, "SUMMARY:Dinner") {
		t.Error("token feed")
	}
	if _, h := c.apiGet("/calendar.ics", "", 200); h.Get("Cache-Control") != "public, max-age=300" || h.Get("Set-Cookie") != "" {
		t.Errorf("public feed headers: %v", h)
	}
}

// hashForTest is how the store keeps tokens, for making a source token
// before the push API has a page for them.
func hashForTest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestTokenPages(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("ada", "", "a long password", store.RoleAdmin)
	ada := c.as("ada")
	body := ada.post("/admin/tokens", "/admin/tokens", url.Values{"name": {"Church website"}}, 200)
	m := regexp.MustCompile(`<code class="token">(cal_[^<]+)</code>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("new token not shown:\n%s", body)
	}
	if strings.Contains(ada.get("/admin/tokens", 200), m[1]) {
		t.Error("the token should only be shown once")
	}
	c.apiGet("/api/v1/events", m[1], 200)

	list, _ := st.ListTokens(store.TokenRead)
	id := itoa(list[0].ID)
	ada.post("/admin/tokens", "/admin/tokens/"+id+"/private", url.Values{"private": {"1"}}, 200)
	if list, _ := st.ListTokens(store.TokenRead); !list[0].SeePrivate || list[0].LastUsed == "" {
		t.Errorf("token = %+v", list[0])
	}
	ada.post("/admin/tokens", "/admin/tokens/"+id+"/delete", url.Values{}, 200)
	c.apiGet("/api/v1/events", m[1], 401)
	ada.post("/admin/tokens", "/admin/tokens", url.Values{"name": {""}}, 422)
}

func TestBuildICS(t *testing.T) {
	list := []store.CalendarEntry{
		{Booking: store.Booking{ID: 4, Title: "Dinner, with; friends", StartsOn: "2026-11-07", EndsOn: "2026-11-08",
			Description: strings.Repeat("Long words ", 12), Rooms: []store.BookingRoom{{Name: "Lifecenter"}}}},
		{Busy: true, Booking: store.Booking{StartsOn: "2026-11-07", StartTime: "10:00", EndTime: "11:00",
			Rooms: []store.BookingRoom{{Name: "Kitchen"}}}},
	}
	out := buildICS("Church", "cal.example.org", "https://cal.example.org", list, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{
		"UID:booking-4@cal.example.org\r\n", `SUMMARY:Dinner\, with\; friends`, "DTSTART;VALUE=DATE:20261107\r\n",
		"DTEND;VALUE=DATE:20261109\r\n", "URL:https://cal.example.org/calendar/4\r\n", "SUMMARY:Busy: Kitchen\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\r\n") {
		if len(line) > 75 {
			t.Errorf("line over 75 octets: %q", line)
		}
	}
}
