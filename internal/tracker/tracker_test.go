package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fakePlaces = `{"places": [
  {"id": 1, "parent_id": null, "kind": "site", "number": "", "name": "Main Campus", "path": "Main Campus",
   "address": "", "notes": "", "tags": [], "also_in": []},
  {"id": 2, "parent_id": 1, "kind": "building", "number": "", "name": "Fellowship Hall", "path": "Main Campus › Fellowship Hall",
   "address": "", "notes": "", "tags": ["event"], "also_in": []},
  {"id": 3, "parent_id": 2, "kind": "floor", "number": "", "name": "Ground floor", "path": "Main Campus › Fellowship Hall › Ground floor",
   "address": "", "notes": "", "tags": [], "also_in": []},
  {"id": 4, "parent_id": 3, "kind": "room", "number": "104", "name": "Nursery", "path": "Main Campus › Fellowship Hall › Ground floor › 104 – Nursery",
   "address": "", "notes": "", "tags": [], "also_in": []},
  {"id": 5, "parent_id": 3, "kind": "area", "number": "", "name": "Kitchen Pass-through", "path": "Main Campus › Fellowship Hall › Ground floor › Kitchen Pass-through",
   "address": "", "notes": "", "tags": [], "also_in": [4]},
  {"id": 6, "parent_id": 1, "kind": "room", "number": "201", "name": "Sanctuary", "path": "Main Campus › 201 – Sanctuary",
   "address": "", "notes": "", "tags": ["event"], "also_in": []}
]}`

const fakePlace = `{"id": 4, "parent_id": 3, "kind": "room", "number": "104", "name": "Nursery",
 "path": "Main Campus › Fellowship Hall › Ground floor › 104 – Nursery",
 "address": "", "notes": "", "tags": [], "also_in": [],
 "children": [], "items": [{"id": 9, "name": "Crib"}], "supplies": []}`

// fakeTracker serves the places endpoints and records the Authorization
// header it was sent with.
func fakeTracker(t *testing.T, token string, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/places":
			if r.URL.RawQuery != "" {
				t.Errorf("Places sent unexpected query %q", r.URL.RawQuery)
			}
			w.Write([]byte(fakePlaces))
		case "/api/v1/places/4":
			w.Write([]byte(fakePlace))
		case "/api/v1/places/99":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such route"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlaces(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)
	c := New(srv.URL, "secret")

	places, err := c.Places(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 6 {
		t.Fatalf("got %d places, want 6", len(places))
	}
	if places[0].ParentID != nil {
		t.Errorf("site ParentID = %v, want nil", *places[0].ParentID)
	}
	room := places[3]
	if room.ID != 4 || room.Kind != "room" || room.Number != "104" || room.Name != "Nursery" {
		t.Errorf("room decoded wrong: %+v", room)
	}
	if room.ParentID == nil || *room.ParentID != 3 {
		t.Errorf("room ParentID wrong: %v", room.ParentID)
	}
	if len(places[4].AlsoIn) != 1 || places[4].AlsoIn[0] != 4 {
		t.Errorf("area AlsoIn = %v, want [4]", places[4].AlsoIn)
	}
	if auth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer secret")
	}
}

func TestPlace(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)
	c := New(srv.URL, "secret")

	p, err := c.Place(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 4 || p.Name != "Nursery" || p.Kind != "room" || p.Number != "104" {
		t.Errorf("Place decoded wrong: %+v", p)
	}
	if p.ParentID == nil || *p.ParentID != 3 {
		t.Errorf("ParentID wrong: %v", p.ParentID)
	}
	if auth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer secret")
	}

	_, err = c.Place(context.Background(), 99)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Place(99) error = %v, want ErrNotFound", err)
	}
}

func TestRooms(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)
	c := New(srv.URL, "secret")
	ctx := context.Background()

	names := func(ps []Place) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	tests := []struct {
		query string
		want  []string
	}{
		// Empty query: buildings, rooms and areas only, not the site or floor.
		{"", []string{"Fellowship Hall", "Nursery", "Kitchen Pass-through", "Sanctuary"}},
		// Matches name, case-insensitively.
		{"NURSERY", []string{"Nursery"}},
		// Matches number.
		{"201", []string{"Sanctuary"}},
		// Matches path, but not floors or sites even when the path matches.
		{"fellowship", []string{"Fellowship Hall", "Nursery", "Kitchen Pass-through"}},
		// Whitespace-only query is treated as empty.
		{"   ", []string{"Fellowship Hall", "Nursery", "Kitchen Pass-through", "Sanctuary"}},
		{"ground floor", []string{"Nursery", "Kitchen Pass-through"}},
		{"nothing like this", nil},
	}
	for _, tt := range tests {
		got, err := c.Rooms(ctx, tt.query)
		if err != nil {
			t.Fatalf("Rooms(%q): %v", tt.query, err)
		}
		if !equal(names(got), tt.want) {
			t.Errorf("Rooms(%q) = %v, want %v", tt.query, names(got), tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)

	if err := New(srv.URL, "secret").Check(context.Background()); err != nil {
		t.Errorf("Check with good token: %v", err)
	}
	if err := New("", "secret").Check(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Check without address = %v, want ErrNotConfigured", err)
	}
	if err := New(srv.URL, "").Check(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Check without token = %v, want ErrNotConfigured", err)
	}
}

func TestPageURLPlace(t *testing.T) {
	c := New("https://tracker.example.org/", "tok")
	if got, want := c.PageURL("place", 4), "https://tracker.example.org/places/4"; got != want {
		t.Errorf("PageURL(place) = %q, want %q", got, want)
	}
	if got, want := c.PageURL("places", 4), "https://tracker.example.org/places/4"; got != want {
		t.Errorf("PageURL(places) = %q, want %q", got, want)
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		place Place
		want  string
	}{
		{Place{Number: "104", Name: "Nursery"}, "104 – Nursery"},
		{Place{Name: "Fellowship Hall"}, "Fellowship Hall"},
	}
	for _, tt := range tests {
		if got := tt.place.Label(); got != tt.want {
			t.Errorf("Label(%+v) = %q, want %q", tt.place, got, tt.want)
		}
	}
}

func TestPlacesJSONRoundTrip(t *testing.T) {
	// Guard that the struct tags match the wire names.
	var p Place
	if err := json.Unmarshal([]byte(`{"id":7,"parent_id":2,"kind":"area","number":"","name":"Stage","path":"x","also_in":[3]}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != 7 || p.ParentID == nil || *p.ParentID != 2 || p.Kind != "area" || len(p.AlsoIn) != 1 {
		t.Errorf("unexpected decode: %+v", p)
	}
}

func equipmentTracker(t *testing.T, products string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/products":
			if r.URL.Query().Get("booking") == "1" {
				w.Write([]byte(products))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
		case "/api/v1/items":
			if r.URL.Query().Get("product") != "7" {
				t.Errorf("Items asked for %q", r.URL.RawQuery)
			}
			w.Write([]byte(`{"items": [
			  {"id": 1, "product_id": 7, "quantity": 30, "place": {"id": 9, "path": "Main Campus › Lifecenter"}},
			  {"id": 2, "product_id": 7, "quantity": 20, "place": {"id": 11, "path": "Main Campus › Storage B"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "secret")
}

func TestProducts(t *testing.T) {
	c := equipmentTracker(t, `{"products": [
	  {"id": 7, "name": "Round tables", "booking": "count", "total": 50, "notes": "Seats eight", "offered_in": [9, 4]},
	  {"id": 8, "name": "Sound", "booking": "service", "total": 0, "notes": "", "offered_in": [9]},
	  {"id": 9, "name": "Ladder", "booking": "", "total": 2, "notes": "", "offered_in": []}]}`)
	got, err := c.Products(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d products, want the 2 that are offered: %+v", len(got), got)
	}
	if p := got[0]; p.ID != 7 || p.Name != "Round tables" || p.Booking != "count" || p.Total != 50 || p.Notes != "Seats eight" || len(p.OfferedIn) != 2 || p.OfferedIn[0] != 9 {
		t.Errorf("product decoded wrong: %+v", p)
	}
	if got[1].Booking != "service" {
		t.Errorf("service decoded wrong: %+v", got[1])
	}
}

func TestProductsTooOld(t *testing.T) {
	// An old tracker ignores booking=1 and sends products without the field.
	c := equipmentTracker(t, `{"products": [{"id": 7, "name": "Round tables", "total": 50}]}`)
	if _, err := c.Products(context.Background(), true); !errors.Is(err, ErrTooOld) {
		t.Errorf("Products from an old tracker = %v, want ErrTooOld", err)
	}
	// No products at all is a real answer, not an old tracker.
	c = equipmentTracker(t, `{"products": []}`)
	if got, err := c.Products(context.Background(), true); err != nil || len(got) != 0 {
		t.Errorf("Products with none = %v, %v", got, err)
	}
	// A tracker that 404s on the question is too old as well.
	c = equipmentTracker(t, `{"products": []}`)
	if _, err := c.Products(context.Background(), false); !errors.Is(err, ErrTooOld) {
		t.Errorf("Products(false) on a 404 = %v, want ErrTooOld", err)
	}
}

func TestItems(t *testing.T) {
	c := equipmentTracker(t, `{"products": []}`)
	items, err := c.Items(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Quantity != 30 || items[0].Place.ID != 9 || items[1].Place.Path != "Main Campus › Storage B" {
		t.Errorf("items decoded wrong: %+v", items)
	}
}
