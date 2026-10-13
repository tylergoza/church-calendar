package store

import (
	"errors"
	"strings"
)

// ErrRoomNameTaken is returned when another room already has the name.
var ErrRoomNameTaken = errors.New("another room already has that name")

// Room is a room or space that can be booked.
type Room struct {
	ID          int64
	Name        string
	PlaceID     int64  // the tracker's place ID; 0 when typed in by hand
	TrackerPath string // "Main building › Lifecenter", cached from the tracker
	Capacity    int    // 0 when not known
	Bookable    bool   // offered on the request form
	Kind        string // the tracker's kind: building, room or area; "" for hand-made
	ParentID    int64  // the room it's inside (a building), 0 if none
	Notes       string
	Archived    bool
}

// FromTracker reports whether the room was imported from the tracker.
func (r Room) FromTracker() bool { return r.PlaceID != 0 }

const roomCols = `id, name, COALESCE(tracker_place_id, 0), tracker_path, COALESCE(capacity, 0), bookable, kind, COALESCE(parent_id, 0), notes, archived`

func scanRoom(sc interface{ Scan(...any) error }) (Room, error) {
	var r Room
	err := sc.Scan(&r.ID, &r.Name, &r.PlaceID, &r.TrackerPath, &r.Capacity, &r.Bookable, &r.Kind, &r.ParentID, &r.Notes, &r.Archived)
	return r, err
}

// ListRooms lists rooms by name, archived ones only when asked.
func (s *Store) ListRooms(withArchived bool) ([]Room, error) {
	q := `SELECT ` + roomCols + ` FROM rooms`
	if !withArchived {
		q += ` WHERE archived = 0`
	}
	rows, err := s.DB.Query(q + ` ORDER BY archived, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Room
	for rows.Next() {
		r, err := scanRoom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRoom loads one room.
func (s *Store) GetRoom(id int64) (*Room, error) {
	r, err := scanRoom(s.DB.QueryRow(`SELECT `+roomCols+` FROM rooms WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &r, nil
}

func roomNameErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: rooms.name") {
		return ErrRoomNameTaken
	}
	return err
}

// CreateRoom adds a room typed in by hand. Bookings sent with its name
// before it was here are linked to it.
func (s *Store) CreateRoom(r *Room) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO rooms (name, capacity, bookable, parent_id, notes) VALUES (?, ?, ?, ?, ?)`,
		strings.TrimSpace(r.Name), nullInt(int64(r.Capacity)), r.Bookable, nullInt(r.ParentID), r.Notes)
	if err != nil {
		return 0, roomNameErr(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := linkRoomNames(tx, nil); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateRoom saves a room's details and keeps the names stored on its
// bookings in step, so they read the same everywhere. Bookings sent with
// its new name are linked to it.
func (s *Store) UpdateRoom(r *Room) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	name := strings.TrimSpace(r.Name)
	res, err := tx.Exec(`UPDATE rooms SET name = ?, capacity = ?, bookable = ?, parent_id = ?, notes = ?, archived = ? WHERE id = ?`,
		name, nullInt(int64(r.Capacity)), r.Bookable, nullInt(r.ParentID), r.Notes, r.Archived, r.ID)
	if err != nil {
		return roomNameErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`UPDATE booking_rooms SET room_name = ? WHERE room_id = ?`, name, r.ID); err != nil {
		return err
	}
	if _, err := linkRoomNames(tx, nil); err != nil {
		return err
	}
	return tx.Commit()
}
