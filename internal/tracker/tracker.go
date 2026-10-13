// Package tracker reads places from the facility maintenance tracker's API,
// so the calendar can import them as rooms. The API is read-only and
// authorised with a token an admin makes on the tracker's API page.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotConfigured means there's no tracker address or token.
var ErrNotConfigured = errors.New("the maintenance tracker isn't connected; an admin can add it on Connections")

// ErrNotFound is the tracker saying it has no such thing.
var ErrNotFound = errors.New("not found in the maintenance tracker")

// cacheFor is how long answers are reused, so a page that shows many
// needs asks the tracker once.
const cacheFor = time.Minute

type Client struct {
	BaseURL string // e.g. "https://maintenance.example.org"
	Token   string
	HTTP    *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	body []byte
	at   time.Time
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: 8 * time.Second},
		cache:   map[string]cached{},
	}
}

func (c *Client) Configured() bool { return c != nil && c.BaseURL != "" && c.Token != "" }

// PageURL links to a page in the tracker itself, e.g. PageURL("place", 4).
// Kind "place" maps to the tracker's "places" pages.
func (c *Client) PageURL(kind string, id int64) string {
	if c == nil || c.BaseURL == "" {
		return ""
	}
	if kind == "place" {
		kind = "places"
	}
	return c.BaseURL + "/" + kind + "/" + strconv.FormatInt(id, 10)
}

// Forget drops cached answers, e.g. after something was changed there.
func (c *Client) Forget() {
	c.mu.Lock()
	c.cache = map[string]cached{}
	c.mu.Unlock()
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	if !c.Configured() {
		return ErrNotConfigured
	}
	u := c.BaseURL + "/api/v1" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	c.mu.Lock()
	hit, ok := c.cache[u]
	c.mu.Unlock()
	if ok && time.Since(hit.at) < cacheFor {
		return json.Unmarshal(hit.body, out)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach the maintenance tracker: %w", err)
	}
	defer resp.Body.Close()
	var body json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("the maintenance tracker sent something unexpected (HTTP %d)", resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("the maintenance tracker turned down the API token; it may have been revoked")
	case resp.StatusCode != http.StatusOK:
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(body, &e)
		return fmt.Errorf("the maintenance tracker said: %s (HTTP %d)", e.Error, resp.StatusCode)
	}
	c.mu.Lock()
	c.cache[u] = cached{body: body, at: time.Now()}
	c.mu.Unlock()
	return json.Unmarshal(body, out)
}

// JSON shape, as the tracker's API sends it -----------------------------------

// Place is a site, building, floor, room or area. ParentID is nil for a
// site. Path is the full name, e.g. "Main Building › 2nd floor › 104 – Nursery".
// Kind is one of "site", "building", "floor", "room" or "area".
type Place struct {
	ID       int64    `json:"id"`
	ParentID *int64   `json:"parent_id"`
	Kind     string   `json:"kind"`
	Number   string   `json:"number"`
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Address  string   `json:"address"`
	Notes    string   `json:"notes"`
	Tags     []string `json:"tags"`
	AlsoIn   []int64  `json:"also_in"`  // areas only: other places a shared area is in
	Bookable *bool    `json:"bookable"` // the tracker says people may book it; nil from a tracker too old to say
}

// Label is the room's name as the tracker shows it: "number – name" when it
// has a number, else the name.
func (p Place) Label() string {
	if p.Number == "" {
		return p.Name
	}
	return p.Number + " – " + p.Name
}

// Product is something the church has, counted or not. Booking says how
// the calendar offers it with a room booking: "count" (asked how many, up
// to Total), "service" (yes or no) or "" (not offered). OfferedIn is the
// places it's offered with.
type Product struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Booking   string  `json:"booking"`
	Total     int     `json:"total"`
	Notes     string  `json:"notes"`
	OfferedIn []int64 `json:"offered_in"`
}

// Item is some of a product in one place. Quantity is how many (1 for
// a product that isn't counted).
type Item struct {
	ID       int64    `json:"id"`
	Quantity int      `json:"quantity"`
	Place    PlaceRef `json:"place"`
}

// PlaceRef is a place as other things point at it.
type PlaceRef struct {
	ID   int64  `json:"id"`
	Path string `json:"path"`
}

// ErrTooOld means the tracker doesn't know about bookable equipment yet
// (it sent products with no "booking" field), so there's nothing to sync.
var ErrTooOld = errors.New("the maintenance tracker is too old to offer equipment; it needs updating")

// Calls ----------------------------------------------------------------------------

// Places is every place, in tree order (parents before children).
func (c *Client) Places(ctx context.Context) ([]Place, error) {
	var out struct {
		Places []Place `json:"places"`
	}
	err := c.get(ctx, "/places", nil, &out)
	return out.Places, err
}

// Products is the products the tracker offers with room bookings (asked
// with booking=1; an empty list means none). A tracker that doesn't send
// "booking" at all is too old and gets ErrTooOld, as does one that doesn't
// know the question (not found), so a sync never takes that for "none".
func (c *Client) Products(ctx context.Context, booking bool) ([]Product, error) {
	var q url.Values
	if booking {
		q = url.Values{"booking": {"1"}}
	}
	var raw struct {
		Products []struct {
			Product
			Booking *string `json:"booking"`
		} `json:"products"`
	}
	if err := c.get(ctx, "/products", q, &raw); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrTooOld
		}
		return nil, err
	}
	out := make([]Product, 0, len(raw.Products))
	known := false
	for _, p := range raw.Products {
		if p.Booking != nil {
			known = true
			p.Product.Booking = *p.Booking
		}
		if booking && p.Product.Booking == "" {
			continue
		}
		out = append(out, p.Product)
	}
	if len(raw.Products) > 0 && !known {
		return nil, ErrTooOld
	}
	return out, nil
}

// Items is where a product is: its items, each with a quantity and place.
func (c *Client) Items(ctx context.Context, productID int64) ([]Item, error) {
	var out struct {
		Items []Item `json:"items"`
	}
	err := c.get(ctx, "/items", url.Values{"product": {strconv.FormatInt(productID, 10)}}, &out)
	return out.Items, err
}

// Place is one place. The tracker also sends its children, items and
// supplies; those aren't decoded here.
func (c *Client) Place(ctx context.Context, id int64) (*Place, error) {
	var out Place
	if err := c.get(ctx, "/places/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Rooms is the places something can be booked in (buildings, rooms and
// areas) that match query, case-insensitively, against name, number or
// path. An empty query returns all of them, in tree order.
func (c *Client) Rooms(ctx context.Context, query string) ([]Place, error) {
	places, err := c.Places(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Place
	for _, p := range places {
		switch p.Kind {
		case "building", "room", "area":
		default:
			continue
		}
		if q == "" ||
			strings.Contains(strings.ToLower(p.Name), q) ||
			strings.Contains(strings.ToLower(p.Number), q) ||
			strings.Contains(strings.ToLower(p.Path), q) {
			out = append(out, p)
		}
	}
	return out, nil
}

// Check sees that the address and token work. It asks for a place that
// can't exist: with a good token the tracker says "not found", with a bad
// one it says so first.
func (c *Client) Check(ctx context.Context) error {
	var out struct{}
	if err := c.get(ctx, "/places/0", nil, &out); !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}
