package store

import (
	"errors"
	"strings"
	"time"
)

// ErrAlreadyDecided is returned when a request has already been approved,
// declined or withdrawn.
var ErrAlreadyDecided = errors.New("that request has already been answered")

// Request statuses.
var RequestStatuses = []string{"waiting", "approved", "declined", "withdrawn"}

var requestStatusLabels = map[string]string{
	"waiting": "Waiting", "approved": "Approved", "declined": "Declined", "withdrawn": "Withdrawn",
}

func RequestStatusLabel(s string) string { return requestStatusLabels[s] }

// Request is someone asking for a room. Approving it makes a booking with
// source "request". Code is the random part of the status link the person
// is given, so they can check back without an account.
type Request struct {
	ID            int64
	Code          string
	Name          string
	Contact       string // email or phone
	UserID        int64  // the member who asked; 0 for a visitor
	Title         string
	StartsOn      string
	StartTime     string
	EndsOn        string
	EndTime       string
	ExpectedCount int // 0 when not given
	Details       string
	Status        string
	Answer        string // note back to the person
	BookingID     int64  // the booking approving it made; 0 if none (or deleted)
	DecidedBy     int64  // 0 for one booked straight away
	CreatedAt     string
	Rooms         []BookingRoom
}

// span is the request's times as a booking, for display and clash checks.
func (r Request) span() Booking {
	return Booking{StartsOn: r.StartsOn, StartTime: r.StartTime, EndsOn: r.EndsOn, EndTime: r.EndTime, Rooms: r.Rooms}
}

func (r Request) When() string      { return r.span().When() }
func (r Request) RoomNames() string { return r.span().RoomNames() }
func (r Request) RoomIDs() []int64  { return r.span().RoomIDs() }
func (r Request) Waiting() bool     { return r.Status == "waiting" }

const requestCols = `id, code, name, contact, COALESCE(user_id, 0), title, starts_on, start_time, ends_on, end_time,
	COALESCE(expected_count, 0), details, status, answer, COALESCE(booking_id, 0), COALESCE(decided_by, 0), created_at`

func (s *Store) queryRequests(query string, args ...any) ([]Request, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var out []Request
	idx := map[int64]int{}
	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Contact, &r.UserID, &r.Title, &r.StartsOn, &r.StartTime,
			&r.EndsOn, &r.EndTime, &r.ExpectedCount, &r.Details, &r.Status, &r.Answer, &r.BookingID, &r.DecidedBy, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		idx[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]string, 0, len(out))
	for _, r := range out {
		ids = append(ids, itoa(r.ID))
	}
	// The IDs are integers from the database, so joining them is safe.
	rrows, err := s.DB.Query(`SELECT rr.request_id, rm.id, rm.name FROM request_rooms rr JOIN rooms rm ON rm.id = rr.room_id
		WHERE rr.request_id IN (` + strings.Join(ids, ",") + `) ORDER BY rm.name COLLATE NOCASE`)
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

// GetRequest loads one request with its rooms.
func (s *Store) GetRequest(id int64) (*Request, error) {
	list, err := s.queryRequests(`SELECT `+requestCols+` FROM requests WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// ListRequests lists requests with one status. Waiting ones come soonest
// first; answered ones newest first.
func (s *Store) ListRequests(status string) ([]Request, error) {
	order := `starts_on, start_time, id`
	if status != "waiting" {
		order = `id DESC`
	}
	return s.queryRequests(`SELECT `+requestCols+` FROM requests WHERE status = ? ORDER BY `+order+` LIMIT 200`, status)
}

// RequestsBy lists a member's own requests, newest first.
func (s *Store) RequestsBy(userID int64) ([]Request, error) {
	return s.queryRequests(`SELECT `+requestCols+` FROM requests WHERE user_id = ? ORDER BY id DESC LIMIT 50`, userID)
}

// CountWaitingRequests is how many requests wait for a scheduler.
func (s *Store) CountWaitingRequests() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT count(*) FROM requests WHERE status = 'waiting'`).Scan(&n)
	return n, err
}

// RequestForBooking finds the request a booking was made from (0 if none).
func (s *Store) RequestForBooking(bookingID int64) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM requests WHERE booking_id = ?`, bookingID).Scan(&id)
	if errors.Is(notFound(err), ErrNotFound) {
		return 0, nil
	}
	return id, err
}

// CreateRequest saves a new waiting request in the given rooms and fills
// in its ID and code.
func (s *Store) CreateRequest(r *Request, roomIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r.Code, r.Status = RandomToken(), "waiting"
	res, err := tx.Exec(`INSERT INTO requests (code, name, contact, user_id, title, starts_on, start_time, ends_on, end_time,
		expected_count, details) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Code, r.Name, r.Contact, nullInt(r.UserID), r.Title, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime,
		nullInt(int64(r.ExpectedCount)), r.Details)
	if err != nil {
		return err
	}
	if r.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	for _, id := range roomIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO request_rooms (request_id, room_id) VALUES (?, ?)`, r.ID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ApproveRequest books a waiting request: a booking with source "request"
// in its rooms, with the given visibility. by is the scheduler (0 when it
// was booked straight away, and the booking is then the asker's). Refused
// with ErrAlreadyDecided unless the request is still waiting, so
// approving twice makes one booking.
func (s *Store) ApproveRequest(id, by int64, answer, visibility string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var title, startsOn, startTime, endsOn, endTime string
	var userID int64
	err = tx.QueryRow(`SELECT title, starts_on, start_time, ends_on, end_time, COALESCE(user_id, 0) FROM requests
		WHERE id = ?`, id).Scan(&title, &startsOn, &startTime, &endsOn, &endTime, &userID)
	if err != nil {
		return 0, notFound(err)
	}
	res, err := tx.Exec(`UPDATE requests SET status = 'approved', answer = ?, decided_by = ? WHERE id = ? AND status = 'waiting'`,
		answer, nullInt(by), id)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrAlreadyDecided
	}
	createdBy := by
	if createdBy == 0 {
		createdBy = userID
	}
	vis := "private"
	if visibility == "public" {
		vis = "public"
	}
	res, err = tx.Exec(`INSERT INTO bookings (source, title, starts_on, start_time, ends_on, end_time, visibility, status, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'confirmed', ?)`, SourceRequest, title, startsOn, startTime, endsOn, endTime, vis, nullInt(createdBy))
	if err != nil {
		return 0, err
	}
	bookingID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO booking_rooms (booking_id, room_id, room_name)
		SELECT ?, rm.id, rm.name FROM request_rooms rr JOIN rooms rm ON rm.id = rr.room_id WHERE rr.request_id = ?`, bookingID, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE requests SET booking_id = ? WHERE id = ?`, bookingID, id); err != nil {
		return 0, err
	}
	return bookingID, tx.Commit()
}

// DeclineRequest turns down a waiting request with a note to the person.
func (s *Store) DeclineRequest(id, by int64, answer string) error {
	return s.closeRequest(`UPDATE requests SET status = 'declined', answer = ?, decided_by = ? WHERE id = ? AND status = 'waiting'`,
		id, answer, nullInt(by), id)
}

// WithdrawRequest is the person taking back a request that still waits.
func (s *Store) WithdrawRequest(id int64) error {
	return s.closeRequest(`UPDATE requests SET status = 'withdrawn' WHERE id = ? AND status = 'waiting'`, id, id)
}

func (s *Store) closeRequest(query string, id int64, args ...any) error {
	res, err := s.DB.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := s.GetRequest(id); err != nil {
		return err
	}
	return ErrAlreadyDecided
}

// spanOf is a time block's start and end as text that compares, the same
// way spansSQL works them out for bookings.
func spanOf(startsOn, startTime, endsOn, endTime string) (start, end string) {
	last := startsOn
	if endsOn != "" {
		last = endsOn
	}
	start = startsOn + " 00:00"
	if startTime != "" {
		start = startsOn + " " + startTime
	}
	if endTime != "" {
		return start, last + " " + endTime
	}
	if d, err := time.Parse(DateLayout, last); err == nil {
		last = d.AddDate(0, 0, 1).Format(DateLayout)
	}
	return start, last + " 00:00"
}

// BusyInRooms lists the bookings (not cancelled) that overlap the given
// times in each of the rooms, keyed by room ID. Rooms that are free have
// no key.
func (s *Store) BusyInRooms(roomIDs []int64, startsOn, startTime, endsOn, endTime string) (map[int64][]Booking, error) {
	out := map[int64][]Booking{}
	if len(roomIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(roomIDs))
	for i, id := range roomIDs {
		ids[i] = itoa(id)
	}
	start, end := spanOf(startsOn, startTime, endsOn, endTime)
	rows, err := s.DB.Query(`WITH `+spansSQL+`
		SELECT br.room_id, sp.id FROM spans sp JOIN booking_rooms br ON br.booking_id = sp.id
		WHERE br.room_id IN (`+strings.Join(ids, ",")+`) AND sp.s < ? AND ? < sp.e`, end, start)
	if err != nil {
		return nil, err
	}
	byRoom := map[int64][]int64{}
	var all []string
	seen := map[int64]bool{}
	for rows.Next() {
		var room, id int64
		if err := rows.Scan(&room, &id); err != nil {
			rows.Close()
			return nil, err
		}
		byRoom[room] = append(byRoom[room], id)
		if !seen[id] {
			seen[id] = true
			all = append(all, itoa(id))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(all) == 0 {
		return out, err
	}
	list, err := queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE id IN (`+strings.Join(all, ",")+`)
		ORDER BY starts_on, start_time <> '', start_time, id`)
	if err != nil {
		return nil, err
	}
	for room, want := range byRoom {
		for _, b := range list {
			for _, id := range want {
				if b.ID == id {
					out[room] = append(out[room], b)
				}
			}
		}
	}
	return out, nil
}
