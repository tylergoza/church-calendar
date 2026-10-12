package store

import (
	"errors"
	"strings"
)

// spansSQL gives each booking that isn't cancelled a start and end that
// compare as text ("2026-11-07 17:30"). An all-day booking runs from 00:00
// on its first day to 00:00 after its last. A timed booking with no end
// time is taken to run to the end of its last day: a room booked "from
// 6 pm" is busy for the evening.
const spansSQL = `spans AS (
	SELECT id,
		starts_on || ' ' || CASE start_time WHEN '' THEN '00:00' ELSE start_time END AS s,
		CASE end_time
			WHEN '' THEN date(CASE ends_on WHEN '' THEN starts_on ELSE ends_on END, '+1 day') || ' 00:00'
			ELSE (CASE ends_on WHEN '' THEN starts_on ELSE ends_on END) || ' ' || end_time
		END AS e
	FROM bookings WHERE status <> 'cancelled')`

// Conflict is two bookings that overlap in the same room. A has the lower
// ID. OK is set when a scheduler marked the pair "that's fine".
type Conflict struct {
	Room BookingRoom
	A, B Booking
	OK   bool
}

// conflicts finds overlapping pairs in known rooms (bookings by name only
// aren't checked), filtered by where, which can use a and b (the spans:
// id, s, e) and ok (conflict_ok). Ordered by room, then start.
func (s *Store) conflicts(where string, args ...any) ([]Conflict, error) {
	rows, err := s.DB.Query(`WITH `+spansSQL+`
		SELECT DISTINCT r.id, r.name, a.id, b.id, ok.low_id IS NOT NULL, min(a.s, b.s) AS first
		FROM booking_rooms ra
		JOIN booking_rooms rb ON rb.room_id = ra.room_id AND rb.booking_id > ra.booking_id
		JOIN rooms r ON r.id = ra.room_id
		JOIN spans a ON a.id = ra.booking_id
		JOIN spans b ON b.id = rb.booking_id
		LEFT JOIN conflict_ok ok ON ok.low_id = a.id AND ok.high_id = b.id
		WHERE a.s < b.e AND b.s < a.e AND (`+where+`)
		ORDER BY r.name COLLATE NOCASE, first, a.id, b.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Conflict
	var pairs [][2]int64
	for rows.Next() {
		var c Conflict
		var pair [2]int64
		var first string
		if err := rows.Scan(&c.Room.RoomID, &c.Room.Name, &pair[0], &pair[1], &c.OK, &first); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
		pairs = append(pairs, pair)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	seen := map[int64]bool{}
	var ids []string
	for _, p := range pairs {
		for _, id := range p {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, itoa(id))
			}
		}
	}
	list, err := queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE id IN (`+strings.Join(ids, ",")+`)`)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Booking, len(list))
	for _, b := range list {
		byID[b.ID] = b
	}
	for i, p := range pairs {
		out[i].A, out[i].B = byID[p[0]], byID[p[1]]
	}
	return out, nil
}

// Conflicts lists clashes that haven't ended before from (YYYY-MM-DD).
// Pairs marked "that's fine" come too when withOK is set.
func (s *Store) Conflicts(from string, withOK bool) ([]Conflict, error) {
	where := `a.e > ?1 AND b.e > ?1`
	if !withOK {
		where += ` AND ok.low_id IS NULL`
	}
	return s.conflicts(where, from+" 00:00")
}

// ConflictsWith lists what clashes with one booking, leaving out pairs
// marked "that's fine". Other dates aren't limited: a clash in the past is
// still a clash for that booking.
func (s *Store) ConflictsWith(bookingID int64) ([]Conflict, error) {
	return s.conflicts(`(a.id = ?1 OR b.id = ?1) AND ok.low_id IS NULL`, bookingID)
}

// SetConflictOK marks (ok) or unmarks a pair of bookings as "that's fine".
func (s *Store) SetConflictOK(a, b int64, ok bool, by int64) error {
	if a == b || a <= 0 || b <= 0 {
		return errors.New("pick two different bookings")
	}
	low, high := min(a, b), max(a, b)
	if !ok {
		_, err := s.DB.Exec(`DELETE FROM conflict_ok WHERE low_id = ? AND high_id = ?`, low, high)
		return err
	}
	_, err := s.DB.Exec(`INSERT OR IGNORE INTO conflict_ok (low_id, high_id, marked_by) VALUES (?, ?, ?)`, low, high, nullInt(by))
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		return ErrNotFound
	}
	return err
}
