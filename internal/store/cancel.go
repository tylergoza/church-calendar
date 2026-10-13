package store

import (
	"database/sql"
	"errors"

	"github.com/tylergoza/church-calendar/internal/recur"
)

// ErrTakenOver is returned when the person tries to change or cancel a
// request whose booking a planner took over (the event or production now
// owns it): it's changed in that app.
var ErrTakenOver = errors.New("that booking is now part of an event")

// ownRequestBooking checks, inside tx, that request id is still waiting or
// approved and that its booking (if any) is still the request's own, and
// returns the request's booking ID (0 if none) and who asked.
func ownRequestBooking(tx *sql.Tx, id int64) (bookingID, userID int64, err error) {
	var status string
	if err := tx.QueryRow(`SELECT status, COALESCE(booking_id, 0), COALESCE(user_id, 0) FROM requests WHERE id = ?`, id).
		Scan(&status, &bookingID, &userID); err != nil {
		return 0, 0, notFound(err)
	}
	if status != "waiting" && status != "approved" {
		return 0, 0, ErrAlreadyDecided
	}
	if bookingID != 0 {
		var source string
		err := tx.QueryRow(`SELECT source FROM bookings WHERE id = ?`, bookingID).Scan(&source)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			bookingID = 0
		case err != nil:
			return 0, 0, err
		case source != SourceRequest:
			return 0, 0, ErrTakenOver
		}
	}
	return bookingID, userID, nil
}

// UpdateOwnRequest saves the member's changes to their request (r.ID says
// which): title, times, rooms, head count and details. Its booking is
// changed with it and stays (or becomes) confirmed. The new times and
// rooms are checked like a new booking, ignoring the booking itself, in
// the same transaction as the save: if any room is busy nothing changes
// and the clashes come back with ErrRoomsBusy. A request still waiting
// (sent before members booked at once) is approved by this. Refused with
// ErrAlreadyDecided once it's declined, withdrawn or cancelled, and with
// ErrTakenOver once a planner took the booking over.
func (s *Store) UpdateOwnRequest(r *Request, roomIDs []int64) (map[int64][]Booking, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	bookingID, userID, err := ownRequestBooking(tx, r.ID)
	if err != nil {
		return nil, err
	}
	busy, err := busyInRooms(tx, roomIDs, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime, bookingID)
	if err != nil {
		return nil, err
	}
	if len(busy) > 0 {
		return busy, ErrRoomsBusy
	}
	if _, err := tx.Exec(`UPDATE requests SET title = ?, starts_on = ?, start_time = ?, ends_on = ?, end_time = ?,
		expected_count = ?, details = ?, status = 'approved' WHERE id = ?`,
		r.Title, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime, nullInt(int64(r.ExpectedCount)), r.Details, r.ID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM request_rooms WHERE request_id = ?`, r.ID); err != nil {
		return nil, err
	}
	for _, id := range roomIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO request_rooms (request_id, room_id) VALUES (?, ?)`, r.ID, id); err != nil {
			return nil, err
		}
	}
	r.Status = "approved"
	if bookingID == 0 {
		r.UserID = userID
		if err := insertRequestBooking(tx, r, "confirmed", userID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	r.BookingID = bookingID
	if _, err := tx.Exec(`UPDATE bookings SET title = ?, starts_on = ?, start_time = ?, ends_on = ?, end_time = ?,
		status = 'confirmed', created_by = COALESCE(created_by, ?), updated_at = datetime('now') WHERE id = ?`,
		r.Title, r.StartsOn, r.StartTime, r.EndsOn, r.EndTime, nullInt(userID), bookingID); err != nil {
		return nil, err
	}
	if err := setRooms(tx, bookingID, roomIDs); err != nil {
		return nil, err
	}
	return nil, tx.Commit()
}

// CancelOwnRequest is the member cancelling their own request, waiting or
// booked: the request is withdrawn and its booking cancelled, which frees
// the rooms. Refused like UpdateOwnRequest.
func (s *Store) CancelOwnRequest(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	bookingID, _, err := ownRequestBooking(tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE requests SET status = 'withdrawn' WHERE id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE bookings SET status = 'cancelled', updated_at = datetime('now') WHERE id = ?`, bookingID); err != nil {
		return err
	}
	return tx.Commit()
}

// cancelBooking cancels a hand-made booking (calendar or request) with a
// note and who did it. The request behind it becomes cancelled, with the
// note as its answer. A booking already cancelled is ErrAlreadyDecided;
// one from another app is ErrTakenOver.
func cancelBooking(tx *sql.Tx, id, by int64, note string) error {
	var source, status string
	if err := tx.QueryRow(`SELECT source, status FROM bookings WHERE id = ?`, id).Scan(&source, &status); err != nil {
		return notFound(err)
	}
	if source != SourceCalendar && source != SourceRequest {
		return ErrTakenOver
	}
	if status == "cancelled" {
		return ErrAlreadyDecided
	}
	if _, err := tx.Exec(`UPDATE bookings SET status = 'cancelled', cancel_note = ?, cancelled_by = ?,
		updated_at = datetime('now') WHERE id = ?`, note, nullInt(by), id); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE requests SET status = 'cancelled', answer = ? WHERE booking_id = ? AND status IN ('waiting', 'approved')`, note, id)
	return err
}

// CancelBooking is a scheduler cancelling one hand-made or request
// booking: kept, with the note, but off the calendar and out of the way of
// its rooms. For a date of a repeat it cancels just that date.
func (s *Store) CancelBooking(id, by int64, note string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := cancelBooking(tx, id, by, note); err != nil {
		return err
	}
	return tx.Commit()
}

// CancelSeriesFrom cancels every date of a repeat with occurrence_date >=
// from that isn't cancelled yet, and ends the repeat the day before so no
// later dates are made. Earlier dates are untouched. Returns how many
// bookings were cancelled.
func (s *Store) CancelSeriesFrom(seriesID int64, from string, by int64, note string) (int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	sr, err := loadSeries(tx, seriesID)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT id FROM bookings WHERE series_id = ? AND occurrence_date >= ? AND status <> 'cancelled'`, seriesID, from)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := cancelBooking(tx, id, by, note); err != nil {
			return 0, err
		}
	}
	until := shiftDate(from, -1)
	if !sr.Rule.Until.IsZero() && recur.FormatDate(sr.Rule.Until) < until {
		until = recur.FormatDate(sr.Rule.Until) // the earlier end wins
	}
	if _, err := tx.Exec(`UPDATE series SET until = ? WHERE id = ?`, until, seriesID); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit()
}

// BookingsOf lists the bookings a person made by hand (not from a
// request), newest first, for their My bookings page.
func (s *Store) BookingsOf(userID int64) ([]Booking, error) {
	return queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE created_by = ? AND source = ?
		ORDER BY starts_on DESC, start_time DESC, id DESC LIMIT 100`, userID, SourceCalendar)
}
