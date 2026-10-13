package server

import (
	"strings"
	"testing"

	"github.com/tylergoza/church-calendar/internal/store"
)

// A visitor sees a pending booking only as a busy block with the mark;
// members see it in full with the Pending label.
func TestPendingVisibility(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	mel := c.as("mel")
	day := soon(2)
	hall := addRoom(t, st, "Hall")
	book(t, st, store.Booking{Title: "Quiet vigil", Visibility: "private", Status: "pending",
		StartsOn: day, StartTime: "18:00", EndTime: "20:00"}, hall)

	visitor := c.get("/calendar?month="+day[:7], 200)
	if strings.Contains(visitor, "Quiet vigil") {
		t.Error("a visitor saw a pending title")
	}
	if !strings.Contains(visitor, "Hall unavailable · 6:00–8:00 pm · pending") {
		t.Errorf("visitor busy block lacks the pending mark:\n%s", visitor)
	}
	feed, _ := c.apiGet("/calendar.ics?from="+day+"&to="+day, "", 200)
	if strings.Contains(feed, "Quiet vigil") || !strings.Contains(feed, "SUMMARY:Busy: Hall") {
		t.Errorf("visitor feed:\n%s", feed)
	}

	member := mel.get("/calendar?month="+day[:7], 200)
	if !strings.Contains(member, "Quiet vigil") || !strings.Contains(member, "Pending") {
		t.Error("a member should see the private pending booking in full with the label")
	}
}

func TestPendingICSStatus(t *testing.T) {
	c, st := newTestServer(t)
	day := soon(2)
	hall := addRoom(t, st, "Hall")
	book(t, st, store.Booking{Title: "Maybe", Visibility: "public", Status: "pending", StartsOn: day}, hall)
	book(t, st, store.Booking{Title: "Hidden", Visibility: "private", Status: "pending", StartsOn: day}, hall)
	book(t, st, store.Booking{Title: "Sure", Visibility: "public", StartsOn: day}, hall)
	feed, _ := c.apiGet("/calendar.ics?from="+day+"&to="+day, "", 200)
	if n := strings.Count(feed, "STATUS:TENTATIVE"); n != 2 {
		t.Errorf("STATUS:TENTATIVE count = %d, want 2 (full and busy pending)\n%s", n, feed)
	}
}

func TestCalendarPendingFilter(t *testing.T) {
	c, st := newTestServer(t)
	day := soon(2)
	hall := addRoom(t, st, "Hall")
	book(t, st, store.Booking{Title: "Pending thing", Visibility: "public", Status: "pending", StartsOn: day}, hall)
	book(t, st, store.Booking{Title: "Pencilled thing", Visibility: "public", Status: "tentative", StartsOn: day}, hall)
	book(t, st, store.Booking{Title: "Firm thing", Visibility: "public", StartsOn: day}, hall)

	on := c.get("/calendar?month="+day[:7], 200)
	for _, w := range []string{"Pending thing", "Pencilled thing", "Firm thing", "Pencilled in"} {
		if !strings.Contains(on, w) {
			t.Errorf("default view lacks %q", w)
		}
	}
	off := c.get("/calendar?pending=0&month="+day[:7], 200)
	if strings.Contains(off, "Pending thing") || strings.Contains(off, "Pencilled thing") || !strings.Contains(off, "Firm thing") {
		t.Error("pending=0 should hide pending and pencilled-in entries only")
	}
	if !strings.Contains(off, "pending=0") {
		t.Error("prev/next links should keep pending=0")
	}
}

// A waiting request holds its rooms: availability shows them busy.
func TestWaitingRequestBlocksAvailability(t *testing.T) {
	c, st := newTestServer(t)
	hall := addRoom(t, st, "Hall")
	day := soon(4)
	c.post("/request", "/request", requestForm("Wedding rehearsal", day, hall), 200)

	body, _ := c.apiGet("/request/availability?starts_on="+day+"&start_time=18:00&end_time=20:00", "", 200)
	if strings.Contains(body, "Wedding rehearsal") || !strings.Contains(body, `"free":false`) {
		t.Errorf("request availability = %s", body)
	}

	tok := sourceToken(t, st, "events", false)
	var av struct{ Busy []apiBlockJSON }
	c.apiDo("GET", "/api/v1/availability?rooms=Hall&from="+day+"&to="+day, tok, "", 200, &av)
	if len(av.Busy) != 1 || !av.Busy[0].Busy || av.Busy[0].Title != "" {
		t.Errorf("api availability = %+v", av.Busy)
	}
}
