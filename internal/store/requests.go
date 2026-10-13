package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// ErrAlreadyDecided is returned when a request has already been approved,
// declined, withdrawn or cancelled.
var ErrAlreadyDecided = errors.New("that request has already been answered")

// ErrRoomsBusy is returned when a member's booking would clash: nothing is
// saved. The busy bookings come back with it.
var ErrRoomsBusy = errors.New("some of those rooms are taken then")

// Request statuses.
var RequestStatuses = []string{"waiting", "approved", "declined", "withdrawn", "cancelled"}

var requestStatusLabels = map[string]string{
	"waiting": "Waiting", "approved": "Approved", "declined": "Declined", "withdrawn": "Withdrawn", "cancelled": "Cancelled",
}

func RequestStatusLabel(s string) string { return requestStatusLabels[s] }

// Request is someone asking for a room. A visitor's request has a pending
// booking (source "request") from the start, which approving confirms. A
// member's is booked (confirmed) straight away. Code is the random part of
// the status link the person is given, so they can check back without an
// account.
type Request struct {
	ID            int64
	Code          string
	Name          string
	Contact       string // email, optional
	Phone         string // required from visitors; never leaves the calendar
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
	BookingID     int64  // its booking (pending until approved); 0 if none (or deleted)
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

const requestCols = `id, code, name, contact, phone, COALESCE(user_id, 0), title, starts_on, start_time, ends_on, end_time,
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
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Contact, &r.Phone, &r.UserID, &r.Title, &r.StartsOn, &r.StartTime,
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

// insertRequest saves a new request with the given status and its rooms,
// and fills in its ID, code and status.
func insertRequest(tx *sql.Tx, r *Request, status string, roomIDs []int64) error {
	r.Code, r.Status = RandomToken(), status
	res, err := tx.Exec(`INSERT INTO requests (code, name, contact, phone, user_id, title, starts_on, start_time, ends_on, end_time,
		expected_count, details, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Code, r.Name, r.Contact, r.Phone, nullInt(r.UserID), r.Title, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime,
		nullInt(int64(r.ExpectedCount)), r.Details, status)
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
	return nil
}

// insertRequestBooking makes the booking (source "request", private) for
// request r in its rooms and points the request at it.
func insertRequestBooking(tx *sql.Tx, r *Request, status string, createdBy int64) error {
	res, err := tx.Exec(`INSERT INTO bookings (source, title, starts_on, start_time, ends_on, end_time, visibility, status, created_by)
		VALUES (?, ?, ?, ?, ?, ?, 'private', ?, ?)`, SourceRequest, r.Title, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime, status, nullInt(createdBy))
	if err != nil {
		return err
	}
	if r.BookingID, err = res.LastInsertId(); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO booking_rooms (booking_id, room_id, room_name)
		SELECT ?, rm.id, rm.name FROM request_rooms rr JOIN rooms rm ON rm.id = rr.room_id WHERE rr.request_id = ?`, r.BookingID, r.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE requests SET booking_id = ? WHERE id = ?`, r.BookingID, r.ID)
	return err
}

// CreateRequest saves a visitor's request in the given rooms, waiting for
// a scheduler, with its pending booking so the rooms are blocked from now
// on. It fills in the request's ID, code and booking.
func (s *Store) CreateRequest(r *Request, roomIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertRequest(tx, r, "waiting", roomIDs); err != nil {
		return err
	}
	if err := insertRequestBooking(tx, r, "pending", r.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

// BookRequest books a signed-in member's request straight away: the
// request is approved (by nobody) and its booking confirmed, made by the
// member. If any room is busy then (pending and pencilled-in bookings
// count) nothing is saved and the clashes come back with ErrRoomsBusy.
// The check runs inside the transaction, so two members can't take the
// same slot at once.
func (s *Store) BookRequest(r *Request, roomIDs []int64) (map[int64][]Booking, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	busy, err := busyInRooms(tx, roomIDs, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime, 0)
	if err != nil {
		return nil, err
	}
	if len(busy) > 0 {
		return busy, ErrRoomsBusy
	}
	if err := insertRequest(tx, r, "approved", roomIDs); err != nil {
		return nil, err
	}
	if err := insertRequestBooking(tx, r, "confirmed", r.UserID); err != nil {
		return nil, err
	}
	return nil, tx.Commit()
}

// ApproveRequest confirms a waiting request's booking, with the given
// visibility. by is the scheduler. A request with no booking (it was
// deleted) gets a new one. Refused with ErrAlreadyDecided unless the
// request is still waiting, so approving twice changes nothing.
func (s *Store) ApproveRequest(id, by int64, answer, visibility string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var r Request
	err = tx.QueryRow(`SELECT id, title, starts_on, start_time, ends_on, end_time, COALESCE(user_id, 0), COALESCE(booking_id, 0)
		FROM requests WHERE id = ?`, id).Scan(&r.ID, &r.Title, &r.StartsOn, &r.StartTime, &r.EndsOn, &r.EndTime, &r.UserID, &r.BookingID)
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
	vis := "private"
	if visibility == "public" {
		vis = "public"
	}
	createdBy := r.UserID
	if createdBy == 0 {
		createdBy = by
	}
	if r.BookingID == 0 {
		if err := insertRequestBooking(tx, &r, "confirmed", createdBy); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`UPDATE bookings SET status = 'confirmed', visibility = ?, created_by = COALESCE(created_by, ?),
		updated_at = datetime('now') WHERE id = ?`, vis, nullInt(createdBy), r.BookingID); err != nil {
		return 0, err
	}
	return r.BookingID, tx.Commit()
}

// DeclineRequest turns down a waiting request with a note to the person,
// and frees its rooms.
func (s *Store) DeclineRequest(id, by int64, answer string) error {
	return s.closeRequest(id, `UPDATE requests SET status = 'declined', answer = ?, decided_by = ? WHERE id = ? AND status = 'waiting'`,
		answer, nullInt(by), id)
}

// WithdrawRequest is the person taking back a request that still waits,
// which frees its rooms.
func (s *Store) WithdrawRequest(id int64) error {
	return s.closeRequest(id, `UPDATE requests SET status = 'withdrawn' WHERE id = ? AND status = 'waiting'`, id)
}

// closeRequest runs query (which must only match a waiting request) and
// cancels the request's booking with it.
func (s *Store) closeRequest(id int64, query string, args ...any) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// One connection: ask inside the transaction, not s.DB.
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM requests WHERE id = ?`, id).Scan(&one); err != nil {
			return notFound(err)
		}
		return ErrAlreadyDecided
	}
	if _, err := tx.Exec(`UPDATE bookings SET status = 'cancelled', updated_at = datetime('now')
		WHERE id = (SELECT booking_id FROM requests WHERE id = ?) AND source = ?`, id, SourceRequest); err != nil {
		return err
	}
	return tx.Commit()
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
	return busyInRooms(s.DB, roomIDs, startsOn, startTime, endsOn, endTime, 0)
}

// BusyInRoomsExcept is BusyInRooms leaving out one booking, for moving a
// booking without it clashing with itself.
func (s *Store) BusyInRoomsExcept(except int64, roomIDs []int64, startsOn, startTime, endsOn, endTime string) (map[int64][]Booking, error) {
	return busyInRooms(s.DB, roomIDs, startsOn, startTime, endsOn, endTime, except)
}

func busyInRooms(q querier, roomIDs []int64, startsOn, startTime, endsOn, endTime string, except int64) (map[int64][]Booking, error) {
	out := map[int64][]Booking{}
	if len(roomIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(roomIDs))
	for i, id := range roomIDs {
		ids[i] = itoa(id)
	}
	start, end := spanOf(startsOn, startTime, endsOn, endTime)
	// A booking in the building, or in a room inside it, makes the room busy.
	rows, err := q.Query(`WITH RECURSIVE `+familySQL+`, `+spansSQL+`
		SELECT DISTINCT f.room_id, sp.id FROM spans sp JOIN booking_rooms br ON br.booking_id = sp.id
		JOIN room_family f ON f.related_id = br.room_id
		WHERE f.room_id IN (`+strings.Join(ids, ",")+`) AND sp.s < ? AND ? < sp.e AND sp.id <> ?`, end, start, except)
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
	list, err := queryBookings(q, `SELECT `+bookingCols+` FROM bookings WHERE id IN (`+strings.Join(all, ",")+`)
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
