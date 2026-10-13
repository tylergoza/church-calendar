package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

const (
	syncSite    = `{"id": 1, "parent_id": null, "kind": "site", "name": "Campus", "path": "Campus"}`
	syncMain    = `{"id": 2, "parent_id": 1, "kind": "building", "name": "Main building", "path": "Campus › Main building", "bookable": true}`
	syncNursery = `{"id": 4, "parent_id": 2, "kind": "room", "number": "104", "name": "Nursery", "path": "Campus › Main building › 104 – Nursery", "bookable": true}`
	syncKitchen = `{"id": 5, "parent_id": 2, "kind": "room", "name": "Kitchen", "path": "Campus › Main building › Kitchen", "bookable": false}`
	syncFloor   = `{"id": 6, "parent_id": 2, "kind": "floor", "name": "Upstairs", "path": "Campus › Main building › Upstairs", "bookable": true}`
)

// syncServer runs a Server (not its HTTP side) against a fake tracker.
func syncServer(t *testing.T, places *string) (*Server, *store.Store) {
	t.Helper()
	tr := fakeTrackerApp(t, places)
	env := map[string]string{"TRACKER_URL": tr.URL, "TRACKER_TOKEN": "mt_good1234"}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{Env: func(k string) string { return env[k] }}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return srv, st
}

func roomNamed(t *testing.T, st *store.Store, name string) *store.Room {
	t.Helper()
	rooms, _ := st.ListRooms(true)
	for i := range rooms {
		if rooms[i].Name == name {
			return &rooms[i]
		}
	}
	return nil
}

func TestTrackerSyncAddsBookableOnly(t *testing.T) {
	places := strings.Join([]string{syncSite, syncMain, syncNursery, syncKitchen, syncFloor}, ",")
	srv, st := syncServer(t, &places)
	res, err := srv.SyncTracker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 2 {
		t.Fatalf("added = %v, want the building and the nursery", res.Added)
	}
	main, nursery := roomNamed(t, st, "Main building"), roomNamed(t, st, "104 – Nursery")
	if main == nil || nursery == nil || nursery.ParentID != main.ID || !nursery.Bookable || nursery.Kind != "room" {
		t.Fatalf("rooms = %+v %+v", main, nursery)
	}
	if roomNamed(t, st, "Kitchen") != nil || roomNamed(t, st, "Upstairs") != nil || roomNamed(t, st, "Campus") != nil {
		t.Error("places that aren't bookable (or aren't a bookable kind) must not be added")
	}
}

func TestTrackerSyncUpdates(t *testing.T) {
	places := strings.Join([]string{syncSite, syncMain, syncNursery, syncKitchen}, ",")
	srv, st := syncServer(t, &places)
	srv.SyncTracker(context.Background())

	// Kitchen becomes bookable (so it's added), the nursery is renamed, is no
	// longer bookable and moves out of the building to the site.
	places = strings.Join([]string{syncSite, syncMain,
		`{"id": 4, "parent_id": 1, "kind": "room", "number": "104", "name": "Baby room", "path": "Campus › 104 – Baby room", "bookable": false}`,
		strings.Replace(syncKitchen, `"bookable": false`, `"bookable": true`, 1)}, ",")
	res, err := srv.SyncTracker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0] != "Kitchen" {
		t.Errorf("added = %v", res.Added)
	}
	baby := roomNamed(t, st, "104 – Baby room")
	if baby == nil || baby.Bookable || baby.ParentID != 0 {
		t.Fatalf("nursery after sync = %+v", baby)
	}
	if k := roomNamed(t, st, "Kitchen"); k == nil || k.ParentID != roomNamed(t, st, "Main building").ID {
		t.Errorf("kitchen = %+v", k)
	}
}

func TestTrackerSyncFailureArchivesNothing(t *testing.T) {
	places := strings.Join([]string{syncSite, syncMain, syncNursery}, ",")
	srv, st := syncServer(t, &places)
	srv.SyncTracker(context.Background())

	// The tracker stops answering properly: a different server that errors.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()
	env := map[string]string{"TRACKER_URL": down.URL, "TRACKER_TOKEN": "mt_good1234"}
	srv2, err := New(Config{Env: func(k string) string { return env[k] }}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv2.SyncTracker(context.Background()); err == nil {
		t.Error("expected the failed call to be reported")
	}
	rooms, _ := st.ListRooms(true)
	if len(rooms) != 2 {
		t.Fatalf("rooms = %d", len(rooms))
	}
	for _, rm := range rooms {
		if rm.Archived {
			t.Errorf("%s was archived by a failed sync", rm.Name)
		}
	}
}

func TestTrackerSyncOffAndOnConnectionTest(t *testing.T) {
	c, st := newTestServer(t)
	_ = c
	srv, err := New(Config{Env: func(string) string { return "" }}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := srv.SyncTracker(context.Background()); err != nil || len(res.Added) != 0 {
		t.Errorf("not connected should do nothing: %+v, %v", res, err)
	}

	// A passing Test on the Connections page syncs.
	places := strings.Join([]string{syncSite, syncMain}, ",")
	tr := fakeTrackerApp(t, &places)
	admin, st2 := adminClient(t, Config{})
	admin.post("/admin/connections", "/admin/connections/tracker", map[string][]string{"tracker_url": {tr.URL}, "tracker_token": {"mt_good1234"}}, 200)
	if roomNamed(t, st2, "Main building") == nil {
		t.Error("a passing connection test should add the bookable places")
	}
}

func TestRoomFormAndRequestGrouping(t *testing.T) {
	places := strings.Join([]string{syncSite, syncMain, syncNursery}, ",")
	tr := fakeTrackerApp(t, &places)
	env := map[string]string{"TRACKER_URL": tr.URL, "TRACKER_TOKEN": "mt_good1234"}
	c, st := newTestServerWith(t, Config{Env: func(k string) string { return env[k] }})
	st.CreateUser("sam", "", "a long password", store.RoleScheduler)
	sched := c.as("sam")
	if _, err := st.ImportRooms([]store.TrackerPlace{
		{ID: 2, Label: "Main building", Name: "Main building", Kind: "building", Bookable: true},
		{ID: 4, Label: "104 – Nursery", Name: "Nursery", Kind: "room", ParentID: 2, Bookable: true},
	}, []int64{2, 4}); err != nil {
		t.Fatal(err)
	}
	main, nursery := roomNamed(t, st, "Main building"), roomNamed(t, st, "104 – Nursery")
	hand := store.Room{Name: "Porch", Bookable: true}
	handID, _ := st.CreateRoom(&hand)

	// Imported: read-only box with a link to the tracker, and no Inside.
	body := sched.get("/rooms/"+itoa(nursery.ID)+"/edit", 200)
	if !strings.Contains(body, "Set in the tracker") || !strings.Contains(body, tr.URL+"/places/4") || strings.Contains(body, `name="bookable"`) || strings.Contains(body, `name="parent"`) {
		t.Errorf("imported room form:\n%s", body)
	}
	// Saving keeps the tracker's flag even if the form says otherwise.
	sched.post("/rooms/"+itoa(nursery.ID)+"/edit", "/rooms/"+itoa(nursery.ID), map[string][]string{"name": {"104 – Nursery"}, "bookable": {""}}, 200)
	// Hand-made: checkbox and Inside; a room can't be inside itself.
	body = sched.get("/rooms/"+itoa(handID)+"/edit", 200)
	if !strings.Contains(body, `name="bookable"`) || !strings.Contains(body, `name="parent"`) || strings.Contains(body, `value="`+itoa(handID)+`">Porch`) {
		t.Errorf("hand-made room form:\n%s", body)
	}
	sched.post("/rooms/"+itoa(handID)+"/edit", "/rooms/"+itoa(handID), map[string][]string{"name": {"Porch"}, "bookable": {"1"}, "parent": {itoa(main.ID)}}, 200)
	if got, _ := st.GetRoom(handID); got.ParentID != main.ID {
		t.Errorf("porch parent = %d", got.ParentID)
	}
	if got, _ := st.GetRoom(nursery.ID); !got.Bookable || got.ParentID != main.ID {
		t.Errorf("imported room changed by the form: %+v", got)
	}
	// The request form groups rooms under the building, which can be picked whole.
	body = sched.get("/request", 200)
	if !strings.Contains(body, `room-group__name">Main building`) || !strings.Contains(body, "Whole building") ||
		!strings.Contains(body, `data-room-id="`+itoa(main.ID)+`"`) || !strings.Contains(body, `data-room-id="`+itoa(nursery.ID)+`"`) {
		t.Errorf("request form:\n%s", body)
	}
}

// A tracker too old to send bookable: nothing is added by the sync, and
// imported rooms keep the calendar's own flag rather than all turning off.
func TestTrackerSyncWithoutBookableFlag(t *testing.T) {
	old := func(p string) string {
		p = strings.Replace(p, `, "bookable": true`, "", 1)
		return strings.Replace(p, `, "bookable": false`, "", 1)
	}
	places := strings.Join([]string{syncSite, old(syncMain), old(syncNursery)}, ",")
	srv, st := syncServer(t, &places)
	if _, err := st.ImportRooms([]store.TrackerPlace{{ID: 4, Label: "104 – Nursery", Name: "Nursery", Kind: "room", NoFlag: true}}, []int64{4}); err != nil {
		t.Fatal(err)
	}
	res, err := srv.SyncTracker(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 0 || len(res.Flagged) != 0 {
		t.Errorf("result = %+v", res)
	}
	if rm := roomNamed(t, st, "104 – Nursery"); rm == nil || !rm.Bookable {
		t.Errorf("nursery = %+v, want still bookable", rm)
	}
}
