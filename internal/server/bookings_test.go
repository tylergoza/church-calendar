package server

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

// schedulerClient signs in a scheduler on a fresh server.
func schedulerClient(t *testing.T) (*client, *client, *store.Store) {
	t.Helper()
	c, st := newTestServer(t)
	st.CreateUser("sam", "Sam", "a long password", store.RoleScheduler)
	return c, c.as("sam"), st
}

// soon is a date a few days from now, so it shows on the front page.
func soon(days int) string { return time.Now().AddDate(0, 0, days).Format(store.DateLayout) }

func bookingForm(title, date string, rooms ...int64) url.Values {
	v := url.Values{"title": {title}, "status": {"confirmed"}, "starts_on": {date},
		"start_time": {"18:00"}, "end_time": {"20:00"}, "visibility": {"private"}, "repeat_freq": {"none"}}
	for _, id := range rooms {
		v.Add("room", itoa(id))
	}
	return v
}

func TestRoomPages(t *testing.T) {
	c, sam, st := schedulerClient(t)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	boss := c.as("boss")

	boss.post("/rooms/new", "/rooms", url.Values{"name": {"Lifecenter"}, "capacity": {"200"}, "bookable": {"1"}}, 200)
	if body := boss.post("/rooms/new", "/rooms", url.Values{"name": {"lifecenter"}}, 422); !strings.Contains(body, "already called") {
		t.Error("expected a duplicate-name error")
	}
	if body := boss.post("/rooms/new", "/rooms", url.Values{"name": {"Kitchen"}, "capacity": {"lots"}}, 422); !strings.Contains(body, "number of people") {
		t.Error("expected a capacity error")
	}
	rooms, _ := st.ListRooms(true)
	if len(rooms) != 1 || rooms[0].Capacity != 200 || !rooms[0].Bookable {
		t.Fatalf("rooms = %+v", rooms)
	}
	id := itoa(rooms[0].ID)
	boss.post("/rooms/"+id+"/edit", "/rooms/"+id, url.Values{"name": {"Lifecenter"}, "archived": {"1"}}, 200)
	if body := boss.get("/rooms", 200); strings.Contains(body, "Lifecenter") {
		t.Error("archived room should be hidden from the list")
	}
	if body := boss.get("/rooms?archived=1", 200); !strings.Contains(body, "Lifecenter") {
		t.Error("archived room should show when asked")
	}

	// Schedulers book rooms but don't add, edit or import them.
	sam.get("/rooms/"+id, 200)
	sam.get("/rooms/new", 403)
	sam.get("/rooms/"+id+"/edit", 403)
	sam.get("/rooms/import", 403)
	if body := sam.get("/rooms", 200); strings.Contains(body, `href="/rooms/new"`) || strings.Contains(body, "/edit") {
		t.Error("schedulers shouldn't see add or edit room links")
	}

	mel := c.as("mel")
	mel.get("/rooms", 200)
	mel.get("/rooms/"+id, 200)
	mel.get("/rooms/new", 403)
	mel.get("/conflicts", 403)
	mel.get("/bookings/new", 403)
	if strings.Contains(mel.get("/", 200), `href="/conflicts"`) {
		t.Error("members shouldn't see the Conflicts link")
	}
	// Visitors go to sign in.
	if body := c.get("/rooms", 200); !strings.Contains(body, `action="/login"`) {
		t.Error("rooms should need signing in")
	}
}

func TestBookingVisibilityAndConflicts(t *testing.T) {
	c, sam, st := schedulerClient(t)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	hall, _ := st.CreateRoom(&store.Room{Name: "Lifecenter"})
	day := soon(3)

	sam.post("/bookings/new", "/bookings", bookingForm("Elders meeting", day, hall), 200)
	pub := bookingForm("Choir concert", day, hall)
	pub.Set("visibility", "public")
	pub.Set("start_time", "19:00")
	pub.Set("end_time", "21:00")
	if body := sam.post("/bookings/new", "/bookings", pub, 200); !strings.Contains(body, "clashes with another booking") || !strings.Contains(body, "That's fine") {
		t.Errorf("expected the clash note and the clash on the page:\n%s", body)
	}

	// Visitors: the public one in full, the private one as busy.
	home := c.get("/", 200)
	if !strings.Contains(home, "Choir concert") || strings.Contains(home, "Elders meeting") || !strings.Contains(home, "Lifecenter unavailable") {
		t.Errorf("visitor front page:\n%s", home)
	}
	// A booking they'd only see as busy isn't there for them at all.
	if body := c.get("/calendar/1", 404); strings.Contains(body, "Elders meeting") || strings.Contains(body, "Lifecenter") {
		t.Error("visitors shouldn't find a private booking's page")
	}
	// Members see private bookings in full, but no clashes or edit link.
	mel := c.as("mel")
	if body := mel.get("/calendar/1", 200); !strings.Contains(body, "Elders meeting") || strings.Contains(body, "Clashes") || strings.Contains(body, "/bookings/1/edit") {
		t.Error("member view of a private booking")
	}

	// Conflicts lists the pair; That's fine hides it.
	if body := sam.get("/conflicts", 200); !strings.Contains(body, "Elders meeting") || !strings.Contains(body, "Choir concert") {
		t.Error("expected the clash on Conflicts")
	}
	sam.post("/conflicts", "/conflicts/ok", url.Values{"a": {"1"}, "b": {"2"}, "ok": {"1"}, "next": {"/conflicts"}}, 200)
	if body := sam.get("/conflicts", 200); !strings.Contains(body, "No conflicts") {
		t.Error("marked clash should be hidden")
	}
	if body := sam.get("/conflicts?all=1", 200); !strings.Contains(body, "Marked fine") {
		t.Error("marked clash should show with all=1")
	}
}

func TestBookingValidation(t *testing.T) {
	_, sam, st := schedulerClient(t)
	cases := map[string]url.Values{
		"Give the booking a title": {"title": {""}},
		"Pick the day it starts":   {"starts_on": {"soon"}},
		"before the day it starts": {"ends_on": {"2020-01-01"}},
		"Add a start time":         {"start_time": {""}},
		"end after it starts":      {"end_time": {"17:00"}},
		"Times look like":          {"start_time": {"6pm"}},
	}
	for want, change := range cases {
		form := bookingForm("Meeting", soon(1))
		for k, v := range change {
			form[k] = v
		}
		if body := sam.post("/bookings/new", "/bookings", form, 422); !strings.Contains(body, want) {
			t.Errorf("expected %q", want)
		}
	}
	// Overnight across two days is fine.
	form := bookingForm("Lock-in", soon(1))
	form.Set("ends_on", soon(2))
	form.Set("end_time", "07:00")
	sam.post("/bookings/new", "/bookings", form, 200)
	if b, _ := st.GetBooking(1); b == nil || b.EndsOn != soon(2) {
		t.Error("overnight booking not saved")
	}
}

func TestBookingRepeats(t *testing.T) {
	_, sam, st := schedulerClient(t)
	hall, _ := st.CreateRoom(&store.Room{Name: "Lifecenter"})
	kitchen, _ := st.CreateRoom(&store.Room{Name: "Kitchen"})

	form := bookingForm("Choir", soon(1), hall)
	form.Set("repeat_freq", "weekly")
	form.Set("repeat_every", "1")
	form.Set("repeat_end", "after")
	form.Set("repeat_count", "3")
	if body := sam.post("/bookings/new", "/bookings", form, 200); !strings.Contains(body, "Repeats: Every week") {
		t.Errorf("expected the repeat summary:\n%s", body)
	}
	b, _ := st.GetBooking(1)
	occ, _ := st.Occurrences(b.SeriesID)
	if len(occ) != 3 {
		t.Fatalf("%d dates, want 3", len(occ))
	}
	// The form for a date offers this / this and later, not the repeat fields.
	second := itoa(occ[1].ID)
	if body := sam.get("/bookings/"+second+"/edit", 200); !strings.Contains(body, "This and later dates") || strings.Contains(body, `name="repeat_freq"`) {
		t.Error("expected the apply choice on a repeat's date")
	}

	// This and later from the 2nd: new title and room on the 2nd and 3rd.
	edit := bookingForm("Choir practice", occ[1].StartsOn, kitchen)
	edit.Set("apply", "later")
	if body := sam.post("/bookings/"+second+"/edit", "/bookings/"+second, edit, 200); !strings.Contains(body, "Changes made to 2 dates") {
		t.Errorf("expected 2 dates changed:\n%s", body)
	}
	occ, _ = st.Occurrences(b.SeriesID)
	if occ[0].Title != "Choir" || occ[0].RoomNames() != "Lifecenter" || occ[2].Title != "Choir practice" || occ[2].RoomNames() != "Kitchen" {
		t.Errorf("after this and later: %+v", occ)
	}
	// Moving the day for later dates isn't allowed.
	edit.Set("starts_on", soon(2))
	if body := sam.post("/bookings/"+second+"/edit", "/bookings/"+second, edit, 422); !strings.Contains(body, "Only this date can move") {
		t.Error("expected moving later dates to be refused")
	}

	// Remove one date, then end from the 2nd.
	sam.post("/bookings/"+itoa(occ[2].ID)+"/edit", "/bookings/"+itoa(occ[2].ID)+"/delete", url.Values{}, 200)
	if occ, _ = st.Occurrences(b.SeriesID); len(occ) != 2 {
		t.Fatalf("%d dates after removing one", len(occ))
	}
	sam.post("/bookings/"+second+"/edit", "/bookings/"+second+"/end-series", url.Values{}, 200)
	if occ, _ = st.Occurrences(b.SeriesID); len(occ) != 1 {
		t.Errorf("%d dates after ending", len(occ))
	}
}

// Bookings an app pushed can't be changed here.
func TestAppBookingsReadOnly(t *testing.T) {
	c, sam, st := schedulerClient(t)
	st.DB.Exec(`INSERT INTO bookings (source, source_id, title, starts_on, visibility, url) VALUES ('events', '12', 'Fall dinner', ?, 'public', 'https://events.example.org/events/12')`, soon(2))
	if body := sam.get("/calendar/1", 200); !strings.Contains(body, "Event planner") || strings.Contains(body, "/bookings/1/edit") {
		t.Error("expected the source and no edit link")
	}
	sam.get("/bookings/1/edit", 403)
	sam.post("/", "/bookings/1/delete", url.Values{}, 403)
	if b, _ := st.GetBooking(1); b == nil {
		t.Error("app booking was deleted")
	}
	// Visitors don't get the link into the other app.
	if strings.Contains(c.get("/calendar/1", 200), "events.example.org") {
		t.Error("visitors shouldn't see the app link")
	}
}
