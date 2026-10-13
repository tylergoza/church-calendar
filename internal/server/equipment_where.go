package server

import (
	"time"

	"github.com/tylergoza/church-calendar/internal/store"
)

// Where equipment is now, and who sees what of it. With the tracker
// connected the places come from its items; alone they come from the
// In use / In storage toggle on each booking's line. Equipment is shown
// only to schedulers, admins and the booking's owner, and never on busy
// blocks.

// canSeeEquipment reports whether u may see the equipment asked for on b:
// schedulers and admins, and the person who made it, on a booking they can
// see in full. Visitors, other members and busy blocks never.
func canSeeEquipment(b *store.Booking, u *store.User) bool {
	if u == nil || !canSeeDetails(b, u) {
		return false
	}
	return u.CanSchedule() || (b.CreatedBy != 0 && b.CreatedBy == u.ID)
}

// trackerOn reports whether equipment places come from the tracker (so the
// toggle is off).
func (s *Server) trackerOn() bool { return s.tracker().Configured() }

// inUseHere is a line marked in use, with its booking.
type inUseHere struct {
	Line    store.EquipmentLine
	Booking store.Booking
	Since   string // "Sat 18 Oct"
}

// equipWhere is where one piece of counted equipment is now.
type equipWhere struct {
	Tracker bool                   // Places come from the tracker
	Places  []store.EquipmentPlace // with the tracker, most first
	InUse   []inUseHere            // alone: lines whose toggle is on
	Storage int                    // alone: how many are left in storage (when the total is known)
	Known   bool                   // alone: the total is known, so Storage counts
}

// Empty reports there's nothing to say (a tracker product with no items).
func (w *equipWhere) Empty() bool {
	return w == nil || (w.Tracker && len(w.Places) == 0)
}

// whereNow holds what's needed to say where any equipment is: loaded
// once per page.
type whereNow struct {
	tracker bool
	places  map[int64][]store.EquipmentPlace
	inUse   map[int64][]inUseHere
}

func (s *Server) loadWhere() (*whereNow, error) {
	w := &whereNow{tracker: s.trackerOn(), inUse: map[int64][]inUseHere{}}
	var err error
	if w.places, err = s.store.EquipmentPlaces(); err != nil {
		return nil, err
	}
	lines, err := s.store.InUseLines()
	if err != nil {
		return nil, err
	}
	for _, u := range lines {
		w.inUse[u.Line.EquipmentID] = append(w.inUse[u.Line.EquipmentID],
			inUseHere{Line: u.Line, Booking: u.Booking, Since: sinceLabel(u.Line.InUseChangedAt)})
	}
	return w, nil
}

// of says where a counted piece of equipment is. Tracker equipment follows
// the tracker while it's connected; everything else follows the toggle.
func (w *whereNow) of(equipmentID int64, total int, fromTracker bool) *equipWhere {
	if w.tracker && fromTracker {
		return &equipWhere{Tracker: true, Places: w.places[equipmentID]}
	}
	out := &equipWhere{InUse: w.inUse[equipmentID], Known: total > 0}
	used := 0
	for _, u := range out.InUse {
		used += u.Line.Quantity
	}
	out.Storage = max(0, total-used)
	return out
}

// sinceLabel turns a SQLite UTC timestamp into "Sat 18 Oct" (local time).
func sinceLabel(ts string) string {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		return ts
	}
	return t.Local().Format("Mon 2 Jan")
}

// equipLineView is a line as a page shows it. Clashes and Where are only
// for schedulers.
type equipLineView struct {
	store.EquipmentLine
	Clashes []store.Booking // other bookings asking for it at the same time
	Where   *equipWhere
}

// equipView is the equipment asked for on a request or booking, ready for
// the "equipment-card" template. Staff (schedulers and admins) also see
// how short each line is and who else asked; Toggle shows the In use / In
// storage buttons, only when the tracker is off.
type equipView struct {
	Lines  []equipLineView
	Staff  bool
	Toggle bool
	Back   string // where the toggle sends you back to
}

// buildEquipView prepares lines for display. b is the booking they belong
// to (nil for a request that has none yet). full adds where each counted
// line is now and the toggle (the booking page); schedulers only.
func (s *Server) buildEquipView(lines []store.EquipmentLine, b *store.Booking, staff, full bool, back string) (*equipView, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	v := &equipView{Staff: staff, Toggle: staff && full && b != nil && !s.trackerOn(), Back: back}
	var clashes map[int64][]store.Booking
	var where *whereNow
	var err error
	if staff && b != nil {
		if clashes, err = s.store.EquipmentClashes(b.ID); err != nil {
			return nil, err
		}
	}
	if staff && full {
		if where, err = s.loadWhere(); err != nil {
			return nil, err
		}
	}
	for _, l := range lines {
		lv := equipLineView{EquipmentLine: l, Clashes: clashes[l.EquipmentID]}
		if where != nil && !l.Service() {
			lv.Where = where.of(l.EquipmentID, l.Total, l.ProductID != 0)
		}
		v.Lines = append(v.Lines, lv)
	}
	return v, nil
}
