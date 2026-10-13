package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// fakeTrackerApp answers the tracker's places API for token mt_good1234,
// with whatever places holds at the time.
func fakeTrackerApp(t *testing.T, places *string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mt_good1234" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"bad token"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/places":
			io.WriteString(w, `{"places": [`+*places+`]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

const (
	sitePlace    = `{"id": 1, "parent_id": null, "kind": "site", "name": "Main Campus", "path": "Main Campus"}`
	nurseryPlace = `{"id": 4, "parent_id": 1, "kind": "room", "number": "104", "name": "Nursery", "path": "Main Campus › 104 – Nursery"}`
	hallPlace    = `{"id": 9, "parent_id": 1, "kind": "building", "name": "Lifecenter", "path": "Main Campus › Lifecenter"}`
)

func TestTrackerConnectionTest(t *testing.T) {
	places := sitePlace
	tr := fakeTrackerApp(t, &places)
	c, _ := adminClient(t, Config{})
	page := "/admin/connections"
	if body := c.post(page, "/admin/connections/tracker", url.Values{"tracker_url": {tr.URL}, "tracker_token": {"mt_good1234"}}, 200); !strings.Contains(body, "Connected to the maintenance tracker") {
		t.Errorf("expected the tracker check to pass:\n%s", body)
	}
	if body := c.post(page, "/admin/connections/tracker", url.Values{"tracker_token": {"mt_revoked99"}, "tracker_url": {tr.URL}}, 200); !strings.Contains(body, "turned down the API token") {
		t.Errorf("expected a bad token message:\n%s", body)
	}
}

func TestRoomImport(t *testing.T) {
	places := strings.Join([]string{sitePlace, nurseryPlace, hallPlace}, ",")
	tr := fakeTrackerApp(t, &places)
	env := map[string]string{"TRACKER_URL": tr.URL, "TRACKER_TOKEN": "mt_good1234"}
	c, st := newTestServerWith(t, Config{Env: func(k string) string { return env[k] }})
	st.CreateUser("sam", "", "a long password", store.RoleScheduler)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	sched := c.as("sam")
	c.as("mel").get("/rooms/import", 403)

	// Pushed before the import, by the tracker's bare name.
	if _, err := st.PutSourceBooking(store.Booking{Source: "events", SourceID: "1", Title: "Dinner", StartsOn: "2026-11-07", Status: "confirmed"},
		[]store.PushRoom{{Name: "Nursery"}}, 0); err != nil {
		t.Fatal(err)
	}
	if body := sched.get("/rooms", 200); !strings.Contains(body, `href="/rooms/import"`) {
		t.Error("rooms page should offer the import when the tracker is connected")
	}
	body := sched.get("/rooms/import", 200)
	if !strings.Contains(body, "104 – Nursery") || !strings.Contains(body, "Lifecenter") || strings.Contains(body, `value="1"`) {
		t.Errorf("import page should list rooms and buildings, not sites:\n%s", body)
	}
	body = sched.post("/rooms/import", "/rooms/import", url.Values{"place": {"4", "9"}}, 200)
	if !strings.Contains(body, "Added: 104 – Nursery, Lifecenter.") || !strings.Contains(body, "1 booking room sent by name") {
		t.Errorf("import result:\n%s", body)
	}
	// Again: nothing new, no duplicates.
	body = sched.post("/rooms/import", "/rooms/import", url.Values{"place": {"4", "9"}}, 200)
	if !strings.Contains(body, "Everything was already up to date") {
		t.Errorf("second import:\n%s", body)
	}
	if rooms, _ := st.ListRooms(true); len(rooms) != 2 {
		t.Errorf("rooms = %d, want 2", len(rooms))
	}

	// The hall is deleted in the tracker: the page warns, the import archives.
	places = strings.Join([]string{sitePlace, nurseryPlace}, ",")
	if body := sched.get("/rooms/import", 200); !strings.Contains(body, "importing will archive this room") {
		t.Errorf("expected a warning about the gone place:\n%s", body)
	}
	if body := sched.post("/rooms/import", "/rooms/import", url.Values{}, 200); !strings.Contains(body, "Archived, as they're gone from the tracker: Lifecenter.") {
		t.Errorf("expected the hall archived:\n%s", body)
	}
}

func TestRoomImportOff(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("sam", "", "a long password", store.RoleScheduler)
	sched := c.as("sam")
	if strings.Contains(sched.get("/rooms", 200), "/rooms/import") {
		t.Error("no import button without the tracker")
	}
	if body := sched.get("/rooms/import", 200); !strings.Contains(body, "The tracker isn't connected") {
		t.Errorf("expected the not connected note:\n%s", body)
	}
}
