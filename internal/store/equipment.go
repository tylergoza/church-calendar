package store

import (
	"errors"
	"strings"
)

// ErrEquipmentNameTaken is returned when other equipment already has the name.
var ErrEquipmentNameTaken = errors.New("other equipment already has that name")

// Equipment modes: count is asked how many, up to Total (tables, chairs);
// service is asked yes or no with a note (Sound, Lighting) and never runs
// out.
const (
	ModeCount   = "count"
	ModeService = "service"
)

// Equipment is something people can ask for with a booking: a thin copy
// of a tracker product set "On the church calendar", or made by hand.
type Equipment struct {
	ID        int64
	Name      string
	ProductID int64  // the tracker's product ID; 0 when made by hand
	Mode      string // ModeCount or ModeService
	Total     int    // how many there are; 0 for a service
	Notes     string // shown on the form
	Archived  bool
	RoomIDs   []int64 // the rooms it's offered with; none = not offered
}

// FromTracker reports whether the equipment comes from the tracker.
func (e Equipment) FromTracker() bool { return e.ProductID != 0 }

// Service reports whether it's asked yes or no (it never runs out).
func (e Equipment) Service() bool { return e.Mode == ModeService }

// OfferedIn reports whether it's offered with the room.
func (e Equipment) OfferedIn(roomID int64) bool {
	for _, id := range e.RoomIDs {
		if id == roomID {
			return true
		}
	}
	return false
}

const equipmentCols = `id, name, COALESCE(tracker_product_id, 0), mode, total, notes, archived`

func (s *Store) queryEquipment(query string, args ...any) ([]Equipment, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	var out []Equipment
	idx := map[int64]int{}
	for rows.Next() {
		var e Equipment
		if err := rows.Scan(&e.ID, &e.Name, &e.ProductID, &e.Mode, &e.Total, &e.Notes, &e.Archived); err != nil {
			rows.Close()
			return nil, err
		}
		idx[e.ID] = len(out)
		out = append(out, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]string, 0, len(out))
	for _, e := range out {
		ids = append(ids, itoa(e.ID))
	}
	// The IDs are integers from the database, so joining them is safe.
	rrows, err := s.DB.Query(`SELECT er.equipment_id, er.room_id FROM equipment_rooms er JOIN rooms r ON r.id = er.room_id
		WHERE er.equipment_id IN (` + strings.Join(ids, ",") + `) ORDER BY r.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var id, room int64
		if err := rrows.Scan(&id, &room); err != nil {
			return nil, err
		}
		out[idx[id]].RoomIDs = append(out[idx[id]].RoomIDs, room)
	}
	return out, rrows.Err()
}

// ListEquipment lists equipment by name with the rooms it's offered with,
// archived ones only when asked.
func (s *Store) ListEquipment(withArchived bool) ([]Equipment, error) {
	q := `SELECT ` + equipmentCols + ` FROM equipment`
	if !withArchived {
		q += ` WHERE archived = 0`
	}
	return s.queryEquipment(q + ` ORDER BY archived, name COLLATE NOCASE`)
}

// GetEquipment loads one piece of equipment with its rooms.
func (s *Store) GetEquipment(id int64) (*Equipment, error) {
	list, err := s.queryEquipment(`SELECT `+equipmentCols+` FROM equipment WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// OfferedWith lists the equipment (not archived) offered with any of the
// rooms, by name. Exact match only: equipment offered with a building is
// offered when the building itself is picked (booked whole), not a room
// inside it, and the other way round.
func (s *Store) OfferedWith(roomIDs []int64) ([]Equipment, error) {
	if len(roomIDs) == 0 {
		return nil, nil
	}
	ids := make([]string, len(roomIDs))
	for i, id := range roomIDs {
		ids[i] = itoa(id)
	}
	return s.queryEquipment(`SELECT ` + equipmentCols + ` FROM equipment WHERE archived = 0
		AND id IN (SELECT equipment_id FROM equipment_rooms WHERE room_id IN (` + strings.Join(ids, ",") + `))
		ORDER BY name COLLATE NOCASE`)
}

func equipmentNameErr(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: equipment.name") {
		return ErrEquipmentNameTaken
	}
	return err
}

// tidyEquipment checks the name and mode; a service has no total.
func tidyEquipment(e *Equipment) error {
	e.Name = strings.TrimSpace(e.Name)
	if e.Name == "" {
		return errors.New("give the equipment a name")
	}
	switch e.Mode {
	case ModeCount:
		if e.Total < 0 {
			e.Total = 0
		}
	case ModeService:
		e.Total = 0
	default:
		return errors.New("pick how it's asked for")
	}
	return nil
}

// setEquipmentRooms replaces the rooms equipment is offered with. Unknown
// room IDs are ignored.
func setEquipmentRooms(q querier, id int64, roomIDs []int64) error {
	if _, err := q.Exec(`DELETE FROM equipment_rooms WHERE equipment_id = ?`, id); err != nil {
		return err
	}
	for _, room := range roomIDs {
		if _, err := q.Exec(`INSERT OR IGNORE INTO equipment_rooms (equipment_id, room_id)
			SELECT ?, id FROM rooms WHERE id = ?`, id, room); err != nil {
			return err
		}
	}
	return nil
}

// CreateEquipment adds equipment (made by hand unless ProductID is set)
// with the rooms it's offered with.
func (s *Store) CreateEquipment(e *Equipment) (int64, error) {
	if err := tidyEquipment(e); err != nil {
		return 0, err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO equipment (name, tracker_product_id, mode, total, notes, archived) VALUES (?, ?, ?, ?, ?, ?)`,
		e.Name, nullInt(e.ProductID), e.Mode, e.Total, e.Notes, e.Archived)
	if err != nil {
		return 0, equipmentNameErr(err)
	}
	if e.ID, err = res.LastInsertId(); err != nil {
		return 0, err
	}
	if err := setEquipmentRooms(tx, e.ID, e.RoomIDs); err != nil {
		return 0, err
	}
	return e.ID, tx.Commit()
}

// UpdateEquipment saves equipment's details and rooms, and keeps the name
// stored on bookings in step. The tracker product ID doesn't change.
func (s *Store) UpdateEquipment(e *Equipment) error {
	if err := tidyEquipment(e); err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE equipment SET name = ?, mode = ?, total = ?, notes = ?, archived = ? WHERE id = ?`,
		e.Name, e.Mode, e.Total, e.Notes, e.Archived, e.ID)
	if err != nil {
		return equipmentNameErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := setEquipmentRooms(tx, e.ID, e.RoomIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE booking_equipment SET name = ? WHERE equipment_id = ?`, e.Name, e.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// EquipmentLine is one thing asked for on a request or booking.
type EquipmentLine struct {
	EquipmentID int64
	Name        string
	Mode        string // the equipment's mode now
	Total       int    // the equipment's total now (0 for a service)
	ProductID   int64  // the tracker's product ID; 0 if made by hand
	Quantity    int    // 1 for a service
	Note        string
	// Bookings only. InUse is the In use / In storage toggle, used while
	// the tracker connection is off; it stays on until someone turns it off.
	InUse          bool
	InUseChangedAt string
	InUseChangedBy int64
	// Short is how many more are asked for at the same time (this booking
	// and the ones overlapping it, not cancelled) than there are. Worked
	// out when read; 0 for services, request lines and cancelled bookings.
	Short int
}

// Service reports whether the line is a service (yes or no, never short).
func (l EquipmentLine) Service() bool { return l.Mode == ModeService }

// tidyLines drops lines with no equipment or quantity, keeps one line per
// equipment, makes services 1 and cuts notes to 200 characters.
func tidyLines(q querier, lines []EquipmentLine) ([]EquipmentLine, error) {
	var out []EquipmentLine
	seen := map[int64]bool{}
	for _, l := range lines {
		if l.EquipmentID == 0 || l.Quantity < 1 || seen[l.EquipmentID] {
			continue
		}
		var name, mode string
		if err := q.QueryRow(`SELECT name, mode FROM equipment WHERE id = ?`, l.EquipmentID).Scan(&name, &mode); err != nil {
			if errors.Is(notFound(err), ErrNotFound) {
				continue
			}
			return nil, err
		}
		seen[l.EquipmentID] = true
		l.Name, l.Mode = name, mode
		if mode == ModeService {
			l.Quantity = 1
		}
		l.Note = strings.TrimSpace(l.Note)
		if r := []rune(l.Note); len(r) > 200 {
			l.Note = string(r[:200])
		}
		out = append(out, l)
	}
	return out, nil
}

// setRequestEquipment replaces what a request asks for.
func setRequestEquipment(q querier, requestID int64, lines []EquipmentLine) error {
	lines, err := tidyLines(q, lines)
	if err != nil {
		return err
	}
	if _, err := q.Exec(`DELETE FROM request_equipment WHERE request_id = ?`, requestID); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := q.Exec(`INSERT INTO request_equipment (request_id, equipment_id, quantity, note) VALUES (?, ?, ?, ?)`,
			requestID, l.EquipmentID, l.Quantity, l.Note); err != nil {
			return err
		}
	}
	return nil
}

// setBookingEquipment replaces what a booking asks for. Lines for
// equipment it already had keep their In use toggle.
func setBookingEquipment(q querier, bookingID int64, lines []EquipmentLine) error {
	lines, err := tidyLines(q, lines)
	if err != nil {
		return err
	}
	keep := make([]string, 0, len(lines))
	for _, l := range lines {
		keep = append(keep, itoa(l.EquipmentID))
	}
	del := `DELETE FROM booking_equipment WHERE booking_id = ?`
	if len(keep) > 0 {
		del += ` AND equipment_id NOT IN (` + strings.Join(keep, ",") + `)`
	}
	if _, err := q.Exec(del, bookingID); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := q.Exec(`INSERT INTO booking_equipment (booking_id, equipment_id, name, quantity, note) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (booking_id, equipment_id) DO UPDATE SET name = excluded.name, quantity = excluded.quantity, note = excluded.note`,
			bookingID, l.EquipmentID, l.Name, l.Quantity, l.Note); err != nil {
			return err
		}
	}
	return nil
}

// copyRequestEquipment gives a request's booking what the request asks for.
func copyRequestEquipment(q querier, requestID, bookingID int64) error {
	lines, err := requestEquipment(q, requestID)
	if err != nil {
		return err
	}
	return setBookingEquipment(q, bookingID, lines)
}

// SetBookingEquipment replaces what a booking asks for (hand-made and
// request bookings; the planners' bookings never carry equipment).
func (s *Store) SetBookingEquipment(bookingID int64, lines []EquipmentLine) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setBookingEquipment(tx, bookingID, lines); err != nil {
		return err
	}
	return tx.Commit()
}

func requestEquipment(q querier, requestID int64) ([]EquipmentLine, error) {
	rows, err := q.Query(`SELECT re.equipment_id, e.name, e.mode, e.total, COALESCE(e.tracker_product_id, 0), re.quantity, re.note
		FROM request_equipment re JOIN equipment e ON e.id = re.equipment_id
		WHERE re.request_id = ? ORDER BY e.name COLLATE NOCASE`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EquipmentLine
	for rows.Next() {
		var l EquipmentLine
		if err := rows.Scan(&l.EquipmentID, &l.Name, &l.Mode, &l.Total, &l.ProductID, &l.Quantity, &l.Note); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RequestEquipment lists what a request asks for, by name.
func (s *Store) RequestEquipment(requestID int64) ([]EquipmentLine, error) {
	return requestEquipment(s.DB, requestID)
}

// BookingEquipment lists what a booking asks for, by name, with how short
// each counted line is at the booking's times.
func (s *Store) BookingEquipment(bookingID int64) ([]EquipmentLine, error) {
	if _, err := s.GetBooking(bookingID); err != nil {
		return nil, err
	}
	all, err := s.BookingsEquipment([]int64{bookingID})
	return all[bookingID], err
}

// BookingsEquipment is BookingEquipment for many bookings at once, keyed
// by booking ID. Bookings asking for nothing have no key.
func (s *Store) BookingsEquipment(bookingIDs []int64) (map[int64][]EquipmentLine, error) {
	out := map[int64][]EquipmentLine{}
	if len(bookingIDs) == 0 {
		return out, nil
	}
	ids := make([]string, len(bookingIDs))
	for i, id := range bookingIDs {
		ids[i] = itoa(id)
	}
	// short is what overlapping lines (this one included) ask for over the
	// total; a cancelled booking has no span, so it's never short.
	rows, err := s.DB.Query(`WITH ` + spansSQL + `,
		lines AS (
			SELECT be.booking_id, be.equipment_id, be.quantity, sp.s, sp.e
			FROM booking_equipment be JOIN spans sp ON sp.id = be.booking_id)
		SELECT be.booking_id, be.equipment_id, be.name, e.mode, e.total, COALESCE(e.tracker_product_id, 0), be.quantity, be.note,
			be.in_use, be.in_use_changed_at, COALESCE(be.in_use_changed_by, 0),
			CASE WHEN e.mode = 'count' AND sp.id IS NOT NULL THEN max(0, (SELECT SUM(o.quantity) FROM lines o
				WHERE o.equipment_id = be.equipment_id AND o.s < sp.e AND sp.s < o.e) - e.total) ELSE 0 END
		FROM booking_equipment be JOIN equipment e ON e.id = be.equipment_id
		LEFT JOIN spans sp ON sp.id = be.booking_id
		WHERE be.booking_id IN (` + strings.Join(ids, ",") + `) ORDER BY be.booking_id, be.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var l EquipmentLine
		if err := rows.Scan(&id, &l.EquipmentID, &l.Name, &l.Mode, &l.Total, &l.ProductID, &l.Quantity, &l.Note,
			&l.InUse, &l.InUseChangedAt, &l.InUseChangedBy, &l.Short); err != nil {
			return nil, err
		}
		out[id] = append(out[id], l)
	}
	return out, rows.Err()
}

// EquipmentAskedFor adds up, for each counted piece of equipment, how many
// the bookings overlapping the given times ask for (not cancelled, so
// pending and pencilled-in ones count), leaving out booking except. The
// form shows the total less this as "not asked for then".
func (s *Store) EquipmentAskedFor(startsOn, startTime, endsOn, endTime string, except int64) (map[int64]int, error) {
	start, end := spanOf(startsOn, startTime, endsOn, endTime)
	rows, err := s.DB.Query(`WITH `+spansSQL+`
		SELECT be.equipment_id, SUM(be.quantity) FROM booking_equipment be
		JOIN spans sp ON sp.id = be.booking_id JOIN equipment e ON e.id = be.equipment_id
		WHERE e.mode = 'count' AND sp.s < ? AND ? < sp.e AND sp.id <> ?
		GROUP BY be.equipment_id`, end, start, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// EquipmentClashes lists, for each counted line of a booking, the other
// bookings (not cancelled) asking for the same equipment at the same time.
func (s *Store) EquipmentClashes(bookingID int64) (map[int64][]Booking, error) {
	rows, err := s.DB.Query(`WITH `+spansSQL+`
		SELECT DISTINCT mine.equipment_id, other.booking_id
		FROM booking_equipment mine JOIN spans a ON a.id = mine.booking_id
		JOIN equipment e ON e.id = mine.equipment_id AND e.mode = 'count'
		JOIN booking_equipment other ON other.equipment_id = mine.equipment_id AND other.booking_id <> mine.booking_id
		JOIN spans b ON b.id = other.booking_id
		WHERE mine.booking_id = ? AND a.s < b.e AND b.s < a.e`, bookingID)
	if err != nil {
		return nil, err
	}
	want := map[int64][]int64{}
	var ids []string
	seen := map[int64]bool{}
	for rows.Next() {
		var equip, id int64
		if err := rows.Scan(&equip, &id); err != nil {
			rows.Close()
			return nil, err
		}
		want[equip] = append(want[equip], id)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, itoa(id))
		}
	}
	rows.Close()
	out := map[int64][]Booking{}
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return out, err
	}
	list, err := queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE id IN (`+strings.Join(ids, ",")+`)
		ORDER BY starts_on, start_time <> '', start_time, id`)
	if err != nil {
		return nil, err
	}
	for equip, bids := range want {
		for _, b := range list {
			for _, id := range bids {
				if b.ID == id {
					out[equip] = append(out[equip], b)
				}
			}
		}
	}
	return out, nil
}

// ShortBookings lists bookings that haven't ended before from (YYYY-MM-DD)
// with at least one counted line asked for over the total at its times,
// soonest first.
func (s *Store) ShortBookings(from string) ([]Booking, error) {
	return queryBookings(s.DB, `WITH `+spansSQL+`,
		lines AS (
			SELECT be.booking_id, be.equipment_id, be.quantity, sp.s, sp.e, e.total
			FROM booking_equipment be JOIN spans sp ON sp.id = be.booking_id
			JOIN equipment e ON e.id = be.equipment_id AND e.mode = 'count')
		SELECT `+bookingCols+` FROM bookings WHERE id IN (
			SELECT a.booking_id FROM lines a WHERE a.e > ?
			AND (SELECT SUM(b.quantity) FROM lines b WHERE b.equipment_id = a.equipment_id AND b.s < a.e AND a.s < b.e) > a.total)
		ORDER BY starts_on, start_time <> '', start_time, id`, from+" 00:00")
}

// SetLineInUse turns a booking line's In use / In storage toggle on or off.
// by is the scheduler. It stays as set whatever the booking's times.
func (s *Store) SetLineInUse(bookingID, equipmentID int64, inUse bool, by int64) error {
	res, err := s.DB.Exec(`UPDATE booking_equipment SET in_use = ?, in_use_changed_at = datetime('now'), in_use_changed_by = ?
		WHERE booking_id = ? AND equipment_id = ? AND in_use <> ?`, inUse, nullInt(by), bookingID, equipmentID, inUse)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var one int
		if err := s.DB.QueryRow(`SELECT 1 FROM booking_equipment WHERE booking_id = ? AND equipment_id = ?`, bookingID, equipmentID).Scan(&one); err != nil {
			return notFound(err)
		}
	}
	return nil
}

// InUseLine is a booking line whose toggle is on: the equipment is set up
// in the booking's rooms.
type InUseLine struct {
	Line    EquipmentLine
	Booking Booking
}

// InUseLines lists every line marked in use, oldest change first.
func (s *Store) InUseLines() ([]InUseLine, error) {
	rows, err := s.DB.Query(`SELECT be.booking_id, be.equipment_id, be.name, e.mode, e.total, COALESCE(e.tracker_product_id, 0),
			be.quantity, be.note, be.in_use, be.in_use_changed_at, COALESCE(be.in_use_changed_by, 0)
		FROM booking_equipment be JOIN equipment e ON e.id = be.equipment_id
		WHERE be.in_use = 1 ORDER BY be.in_use_changed_at, be.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	var out []InUseLine
	var ids []string
	seen := map[int64]bool{}
	for rows.Next() {
		var u InUseLine
		l := &u.Line
		if err := rows.Scan(&u.Booking.ID, &l.EquipmentID, &l.Name, &l.Mode, &l.Total, &l.ProductID,
			&l.Quantity, &l.Note, &l.InUse, &l.InUseChangedAt, &l.InUseChangedBy); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, u)
		if !seen[u.Booking.ID] {
			seen[u.Booking.ID] = true
			ids = append(ids, itoa(u.Booking.ID))
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	list, err := queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE id IN (`+strings.Join(ids, ",")+`)`)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]Booking, len(list))
	for _, b := range list {
		byID[b.ID] = b
	}
	for i := range out {
		out[i].Booking = byID[out[i].Booking.ID]
	}
	return out, nil
}

// EquipmentPlace is where some of a counted piece of tracker equipment is
// now, from the tracker's items. RoomID is set when the place is a
// calendar room.
type EquipmentPlace struct {
	EquipmentID int64
	Path        string
	RoomID      int64
	Quantity    int
}

// SetEquipmentPlaces replaces where a piece of equipment is (the sync
// does this every time).
func (s *Store) SetEquipmentPlaces(equipmentID int64, places []EquipmentPlace) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM equipment_places WHERE equipment_id = ?`, equipmentID); err != nil {
		return err
	}
	for _, p := range places {
		if _, err := tx.Exec(`INSERT INTO equipment_places (equipment_id, place_path, room_id, quantity)
			VALUES (?, ?, (SELECT id FROM rooms WHERE id = ?), ?)`, equipmentID, p.Path, p.RoomID, p.Quantity); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EquipmentPlaces lists where each piece of tracker equipment is, keyed by
// equipment ID, most first.
func (s *Store) EquipmentPlaces() (map[int64][]EquipmentPlace, error) {
	rows, err := s.DB.Query(`SELECT equipment_id, place_path, COALESCE(room_id, 0), quantity FROM equipment_places
		ORDER BY equipment_id, quantity DESC, place_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]EquipmentPlace{}
	for rows.Next() {
		var p EquipmentPlace
		if err := rows.Scan(&p.EquipmentID, &p.Path, &p.RoomID, &p.Quantity); err != nil {
			return nil, err
		}
		out[p.EquipmentID] = append(out[p.EquipmentID], p)
	}
	return out, rows.Err()
}

// SetRequestEquipment replaces what a request asks for (a scheduler
// editing its booking keeps the two in step).
func (s *Store) SetRequestEquipment(requestID int64, lines []EquipmentLine) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := setRequestEquipment(tx, requestID, lines); err != nil {
		return err
	}
	return tx.Commit()
}
