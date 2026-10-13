package store

// Access is how much of the calendar a reader may see. The rules are the
// event planner's, so moving its calendar here changes nothing:
//
//	                                             public  private  locked
//	visitors, read tokens without private access  full    busy     busy
//	members, read tokens with private access      full    full     busy
//
// "Busy" is only rooms and times. Locked bookings are busy to everyone.
type Access int

const (
	AccessPublic Access = iota
	AccessPrivate
)

// AccessFor is a signed-in person's access (nil for a visitor).
func AccessFor(u *User) Access {
	if u != nil {
		return AccessPrivate
	}
	return AccessPublic
}

// Full reports whether a booking with this visibility shows in full.
func (a Access) Full(visibility string) bool {
	switch visibility {
	case "public":
		return true
	case "private":
		return a == AccessPrivate
	}
	return false
}

// CalendarEntry is a booking as one reader sees it. When Busy, everything
// but its dates, times and room names is blanked here, so nothing above
// the store can leak a busy booking's details: ID 0, no title, kind,
// source, description or link.
type CalendarEntry struct {
	Booking
	Busy bool
}

// EntryFor is the booking as a reader with access a sees it.
func EntryFor(b Booking, a Access) CalendarEntry {
	if a.Full(b.Visibility) {
		return CalendarEntry{Booking: b}
	}
	rooms := make([]BookingRoom, len(b.Rooms))
	for i, r := range b.Rooms {
		rooms[i] = BookingRoom{Name: r.Name}
	}
	return CalendarEntry{Busy: true, Booking: Booking{
		StartsOn: b.StartsOn, StartTime: b.StartTime, EndsOn: b.EndsOn, EndTime: b.EndTime,
		Status: b.Status, Rooms: rooms,
	}}
}

// Calendar lists the bookings that aren't cancelled on any day from..to
// (inclusive), by date and time, as a reader with access a sees them.
func (s *Store) Calendar(from, to string, a Access) ([]CalendarEntry, error) {
	list, err := queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings
		WHERE status <> 'cancelled' AND starts_on <= ? AND (CASE ends_on WHEN '' THEN starts_on ELSE ends_on END) >= ?
		ORDER BY starts_on, start_time <> '', start_time, id`, to, from)
	if err != nil {
		return nil, err
	}
	out := make([]CalendarEntry, len(list))
	for i, b := range list {
		out[i] = EntryFor(b, a)
	}
	return out, nil
}

// CalendarBooking is one booking the reader may see in full. ErrNotFound
// when it doesn't exist, is cancelled, or would only show as busy, so the
// answer doesn't give away that it's there.
func (s *Store) CalendarBooking(id int64, a Access) (*Booking, error) {
	b, err := s.GetBooking(id)
	if err != nil {
		return nil, err
	}
	if b.Status == "cancelled" || !a.Full(b.Visibility) {
		return nil, ErrNotFound
	}
	return b, nil
}

// BookingBySource finds the booking an app sent, for the apps' old links.
func (s *Store) BookingBySource(source, sourceID string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM bookings WHERE source = ? AND source_id = ?`, source, sourceID).Scan(&id)
	return id, notFound(err)
}
