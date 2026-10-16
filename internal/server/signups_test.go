package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

func signupPush(day, visibility, signup string) string {
	return `{"title": "Chili Cook-off", "starts_on": "` + day + `", "start_time": "17:30", "visibility": "` + visibility + `",
		"rooms": [{"name": "Lifecenter"}]` + signup + `}`
}

const openSignup = `, "signup": {"label": "Sign up for the cook-off", "url": "https://events.example.org/signup/k3J9xQ", "state": "open"}`

// TestSignupPush checks a pushed sign-up is stored, cleared by a push
// without one, and dropped (never refused) when locked or on the wrong host.
func TestSignupPush(t *testing.T) {
	c, st := newTestServerWith(t, Config{Env: func(k string) string {
		if k == "EVENTS_URL" {
			return "https://events.example.org"
		}
		return ""
	}})
	addRoom(t, st, "Lifecenter")
	tok := sourceToken(t, st, "events", false)
	day := soon(4)
	get := func(id int64) store.Signup {
		t.Helper()
		b, err := st.GetBooking(id)
		if err != nil {
			t.Fatal(err)
		}
		return b.Signup
	}

	var a putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, signupPush(day, "public", openSignup), 200, &a)
	if sg := get(a.ID); sg.URL != "https://events.example.org/signup/k3J9xQ" || sg.State != "open" || sg.Label != "Sign up for the cook-off" {
		t.Errorf("stored = %+v", sg)
	}
	if a.Booking.Signup == nil || a.Booking.Signup.State != "open" {
		t.Errorf("answer signup = %+v", a.Booking.Signup)
	}

	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, signupPush(day, "public", ""), 200, &a)
	if sg := get(a.ID); sg != (store.Signup{}) {
		t.Errorf("no signup should clear it, got %+v", sg)
	}
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, signupPush(day, "public", openSignup), 200, &a)
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, signupPush(day, "public", `, "signup": null`), 200, &a)
	if sg := get(a.ID); sg.URL != "" {
		t.Errorf("null should clear it, got %+v", sg)
	}

	c.apiDo("PUT", "/api/v1/bookings/events/2", tok, signupPush(day, "locked", openSignup), 200, &a)
	if sg := get(a.ID); sg.URL != "" {
		t.Errorf("locked keeps no signup, got %+v", sg)
	}

	for name, sg := range map[string]string{
		"wrong host": `, "signup": {"url": "https://evil.example.com/signup/x", "state": "open"}`,
		"http":       `, "signup": {"url": "http://events.example.org/signup/x", "state": "open"}`,
		"bad state":  `, "signup": {"url": "https://events.example.org/signup/x", "state": "maybe"}`,
	} {
		c.apiDo("PUT", "/api/v1/bookings/events/3", tok, signupPush(day, "public", sg), 200, &a)
		if got := get(a.ID); got.URL != "" {
			t.Errorf("%s: should be dropped, got %+v", name, got)
		}
	}

	// No label gets the plain one; an unreadable close time is dropped.
	c.apiDo("PUT", "/api/v1/bookings/events/4", tok, signupPush(day, "public",
		`, "signup": {"url": "https://events.example.org/signup/y", "state": "full", "closes_at": "soon"}`), 200, &a)
	if sg := get(a.ID); sg.Label != "Sign up" || sg.State != "full" || sg.ClosesAt != "" {
		t.Errorf("tidied = %+v", sg)
	}
}

func TestSignupURLOK(t *testing.T) {
	for _, tc := range []struct {
		raw, planner string
		want         bool
	}{
		{"https://anywhere.example.org/s/1", "", true},
		{"http://anywhere.example.org/s/1", "", false},
		{"http://localhost:8110/s/1", "", true},
		{"https://events.example.org/s/1", "https://events.example.org", true},
		{"https://other.example.org/s/1", "https://events.example.org", false},
		{"http://events.example.org/s/1", "https://events.example.org", false},
		{"http://127.0.0.1:8110/s/1", "http://127.0.0.1:8110", true},
		{"javascript:alert(1)", "", false},
		{"https://user:pw@events.example.org/s/1", "", false},
	} {
		if got := signupURLOK(tc.raw, tc.planner); got != tc.want {
			t.Errorf("signupURLOK(%q, %q) = %v, want %v", tc.raw, tc.planner, got, tc.want)
		}
	}
}

func TestSignupStateAt(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	sg := store.Signup{URL: "https://x.example.org", State: "open", ClosesAt: "2026-11-01T23:59"}
	if got := sg.StateAt(now); got != "closed" {
		t.Errorf("past close = %q, want closed", got)
	}
	sg.ClosesAt = "2026-11-03T23:59"
	if got := sg.StateAt(now); got != "open" {
		t.Errorf("future close = %q, want open", got)
	}
	if got := (store.Signup{State: "open"}).StateAt(now); got != "" {
		t.Errorf("no url = %q, want none", got)
	}
}

// TestSignupShown checks who sees the link: public to everyone, private
// only to members and tokens with private access, and a past close time
// reads as closed everywhere.
func TestSignupShown(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("mel", "", "a long password", store.RoleMember)
	mel := c.as("mel")
	addRoom(t, st, "Lifecenter")
	tok := sourceToken(t, st, "events", false)
	day := soon(3)
	var pub, priv, past putAnswer
	c.apiDo("PUT", "/api/v1/bookings/events/1", tok, signupPush(day, "public", openSignup), 200, &pub)
	c.apiDo("PUT", "/api/v1/bookings/events/2", tok, strings.Replace(signupPush(day, "private", openSignup), "Chili", "Board", 1), 200, &priv)
	c.apiDo("PUT", "/api/v1/bookings/events/3", tok, strings.Replace(signupPush(day, "public",
		`, "signup": {"url": "https://events.example.org/signup/old", "state": "open", "closes_at": "2020-01-01T00:00"}`), "Chili", "Old", 1), 200, &past)

	if body := c.get("/calendar/"+itoa(pub.ID), 200); !strings.Contains(body, "Sign up for the cook-off") ||
		!strings.Contains(body, `href="https://events.example.org/signup/k3J9xQ"`) {
		t.Error("public booking page should show the button")
	}
	if body := c.get("/calendar/"+itoa(past.ID), 200); !strings.Contains(body, "Sign-ups closed") || strings.Contains(body, "signup/old") {
		t.Error("past close time should read as closed, with no link")
	}
	c.get("/calendar/"+itoa(priv.ID), 404)
	if body := mel.get("/calendar/"+itoa(priv.ID), 200); !strings.Contains(body, "Sign up for the cook-off") {
		t.Error("members see a private booking's sign-up")
	}
	if body := c.get("/calendar?month="+day[:7], 200); strings.Count(body, "Sign-ups open") != 2 { // grid and agenda
		t.Errorf("month view mark: %d", strings.Count(body, "Sign-ups open"))
	}

	signups := func(token string) map[string]*apiSignupJSON {
		t.Helper()
		body, _ := c.apiGet("/api/v1/events", token, 200)
		var out struct{ Events []apiEventJSON }
		json.Unmarshal([]byte(body), &out)
		m := map[string]*apiSignupJSON{}
		for _, e := range out.Events {
			if !e.Busy {
				m[e.Title] = e.Signup
			}
		}
		return m
	}
	plain, _ := st.CreateReadToken("Website", false, 0)
	full, _ := st.CreateReadToken("Intranet", true, 0)
	if m := signups(plain); m["Chili Cook-off"] == nil || m["Chili Cook-off"].State != "open" || m["Old Cook-off"].State != "closed" {
		t.Errorf("public signups = %+v", m)
	} else if _, ok := m["Board Cook-off"]; ok {
		t.Error("a token without private access mustn't see the private booking")
	}
	if m := signups(full); m["Board Cook-off"] == nil {
		t.Error("a token with private access sees the private sign-up")
	}

	ics, _ := c.apiGet("/calendar.ics", "", 200)
	unfolded := strings.ReplaceAll(ics, "\r\n ", "")
	if !strings.Contains(unfolded, `Sign up: https://events.example.org/signup/k3J9xQ`) {
		t.Errorf("ics should carry the open link:\n%s", ics)
	}
	if strings.Contains(unfolded, "signup/old") {
		t.Error("ics mustn't carry a closed link")
	}
	if ics, _ := c.apiGet("/api/v1/events.ics?token="+url.QueryEscape(full), "", 200); strings.Count(strings.ReplaceAll(ics, "\r\n ", ""), "Sign up:") != 2 {
		t.Error("private-access feed carries both open links")
	}
}
