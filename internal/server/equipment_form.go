package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/tylergoza/church-calendar/internal/store"
)

// Equipment on the request and booking forms. The "equipment-fields"
// partial takes a []equipRow; field names are equip_<id> (a quantity for
// counted equipment, "1" for a service) and equip_note_<id>.

// equipRow is one piece of equipment on a form, with what's been asked.
type equipRow struct {
	E     store.Equipment
	Qty   int    // asked for; 0 = not asked
	Note  string // for a service (and counted, if sent)
	Left  int    // counted: total less what other bookings ask for then
	Known bool   // Left is worked out (the times are known)
	Rooms string // IDs of the rooms it's offered with, space separated
}

const maxEquipFormNote = 200

func equipFieldName(prefix string, id int64) string { return prefix + strconv.FormatInt(id, 10) }

// readEquipment reads the equipment fields. Only equipment offered with
// the rooms picked counts; the rest is ignored. Counted quantities are 0
// (not asked) up to the total; services are ticked or not; notes are up
// to 200 characters. The lines come back even with errors, so the form
// can be shown again with them.
func (s *Server) readEquipment(r *http.Request, rooms []int64) ([]store.EquipmentLine, []string, error) {
	offered, err := s.store.OfferedWith(rooms)
	if err != nil {
		return nil, nil, err
	}
	var lines []store.EquipmentLine
	var errs []string
	for _, e := range offered {
		l := store.EquipmentLine{EquipmentID: e.ID, Name: e.Name, Mode: e.Mode, Total: e.Total,
			Note: formStr(r, equipFieldName("equip_note_", e.ID))}
		v := formStr(r, equipFieldName("equip_", e.ID))
		if e.Service() {
			if v == "" || v == "0" {
				continue
			}
			l.Quantity = 1
		} else if v != "" {
			n, err := strconv.Atoi(v)
			switch {
			case err != nil || n < 0:
				errs = append(errs, "How many "+e.Name+" is a number.")
				continue
			case n > e.Total:
				errs = append(errs, "There are only "+strconv.Itoa(e.Total)+" of "+e.Name+".")
			}
			if n == 0 {
				continue
			}
			l.Quantity = n
		} else {
			continue
		}
		if len([]rune(l.Note)) > maxEquipFormNote {
			errs = append(errs, "Keep the note for "+e.Name+" under "+strconv.Itoa(maxEquipFormNote)+" characters.")
		}
		lines = append(lines, l)
	}
	return lines, errs, nil
}

// formEquipmentRows builds the rows for a form: every piece of equipment
// offered with any of the rooms shown (JavaScript trims it to the rooms
// picked; the server checks it on submit), with what's already asked
// (lines) and how many are left at the times when they're known. except
// is the booking being edited, which doesn't count against itself.
func (s *Server) formEquipmentRows(shown []store.Room, lines []store.EquipmentLine, startsOn, startTime, endsOn, endTime string, except int64) ([]equipRow, error) {
	ids := make([]int64, len(shown))
	for i, rm := range shown {
		ids[i] = rm.ID
	}
	offered, err := s.store.OfferedWith(ids)
	if err != nil || len(offered) == 0 {
		return nil, err
	}
	if endsOn == startsOn {
		endsOn = ""
	}
	known := len(checkWhen(startsOn, startTime, endsOn, endTime)) == 0
	var asked map[int64]int
	if known {
		if asked, err = s.store.EquipmentAskedFor(startsOn, startTime, endsOn, endTime, except); err != nil {
			return nil, err
		}
	}
	rows := make([]equipRow, len(offered))
	for i, e := range offered {
		row := equipRow{E: e, Known: known && !e.Service(), Left: max(0, e.Total-asked[e.ID])}
		for _, l := range lines {
			if l.EquipmentID == e.ID {
				row.Qty, row.Note = l.Quantity, l.Note
			}
		}
		var rooms []string
		for _, id := range e.RoomIDs {
			rooms = append(rooms, strconv.FormatInt(id, 10))
		}
		row.Rooms = strings.Join(rooms, " ")
		rows[i] = row
	}
	return rows, nil
}

// bookingShortNote is a sentence for the flash when a booking asks for more of
// something than there is at its times; "" when nothing is short.
func (s *Server) bookingShortNote(bookingID int64) (string, error) {
	lines, err := s.store.BookingEquipment(bookingID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, l := range lines {
		if l.Short > 0 {
			names = append(names, l.Name)
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	slices.Sort(names)
	return " We may be short of " + joinEquipNames(names) + "; a scheduler will be in touch.", nil
}

// joinEquipNames is "a", "a and b" or "a, b and c".
func joinEquipNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
