package store

import (
	"database/sql"
	"strings"
)

// TrackerPlace is a place in the maintenance tracker, as the import needs
// it. Label is what the room is called here ("104 – Nursery"); Name is the
// tracker's bare name ("Nursery"), also used to link bookings sent by name.
type TrackerPlace struct {
	ID    int64
	Label string
	Name  string
	Path  string
}

// ImportResult is what an import did, for the message afterwards.
type ImportResult struct {
	Added    []string // new rooms
	Linked   []string // rooms typed in by hand that now follow a place
	Renamed  []string // imported rooms whose name or path changed
	Archived []string // imported rooms whose place is gone from the tracker
	Skipped  []string // why a place couldn't be added or renamed
	Bookings int      // booking rooms stored by name, now linked to a room
}

// ImportRooms brings places from the tracker in as rooms. places is every
// place the tracker has now; pick is the ones to add. In one transaction:
//
//   - every room already imported follows its place's name and path, and
//     is archived if its place is gone (the tracker deletes, it doesn't
//     archive);
//   - each picked place becomes a room, or takes over a room typed in by
//     hand with the same name, so running it again never duplicates;
//   - booking rooms stored by name only (pushed before the room was here)
//     that match a room's name, or a place's bare name, get linked.
//
// A name another room already has is skipped with a note, never merged.
func (s *Store) ImportRooms(places []TrackerPlace, pick []int64) (ImportResult, error) {
	var res ImportResult
	tx, err := s.DB.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	byID := map[int64]TrackerPlace{}
	for _, p := range places {
		p.Label, p.Name = strings.TrimSpace(p.Label), strings.TrimSpace(p.Name)
		if p.Label == "" {
			p.Label = p.Name
		}
		byID[p.ID] = p
	}
	nameTaken := func(name string, except int64) (bool, error) {
		var n int
		err := tx.QueryRow(`SELECT COUNT(*) FROM rooms WHERE name = ? AND id != ?`, name, except).Scan(&n)
		return n > 0, err
	}

	// Rooms already imported follow the tracker.
	rows, err := tx.Query(`SELECT id, name, tracker_place_id, tracker_path, archived FROM rooms WHERE tracker_place_id IS NOT NULL`)
	if err != nil {
		return res, err
	}
	type linked struct {
		id, place  int64
		name, path string
		archived   bool
	}
	var have []linked
	for rows.Next() {
		var l linked
		if err := rows.Scan(&l.id, &l.name, &l.place, &l.path, &l.archived); err != nil {
			rows.Close()
			return res, err
		}
		have = append(have, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	imported := map[int64]bool{}
	for _, l := range have {
		imported[l.place] = true
		p, ok := byID[l.place]
		if !ok {
			if !l.archived {
				if _, err := tx.Exec(`UPDATE rooms SET archived = 1 WHERE id = ?`, l.id); err != nil {
					return res, err
				}
				res.Archived = append(res.Archived, l.name)
			}
			continue
		}
		name := l.name
		if p.Label != l.name {
			taken, err := nameTaken(p.Label, l.id)
			if err != nil {
				return res, err
			}
			if taken {
				res.Skipped = append(res.Skipped, l.name+" is now "+p.Label+" in the tracker, but another room here has that name, so it kept its name.")
			} else {
				name = p.Label
			}
		}
		if name == l.name && p.Path == l.path {
			continue
		}
		if _, err := tx.Exec(`UPDATE rooms SET name = ?, tracker_path = ? WHERE id = ?`, name, p.Path, l.id); err != nil {
			return res, err
		}
		if _, err := tx.Exec(`UPDATE booking_rooms SET room_name = ? WHERE room_id = ?`, name, l.id); err != nil {
			return res, err
		}
		res.Renamed = append(res.Renamed, name)
	}

	// Picked places become rooms.
	for _, id := range pick {
		p, ok := byID[id]
		if !ok || imported[id] || p.Label == "" {
			continue
		}
		imported[id] = true
		var roomID, placeID sql.NullInt64
		err := tx.QueryRow(`SELECT id, tracker_place_id FROM rooms WHERE name = ?`, p.Label).Scan(&roomID, &placeID)
		switch {
		case err == sql.ErrNoRows:
			if _, err := tx.Exec(`INSERT INTO rooms (name, tracker_place_id, tracker_path) VALUES (?, ?, ?)`, p.Label, p.ID, p.Path); err != nil {
				return res, err
			}
			res.Added = append(res.Added, p.Label)
		case err != nil:
			return res, err
		case placeID.Valid:
			res.Skipped = append(res.Skipped, p.Label+" wasn't added: another place in the tracker already has that name here.")
		default:
			if _, err := tx.Exec(`UPDATE rooms SET tracker_place_id = ?, tracker_path = ? WHERE id = ?`, p.ID, p.Path, roomID.Int64); err != nil {
				return res, err
			}
			res.Linked = append(res.Linked, p.Label)
		}
	}

	// Booking rooms stored by name that a room now answers to.
	if res.Bookings, err = linkRoomNames(tx, byID); err != nil {
		return res, err
	}
	return res, tx.Commit()
}

// linkRoomNames links booking rooms with no room ID whose name is a room's
// name (any case) or, for an imported room, its place's bare name in
// places. A linked row takes the room's name. A booking already listing
// that room just loses the extra row.
func linkRoomNames(tx *sql.Tx, places map[int64]TrackerPlace) (int, error) {
	rows, err := tx.Query(`SELECT id, name, COALESCE(tracker_place_id, 0) FROM rooms`)
	if err != nil {
		return 0, err
	}
	type room struct {
		id    int64
		name  string
		alias string
	}
	var rooms []room
	for rows.Next() {
		var r room
		var place int64
		if err := rows.Scan(&r.id, &r.name, &place); err != nil {
			rows.Close()
			return 0, err
		}
		r.alias = r.name
		if p, ok := places[place]; ok && place != 0 && p.Name != "" {
			r.alias = p.Name
		}
		rooms = append(rooms, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var total int
	for _, r := range rooms {
		match := `room_id IS NULL AND (room_name = ?2 COLLATE NOCASE OR room_name = ?3 COLLATE NOCASE)`
		if _, err := tx.Exec(`DELETE FROM booking_rooms WHERE `+match+`
			AND EXISTS (SELECT 1 FROM booking_rooms b2 WHERE b2.booking_id = booking_rooms.booking_id AND b2.room_id = ?1)`,
			r.id, r.name, r.alias); err != nil {
			return 0, err
		}
		// Two unlinked names for the same room on one booking: keep one.
		if _, err := tx.Exec(`DELETE FROM booking_rooms WHERE `+match+`
			AND rowid NOT IN (SELECT MIN(rowid) FROM booking_rooms WHERE `+match+` GROUP BY booking_id)`,
			r.id, r.name, r.alias); err != nil {
			return 0, err
		}
		res, err := tx.Exec(`UPDATE booking_rooms SET room_id = ?1, room_name = ?2 WHERE `+match, r.id, r.name, r.alias)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}
