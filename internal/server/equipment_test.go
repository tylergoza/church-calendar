package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

func equipmentSetup(t *testing.T) (admin *client, st *store.Store, c *client) {
	t.Helper()
	c, st = newTestServer(t)
	st.CreateUser("boss", "", "a long password", store.RoleAdmin)
	st.CreateUser("sam", "", "a long password", store.RoleScheduler)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	return c.as("boss"), st, c
}

func TestEquipmentAdminOnly(t *testing.T) {
	boss, _, c := equipmentSetup(t)
	for _, who := range []string{"sam", "mel"} {
		u := c.as(who)
		u.get("/equipment", 403)
		u.get("/equipment/new", 403)
		u.post("/account", "/equipment", url.Values{"name": {"Chairs"}, "mode": {"count"}}, 403)
		if strings.Contains(u.get("/", 200), `href="/equipment"`) {
			t.Errorf("%s sees the Equipment link", who)
		}
	}
	boss.get("/equipment", 200)
	if !strings.Contains(boss.get("/", 200), `href="/equipment"`) {
		t.Error("admin doesn't see the Equipment link")
	}
}

func TestEquipmentNameUnique(t *testing.T) {
	boss, _, _ := equipmentSetup(t)
	form := func() url.Values { return url.Values{"name": {"Chairs"}, "mode": {"count"}, "total": {"40"}} }
	boss.post("/equipment/new", "/equipment", form(), 200)
	body := boss.post("/equipment/new", "/equipment", form(), 422)
	if !strings.Contains(body, "already called Chairs") {
		t.Errorf("no name error:\n%s", body)
	}
}

func TestEquipmentServiceHasNoTotal(t *testing.T) {
	boss, st, _ := equipmentSetup(t)
	boss.post("/equipment/new", "/equipment", url.Values{"name": {"Sound"}, "mode": {"service"}, "total": {"7"}}, 200)
	list, _ := st.ListEquipment(false)
	if len(list) != 1 || !list[0].Service() || list[0].Total != 0 {
		t.Fatalf("got %+v", list)
	}
}

func TestEquipmentNoRoomsIsNotOffered(t *testing.T) {
	boss, st, _ := equipmentSetup(t)
	room := &store.Room{Name: "Hall", Bookable: true}
	id, err := st.CreateRoom(room)
	if err != nil {
		t.Fatal(err)
	}
	boss.post("/equipment/new", "/equipment", url.Values{"name": {"Tables"}, "mode": {"count"}, "total": {"10"}}, 200)
	if got, _ := st.OfferedWith([]int64{id}); len(got) != 0 {
		t.Errorf("offered with %d room(s), want none", len(got))
	}
	if !strings.Contains(boss.get("/equipment", 200), "Not offered anywhere yet") {
		t.Error("list doesn't say it's not offered")
	}
	list, _ := st.ListEquipment(false)
	boss.post("/equipment/"+itoa(list[0].ID)+"/edit", "/equipment/"+itoa(list[0].ID),
		url.Values{"name": {"Tables"}, "mode": {"count"}, "room": {itoa(id), "9999"}}, 200)
	if got, _ := st.OfferedWith([]int64{id}); len(got) != 1 {
		t.Errorf("offered with %d, want 1", len(got))
	}
	body := boss.get("/equipment", 200)
	if strings.Contains(body, "Not offered anywhere yet") || !strings.Contains(body, "Hall") {
		t.Error("list should show Hall")
	}
}

func TestEquipmentImportedIsReadOnly(t *testing.T) {
	boss, st, _ := equipmentSetup(t)
	id, err := st.CreateEquipment(&store.Equipment{Name: "Projector", ProductID: 5, Mode: store.ModeCount, Total: 2})
	if err != nil {
		t.Fatal(err)
	}
	page := "/equipment/" + itoa(id) + "/edit"
	body := boss.get(page, 200)
	if !strings.Contains(body, "Set in the tracker") || strings.Contains(body, `name="name"`) {
		t.Errorf("imported edit page should be read-only:\n%s", body)
	}
	boss.post("/equipment/new", "/equipment/"+itoa(id), url.Values{"name": {"Renamed"}, "mode": {"service"}}, 403)
	e, _ := st.GetEquipment(id)
	if e.Name != "Projector" || e.Service() {
		t.Errorf("imported equipment changed: %+v", e)
	}
}
