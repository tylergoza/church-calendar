package store

// BookingsAskingEquipment lists the bookings (not cancelled) on any day
// from..to (YYYY-MM-DD) that ask for equipment or services, soonest first.
// Start-of-day bookings come before timed ones, as on the calendar.
func (s *Store) BookingsAskingEquipment(from, to string) ([]Booking, error) {
	return queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings
		WHERE status <> 'cancelled' AND starts_on <= ?
		AND CASE ends_on WHEN '' THEN starts_on ELSE ends_on END >= ?
		AND EXISTS (SELECT 1 FROM booking_equipment be WHERE be.booking_id = bookings.id)
		ORDER BY starts_on, start_time <> '', start_time, id`, to, from)
}
