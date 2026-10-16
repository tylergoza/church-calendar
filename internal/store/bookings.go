package store

import (
	"database/sql"
	"strings"
	"time"
)

// Sources made in this app. Every other source is an app that pushes.
const (
	SourceCalendar = "calendar" // made by hand by a scheduler
	SourceRequest  = "request"  // an approved room request
)

// Booking statuses. pending is a visitor's request waiting for a
// scheduler; tentative is a date pencilled in. Both block their rooms like
// confirmed ones. Only cancelled frees them.
var BookingStatuses = []string{"confirmed", "tentative", "pending", "cancelled"}

var bookingStatusLabels = map[string]string{
	"confirmed": "Confirmed", "tentative": "Pencilled in", "pending": "Pending", "cancelled": "Cancelled",
}

func BookingStatusLabel(s string) string { return bookingStatusLabels[s] }

// NormalizeVisibility makes v one of public, private or locked; anything
// else is private.
func NormalizeVisibility(v string) string {
	switch v {
	case "public", "locked":
		return v
	}
	return "private"
}

// querier is what both *sql.DB and *sql.Tx offer.
type querier interface {
	Exec(q string, args ...any) (sql.Result, error)
	Query(q string, args ...any) (*sql.Rows, error)
	QueryRow(q string, args ...any) *sql.Row
}

// BookingRoom is a room a booking uses. RoomID is 0 for a place the
// calendar doesn't know (sent by an app by name): it's shown, but not
// checked for conflicts.
type BookingRoom struct {
	RoomID int64
	Name   string
}

// Booking is one block of time on the calendar. Dates are YYYY-MM-DD,
// times HH:MM ("" for all day). EndsOn "" means the same day.
type Booking struct {
	ID       int64
	Source   string
	SourceID string // the other app's ID; "" for calendar and request
	// SeriesID is the hand-made repeat this is a date of, 0 if none.
	// OccurrenceDate is the date the series' rule gave it (it stays even if
	// the booking is moved); "" when not in a series.
	SeriesID       int64
	OccurrenceDate string
	Title          string
	Kind           string
	StartsOn       string
	StartTime      string
	EndsOn         string
	EndTime        string
	Visibility     string // public, private or locked
	Description    string // the public description only
	URL            string // back to the owning app's page
	Status         string
	CreatedBy      int64
	CreatedAt      string
	UpdatedAt      string
	CancelNote     string // to the person, when a scheduler cancelled it
	CancelledBy    int64
	Signup         Signup // only on app bookings; the zero value is none
	Rooms          []BookingRoom
}

// Signup is a sign-up link an app sent with its booking. The calendar never
// holds the form, counts or answers, only where to go and whether it's open.
type Signup struct {
	Label    string
	URL      string // "" = no sign-up
	State    string // open, full or closed
	ClosesAt string // local time, "2026-11-01T23:59"; "" for none
}

// SignupStates are the states an app may send.
var SignupStates = []string{"open", "full", "closed"}

// SignupClosesLayout is how ClosesAt is written.
const SignupClosesLayout = "2006-01-02T15:04"

// StateAt is the state as of now: a close time already passed reads as
// closed even before the app says so. "" when there is no sign-up.
func (s Signup) StateAt(now time.Time) string {
	if s.URL == "" {
		return ""
	}
	if s.ClosesAt != "" && now.Format(SignupClosesLayout) >= s.ClosesAt {
		return "closed"
	}
	return s.State
}

// Current is StateAt the server's time now.
func (s Signup) Current() string { return s.StateAt(time.Now()) }

// NotYet reports whether the booking is pending or pencilled in: it blocks
// its rooms, but isn't certain yet.
func (b Booking) NotYet() bool { return b.Status == "pending" || b.Status == "tentative" }

// Cancelled reports whether the booking was cancelled (kept, off the calendar).
func (b Booking) Cancelled() bool { return b.Status == "cancelled" }

// HandMade reports whether the booking belongs to this app (so it can be
// edited here) rather than to an app that pushed it.
func (b Booking) HandMade() bool { return b.Source == SourceCalendar || b.Source == SourceRequest }

// LastDate is the day the booking ends.
func (b Booking) LastDate() string {
	if b.EndsOn != "" {
		return b.EndsOn
	}
	return b.StartsOn
}

// AllDay reports whether the booking has no start time.
func (b Booking) AllDay() bool { return b.StartTime == "" }

// RoomIDs lists the known rooms the booking uses.
func (b Booking) RoomIDs() []int64 {
	var out []int64
	for _, r := range b.Rooms {
		if r.RoomID != 0 {
			out = append(out, r.RoomID)
		}
	}
	return out
}

// RoomNames is the rooms for display, "Lifecenter, Kitchen".
func (b Booking) RoomNames() string {
	names := make([]string, len(b.Rooms))
	for i, r := range b.Rooms {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

func fmtClock(t string) string {
	if c, err := time.Parse("15:04", t); err == nil {
		return c.Format("3:04 PM")
	}
	return t
}

// When is the date and time for display, e.g. "Sat, Nov 1, 2026 · 6:00 PM – 8:00 PM".
func (b Booking) When() string {
	d, err := time.Parse(DateLayout, b.StartsOn)
	if err != nil {
		return b.StartsOn
	}
	out := d.Format("Mon, Jan 2, 2006")
	if b.EndsOn != "" && b.EndsOn != b.StartsOn {
		if end, err := time.Parse(DateLayout, b.EndsOn); err == nil {
			out = strings.TrimSpace(out+" "+fmtClock(b.StartTime)) + " – " + end.Format("Mon, Jan 2, 2006")
			if b.EndTime != "" {
				out += " " + fmtClock(b.EndTime)
			}
			return out
		}
	}
	if b.StartTime != "" {
		out += " · " + fmtClock(b.StartTime)
		if b.EndTime != "" {
			out += " – " + fmtClock(b.EndTime)
		}
	}
	return out
}

// ShortWhen is the time within a day's list: "6:00 PM – 8:00 PM", "All
// day", or the whole When for a booking over several days.
func (b Booking) ShortWhen() string {
	switch {
	case b.EndsOn != "" && b.EndsOn != b.StartsOn:
		return b.When()
	case b.StartTime == "":
		return "All day"
	case b.EndTime == "":
		return "From " + fmtClock(b.StartTime)
	}
	return fmtClock(b.StartTime) + " – " + fmtClock(b.EndTime)
}

const bookingCols = `id, source, COALESCE(source_id, ''), COALESCE(series_id, 0), occurrence_date, title, kind,
	starts_on, start_time, ends_on, end_time, visibility, description, url, status, COALESCE(created_by, 0), created_at, updated_at,
	cancel_note, COALESCE(cancelled_by, 0), signup_label, signup_url, signup_state, signup_closes_at`

func scanBooking(sc interface{ Scan(...any) error }) (Booking, error) {
	var b Booking
	err := sc.Scan(&b.ID, &b.Source, &b.SourceID, &b.SeriesID, &b.OccurrenceDate, &b.Title, &b.Kind,
		&b.StartsOn, &b.StartTime, &b.EndsOn, &b.EndTime, &b.Visibility, &b.Description, &b.URL, &b.Status, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt,
		&b.CancelNote, &b.CancelledBy, &b.Signup.Label, &b.Signup.URL, &b.Signup.State, &b.Signup.ClosesAt)
	return b, err
}

// queryBookings runs a bookings query and loads each one's rooms.
func queryBookings(q querier, query string, args ...any) ([]Booking, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var out []Booking
	idx := map[int64]int{}
	for rows.Next() {
		b, err := scanBooking(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		idx[b.ID] = len(out)
		out = append(out, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]string, 0, len(out))
	for _, b := range out {
		ids = append(ids, itoa(b.ID))
	}
	// The IDs are integers from the database, so joining them is safe.
	rrows, err := q.Query(`SELECT booking_id, COALESCE(room_id, 0), room_name FROM booking_rooms
		WHERE booking_id IN (` + strings.Join(ids, ",") + `) ORDER BY room_name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var id int64
		var br BookingRoom
		if err := rrows.Scan(&id, &br.RoomID, &br.Name); err != nil {
			return nil, err
		}
		out[idx[id]].Rooms = append(out[idx[id]].Rooms, br)
	}
	return out, rrows.Err()
}

func getBooking(q querier, id int64) (*Booking, error) {
	list, err := queryBookings(q, `SELECT `+bookingCols+` FROM bookings WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// GetBooking loads one booking with its rooms.
func (s *Store) GetBooking(id int64) (*Booking, error) { return getBooking(s.DB, id) }

// BookingsBetween lists bookings on any day from..to (inclusive), by start.
// With roomID set, only those in that room.
func (s *Store) BookingsBetween(from, to string, roomID int64) ([]Booking, error) {
	q := `SELECT ` + bookingCols + ` FROM bookings
		WHERE starts_on <= ? AND (CASE ends_on WHEN '' THEN starts_on ELSE ends_on END) >= ?`
	args := []any{to, from}
	if roomID != 0 {
		q += ` AND id IN (SELECT booking_id FROM booking_rooms WHERE room_id = ?)`
		args = append(args, roomID)
	}
	return queryBookings(s.DB, q+` ORDER BY starts_on, start_time <> '', start_time, id`, args...)
}

// setRooms replaces a hand-made booking's rooms with known rooms, by ID.
// Unknown IDs are ignored.
func setRooms(q querier, bookingID int64, roomIDs []int64) error {
	if _, err := q.Exec(`DELETE FROM booking_rooms WHERE booking_id = ?`, bookingID); err != nil {
		return err
	}
	for _, id := range roomIDs {
		if _, err := q.Exec(`INSERT OR IGNORE INTO booking_rooms (booking_id, room_id, room_name)
			SELECT ?, id, name FROM rooms WHERE id = ?`, bookingID, id); err != nil {
			return err
		}
	}
	return nil
}

// CreateBooking adds a hand-made booking (source calendar unless set) in
// the given rooms.
func (s *Store) CreateBooking(b *Booking, roomIDs []int64) (int64, error) {
	if b.Source == "" {
		b.Source = SourceCalendar
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO bookings (source, source_id, title, kind, starts_on, start_time, ends_on, end_time,
		visibility, description, url, status, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.Source, nullStr(b.SourceID), b.Title, b.Kind, b.StartsOn, b.StartTime, b.EndsOn, b.EndTime,
		NormalizeVisibility(b.Visibility), b.Description, b.URL, b.Status, nullInt(b.CreatedBy))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := setRooms(tx, id, roomIDs); err != nil {
		return 0, err
	}
	b.ID = id
	return id, tx.Commit()
}

// UpdateBooking saves a hand-made booking's details and rooms. The source,
// series and who made it don't change.
func (s *Store) UpdateBooking(b *Booking, roomIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE bookings SET title = ?, kind = ?, starts_on = ?, start_time = ?, ends_on = ?, end_time = ?,
		visibility = ?, description = ?, status = ?, updated_at = datetime('now') WHERE id = ?`,
		b.Title, b.Kind, b.StartsOn, b.StartTime, b.EndsOn, b.EndTime, NormalizeVisibility(b.Visibility), b.Description, b.Status, b.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := setRooms(tx, b.ID, roomIDs); err != nil {
		return err
	}
	return tx.Commit()
}
