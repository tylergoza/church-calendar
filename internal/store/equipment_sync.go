package store

import (
	"database/sql"
	"slices"
	"strings"
)

// TrackerProduct is a product in the maintenance tracker, as the equipment
// sync needs it. Booking is "count", "service" or "" (not offered);
// OfferedIn is the tracker place IDs it's offered with.
type TrackerProduct struct {
	ID        int64
	Name      string
	Booking   string
	Total     int
	Notes     string
	OfferedIn []int64
}

// EquipmentSyncResult is what a sync did, for the log and the message.
type EquipmentSyncResult struct {
	Added    []string // new equipment
	Linked   []string // equipment typed in by hand that now follows a product
	Updated  []string // tracker equipment whose name, mode, total, notes or rooms changed
	Archived []string // tracker equipment whose product is no longer offered
	Skipped  []string // why a product couldn't be added or renamed
}

// Changed reports whether the sync did anything.
func (r EquipmentSyncResult) Changed() bool {
	return len(r.Added)+len(r.Linked)+len(r.Updated)+len(r.Archived) > 0
}

// SyncEquipment brings equipment in step with the tracker. products is
// every product the tracker offers with room bookings now. In one
// transaction:
//
//   - each offered product (a mode of count or service and at least one
//     place it's offered in) becomes equipment, or updates the one that
//     follows it: name, mode, total, notes and rooms, with the name on
//     bookings kept in step. Offered places that aren't calendar rooms
//     (rooms.tracker_place_id) are skipped;
//   - equipment made by hand with the same name as a new product is linked
//     to it, keeping its bookings, as ImportRooms does for rooms. A name
//     that belongs to equipment following another product is skipped with
//     a note, never merged;
//   - equipment that follows a product no longer in the list is archived
//     (bookings keep it), and un-archived if the product comes back.
//
// Hand-made equipment is otherwise never touched. The caller passes the
// list only after the tracker answered; a failed call must not get here.
func (s *Store) SyncEquipment(products []TrackerProduct) (EquipmentSyncResult, error) {
	var res EquipmentSyncResult
	tx, err := s.DB.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	roomOf := map[int64]int64{} // tracker place ID -> room ID
	rows, err := tx.Query(`SELECT tracker_place_id, id FROM rooms WHERE tracker_place_id IS NOT NULL`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var place, room int64
		if err := rows.Scan(&place, &room); err != nil {
			rows.Close()
			return res, err
		}
		roomOf[place] = room
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	type have struct {
		id         int64
		name, mode string
		total      int
		notes      string
		archived   bool
		rooms      []int64
	}
	// load reads the equipment for a product, or a hand-made one by name.
	load := func(where string, arg any) (*have, error) {
		var h have
		err := tx.QueryRow(`SELECT id, name, mode, total, notes, archived FROM equipment WHERE `+where, arg).
			Scan(&h.id, &h.name, &h.mode, &h.total, &h.notes, &h.archived)
		if err == sql.ErrNoRows {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		rr, err := tx.Query(`SELECT room_id FROM equipment_rooms WHERE equipment_id = ?`, h.id)
		if err != nil {
			return nil, err
		}
		defer rr.Close()
		for rr.Next() {
			var room int64
			if err := rr.Scan(&room); err != nil {
				return nil, err
			}
			h.rooms = append(h.rooms, room)
		}
		slices.Sort(h.rooms)
		return &h, rr.Err()
	}

	offered := map[int64]bool{}
	for _, p := range products {
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" || len(p.OfferedIn) == 0 || (p.Booking != ModeCount && p.Booking != ModeService) {
			continue
		}
		offered[p.ID] = true
		if p.Booking == ModeService {
			p.Total = 0
		}
		p.Total = max(p.Total, 0)
		var rooms []int64
		for _, place := range p.OfferedIn {
			if room, ok := roomOf[place]; ok && !slices.Contains(rooms, room) {
				rooms = append(rooms, room)
			}
		}
		slices.Sort(rooms)

		linked := false
		cur, err := load(`tracker_product_id = ?`, p.ID)
		if err != nil {
			return res, err
		}
		if cur == nil {
			byName, err := load(`name = ?`, p.Name)
			if err != nil {
				return res, err
			}
			switch {
			case byName == nil:
				r, err := tx.Exec(`INSERT INTO equipment (name, tracker_product_id, mode, total, notes) VALUES (?, ?, ?, ?, ?)`,
					p.Name, p.ID, p.Booking, p.Total, p.Notes)
				if err != nil {
					return res, err
				}
				id, _ := r.LastInsertId()
				if err := setEquipmentRooms(tx, id, rooms); err != nil {
					return res, err
				}
				res.Added = append(res.Added, p.Name)
				continue
			case !handMade(tx, byName.id):
				res.Skipped = append(res.Skipped, p.Name+" is in the tracker, but other equipment here that follows another product has that name.")
				continue
			}
			// Hand-made with the same name: it takes the product over.
			if _, err := tx.Exec(`UPDATE equipment SET tracker_product_id = ?, archived = 0 WHERE id = ?`, p.ID, byName.id); err != nil {
				return res, err
			}
			cur = byName
			res.Linked = append(res.Linked, p.Name)
			cur.archived, cur.mode = false, "" // so the details below are written
			linked = true
		}

		name := cur.name
		if p.Name != cur.name {
			var other int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM equipment WHERE name = ? AND id != ?`, p.Name, cur.id).Scan(&other); err != nil {
				return res, err
			}
			if other > 0 {
				res.Skipped = append(res.Skipped, cur.name+" is now "+p.Name+" in the tracker, but other equipment here has that name, so it kept its name.")
			} else {
				name = p.Name
			}
		}
		if name == cur.name && p.Booking == cur.mode && p.Total == cur.total && p.Notes == cur.notes && !cur.archived && slices.Equal(rooms, cur.rooms) {
			continue
		}
		if _, err := tx.Exec(`UPDATE equipment SET name = ?, mode = ?, total = ?, notes = ?, archived = 0 WHERE id = ?`,
			name, p.Booking, p.Total, p.Notes, cur.id); err != nil {
			return res, err
		}
		if err := setEquipmentRooms(tx, cur.id, rooms); err != nil {
			return res, err
		}
		if _, err := tx.Exec(`UPDATE booking_equipment SET name = ? WHERE equipment_id = ?`, name, cur.id); err != nil {
			return res, err
		}
		if !linked {
			res.Updated = append(res.Updated, name)
		}
	}

	// Equipment whose product is no longer offered.
	rows, err = tx.Query(`SELECT id, name, tracker_product_id FROM equipment WHERE tracker_product_id IS NOT NULL AND archived = 0`)
	if err != nil {
		return res, err
	}
	var gone []int64
	for rows.Next() {
		var id, product int64
		var name string
		if err := rows.Scan(&id, &name, &product); err != nil {
			rows.Close()
			return res, err
		}
		if !offered[product] {
			gone = append(gone, id)
			res.Archived = append(res.Archived, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, id := range gone {
		if _, err := tx.Exec(`UPDATE equipment SET archived = 1 WHERE id = ?`, id); err != nil {
			return res, err
		}
	}
	return res, tx.Commit()
}

// handMade reports whether the equipment doesn't follow a tracker product.
func handMade(q querier, id int64) bool {
	var product sql.NullInt64
	q.QueryRow(`SELECT tracker_product_id FROM equipment WHERE id = ?`, id).Scan(&product)
	return !product.Valid
}
