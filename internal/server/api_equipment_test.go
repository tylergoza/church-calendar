package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// Equipment is in the read API only on bookings the token sees in full,
// never on busy blocks or in the .ics feeds, and phone numbers never leave.
func TestReadAPIEquipment(t *testing.T) {
	c, st := newTestServer(t)
	hall, _ := st.CreateRoom(&store.Room{Name: "Lifecenter", Bookable: true})
	kitchen, _ := st.CreateRoom(&store.Room{Name: "Kitchen", Bookable: true})
	chairs, _ := st.CreateEquipment(&store.Equipment{Name: "Chairs", Mode: store.ModeCount, Total: 40, ProductID: 12, RoomIDs: []int64{hall, kitchen}})
	sound, _ := st.CreateEquipment(&store.Equipment{Name: "Sound", Mode: store.ModeService, RoomIDs: []int64{hall, kitchen}})
	day := soon(3)
	r := store.Request{Name: "Jo", Phone: "555-0199", Title: "Supper", StartsOn: day, StartTime: "17:00", EndTime: "20:00",
		Equipment: []store.EquipmentLine{{EquipmentID: chairs, Quantity: 30}, {EquipmentID: sound, Quantity: 1, Note: "Two mics"}}}
	if err := st.CreateRequest(&r, []int64{hall}); err != nil {
		t.Fatal(err)
	}
	locked := book(t, st, store.Booking{Title: "Board", Visibility: "locked", StartsOn: day, StartTime: "18:00", EndTime: "19:00"}, kitchen)
	st.SetBookingEquipment(locked, []store.EquipmentLine{{EquipmentID: chairs, Quantity: 20}})

	type event struct {
		ID        int64
		Busy      bool
		Equipment []apiEquipmentJSON
	}
	read := func(tok string) []event {
		body, _ := c.apiGet("/api/v1/events", tok, 200)
		if strings.Contains(body, "555-0199") {
			t.Error("a phone number got into the API")
		}
		var out struct{ Events []event }
		json.Unmarshal([]byte(body), &out)
		return out.Events
	}

	// A token that can't see private bookings sees both as busy.
	plain, _ := st.CreateReadToken("Website", false, 0)
	for _, e := range read(plain) {
		if !e.Busy || len(e.Equipment) != 0 {
			t.Errorf("busy block with equipment: %+v", e)
		}
	}

	// One that can sees the request's booking in full; the locked one stays busy.
	full, _ := st.CreateReadToken("Tracker", true, 0)
	var seen bool
	for _, e := range read(full) {
		switch {
		case e.Busy && len(e.Equipment) != 0:
			t.Errorf("busy block with equipment: %+v", e)
		case e.ID == r.BookingID:
			seen = true
			if len(e.Equipment) != 2 {
				t.Fatalf("equipment = %+v", e.Equipment)
			}
			ch, so := e.Equipment[0], e.Equipment[1]
			if ch.Name != "Chairs" || ch.TrackerProductID != 12 || ch.Quantity != 30 || ch.Short != 10 {
				t.Errorf("chairs = %+v, want short by 10 with the locked booking", ch)
			}
			if so.Name != "Sound" || so.Note != "Two mics" || so.Short != 0 || so.TrackerProductID != 0 {
				t.Errorf("sound = %+v", so)
			}
		}
	}
	if !seen {
		t.Error("the request's booking wasn't in full")
	}
	if body, _ := c.apiGet("/api/v1/events/"+itoa(r.BookingID), full, 200); !strings.Contains(body, `"tracker_product_id": 12`) {
		t.Errorf("one event has no equipment: %s", body)
	}

	// The feeds never carry equipment.
	for _, path := range []string{"/api/v1/events.ics?token=" + url.QueryEscape(full), "/calendar.ics"} {
		if body, _ := c.apiGet(path, "", 200); strings.Contains(body, "Chairs") || strings.Contains(body, "mics") {
			t.Errorf("%s has equipment:\n%s", path, body)
		}
	}
}
