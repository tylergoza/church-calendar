package store

import (
	"database/sql"
	"strings"
)

// AppSources are the apps that may push their bookings here, each with
// its own source tokens. A source token only touches its own source's
// bookings.
var AppSources = []string{"events", "productions"}

// IsAppSource reports whether source is an app that pushes.
func IsAppSource(source string) bool {
	for _, s := range AppSources {
		if s == source {
			return true
		}
	}
	return false
}

// PushRoom is a room as an app sends it: the tracker's place ID when it
// has one (0 if not) and its name.
type PushRoom struct {
	PlaceID int64
	Name    string
}

// PushResult is what a push did. Replaced is the hand-made booking it took
// over (0 if none).
type PushResult struct {
	ID       int64
	Created  bool
	Replaced int64
}

// PutSourceBooking creates or replaces an app's booking, keyed by source
// and sourceID, so sending the same thing twice is fine. Rooms are matched
// to the calendar's by place ID, then by name (any case); a match is
// stored under the calendar's name for the room, anything else by the name
// sent, with no room ID (shown, not checked for conflicts). Rooms are
// never created. Locked bookings are stored with no title, kind or
// description, whatever was sent.
//
// replaces is a hand-made booking (source calendar or request) this one
// takes over: it's deleted in the same transaction and its request, if
// any, now points here. Anything else (gone already, another app's
// booking) is ignored, so a retried push still succeeds.
func (s *Store) PutSourceBooking(b Booking, rooms []PushRoom, replaces int64) (PushResult, error) {
	var res PushResult
	b.Visibility = NormalizeVisibility(b.Visibility)
	if b.Visibility == "locked" {
		b.Title, b.Kind, b.Description = "", "", ""
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT id FROM bookings WHERE source = ? AND source_id = ?`, b.Source, b.SourceID).Scan(&res.ID)
	switch {
	case err == sql.ErrNoRows:
		r, err := tx.Exec(`INSERT INTO bookings (source, source_id, title, kind, starts_on, start_time, ends_on, end_time,
			visibility, description, url, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			b.Source, b.SourceID, b.Title, b.Kind, b.StartsOn, b.StartTime, b.EndsOn, b.EndTime,
			b.Visibility, b.Description, b.URL, b.Status)
		if err != nil {
			return res, err
		}
		if res.ID, err = r.LastInsertId(); err != nil {
			return res, err
		}
		res.Created = true
	case err != nil:
		return res, err
	default:
		if _, err := tx.Exec(`UPDATE bookings SET title = ?, kind = ?, starts_on = ?, start_time = ?, ends_on = ?, end_time = ?,
			visibility = ?, description = ?, url = ?, status = ?, updated_at = datetime('now') WHERE id = ?`,
			b.Title, b.Kind, b.StartsOn, b.StartTime, b.EndsOn, b.EndTime, b.Visibility, b.Description, b.URL, b.Status, res.ID); err != nil {
			return res, err
		}
	}
	if err := setPushRooms(tx, res.ID, rooms); err != nil {
		return res, err
	}
	if replaces > 0 && replaces != res.ID {
		var source string
		err := tx.QueryRow(`SELECT source FROM bookings WHERE id = ?`, replaces).Scan(&source)
		if err != nil && err != sql.ErrNoRows {
			return res, err
		}
		if source == SourceCalendar || source == SourceRequest {
			if _, err := tx.Exec(`UPDATE requests SET booking_id = ? WHERE booking_id = ?`, res.ID, replaces); err != nil {
				return res, err
			}
			if err := deleteBooking(tx, replaces); err != nil {
				return res, err
			}
			res.Replaced = replaces
		}
	}
	return res, tx.Commit()
}

// setPushRooms replaces a pushed booking's rooms, matching each to a
// calendar room where it can.
func setPushRooms(tx *sql.Tx, bookingID int64, rooms []PushRoom) error {
	if _, err := tx.Exec(`DELETE FROM booking_rooms WHERE booking_id = ?`, bookingID); err != nil {
		return err
	}
	for _, r := range rooms {
		var id int64
		name := strings.TrimSpace(r.Name)
		err := sql.ErrNoRows
		if r.PlaceID > 0 {
			err = tx.QueryRow(`SELECT id, name FROM rooms WHERE tracker_place_id = ?`, r.PlaceID).Scan(&id, &name)
		}
		if err == sql.ErrNoRows && name != "" {
			err = tx.QueryRow(`SELECT id, name FROM rooms WHERE name = ?`, name).Scan(&id, &name)
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if name == "" {
			continue
		}
		// A second name for a room already listed is dropped.
		if _, err := tx.Exec(`INSERT INTO booking_rooms (booking_id, room_id, room_name)
			SELECT ?1, ?2, ?3 WHERE NOT EXISTS (SELECT 1 FROM booking_rooms WHERE booking_id = ?1
				AND (room_name = ?3 COLLATE NOCASE OR (?2 IS NOT NULL AND room_id = ?2)))`,
			bookingID, nullInt(id), name); err != nil {
			return err
		}
	}
	return nil
}

// DeleteSourceBooking removes an app's booking. Already gone is fine.
func (s *Store) DeleteSourceBooking(source, sourceID string) error {
	_, err := s.DB.Exec(`DELETE FROM bookings WHERE source = ? AND source_id = ?`, source, sourceID)
	return err
}

// SourceBookings lists an app's bookings (cancelled ones too) changed at or
// after since ("YYYY-MM-DD HH:MM:SS" in UTC, "" for all), oldest change
// first.
func (s *Store) SourceBookings(source, since string) ([]Booking, error) {
	return queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE source = ? AND updated_at >= ?
		ORDER BY updated_at, id`, source, since)
}

// BusyInRoomsNamed lists the bookings (not cancelled) on any day from..to
// in rooms with these names (any case): the calendar's rooms, and places
// apps sent by a name the calendar doesn't know.
func (s *Store) BusyInRoomsNamed(names []string, from, to string) ([]Booking, error) {
	if len(names) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	args := []any{to, from}
	for _, n := range names {
		args = append(args, n)
	}
	for _, n := range names {
		args = append(args, n)
	}
	// A room's building and the rooms inside it count too.
	return queryBookings(s.DB, `WITH RECURSIVE `+familySQL+`
		SELECT `+bookingCols+` FROM bookings
		WHERE status <> 'cancelled' AND starts_on <= ? AND (CASE ends_on WHEN '' THEN starts_on ELSE ends_on END) >= ?
		AND id IN (SELECT booking_id FROM booking_rooms
			WHERE room_name COLLATE NOCASE IN (`+marks+`)
			   OR room_id IN (SELECT f.related_id FROM room_family f JOIN rooms r ON r.id = f.room_id WHERE r.name IN (`+marks+`)))
		ORDER BY starts_on, start_time <> '', start_time, id`, args...)
}

// SourceStat is one app's part of the calendar, for the Connected apps
// page.
type SourceStat struct {
	Source     string
	Bookings   int    // all of them, past ones too
	Upcoming   int    // ending today or later, not cancelled
	LastChange string // when one of its bookings last changed; "" if none
	Tokens     int
	LastUsed   string // when one of its tokens was last used; "" if never
}

// SourceStats gives each app in AppSources (and any other source that has
// bookings or tokens) its counts.
func (s *Store) SourceStats(today string) ([]SourceStat, error) {
	byName := map[string]*SourceStat{}
	var out []*SourceStat
	get := func(src string) *SourceStat {
		if st, ok := byName[src]; ok {
			return st
		}
		st := &SourceStat{Source: src}
		byName[src] = st
		out = append(out, st)
		return st
	}
	for _, src := range AppSources {
		get(src)
	}
	rows, err := s.DB.Query(`SELECT source, count(*),
		sum(status <> 'cancelled' AND (CASE ends_on WHEN '' THEN starts_on ELSE ends_on END) >= ?), max(updated_at)
		FROM bookings WHERE source NOT IN (?, ?) GROUP BY source ORDER BY source`, today, SourceCalendar, SourceRequest)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var src string
		var n, up int
		var last string
		if err := rows.Scan(&src, &n, &up, &last); err != nil {
			rows.Close()
			return nil, err
		}
		st := get(src)
		st.Bookings, st.Upcoming, st.LastChange = n, up, last
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(`SELECT source, count(*), COALESCE(max(last_used), '') FROM tokens
		WHERE kind = ? GROUP BY source ORDER BY source`, TokenSource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var src, last string
		var n int
		if err := rows.Scan(&src, &n, &last); err != nil {
			return nil, err
		}
		st := get(src)
		st.Tokens, st.LastUsed = n, last
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	list := make([]SourceStat, len(out))
	for i, st := range out {
		list[i] = *st
	}
	return list, nil
}
