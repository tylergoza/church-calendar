package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/church-calendar/internal/recur"
)

// SeriesHorizonMonths is how far ahead of "today" repeat dates are made.
const SeriesHorizonMonths = 6

var (
	// ErrNotInSeries is returned when a booking isn't a date of a repeat.
	ErrNotInSeries = errors.New("that booking doesn't repeat")
	// ErrAlreadyInSeries is returned when making a repeat from one of its dates.
	ErrAlreadyInSeries = errors.New("that booking already repeats")
)

// Series is a hand-made repeat's rule. Each date is a real booking, made
// ahead of time by copying the template booking (as the event planner does).
type Series struct {
	ID                int64
	Rule              recur.Rule // Rule.Start is the series' starts_on
	TemplateBookingID int64      // the booking new dates copy; 0 when it was deleted (the latest date is used)
	GeneratedThrough  string     // dates were made up to this one
}

const seriesCols = `id, freq, every, weekdays, month_day, month_week, month_weekday, starts_on, until, max_count, template_booking_id, generated_through`

func loadSeries(q querier, id int64) (*Series, error) {
	var (
		sr                                Series
		freq, weekdays, starts, until     string
		every                             int
		mDay, mWeek, mWd, maxCount, tplID sql.NullInt64
	)
	err := q.QueryRow(`SELECT `+seriesCols+` FROM series WHERE id = ?`, id).Scan(
		&sr.ID, &freq, &every, &weekdays, &mDay, &mWeek, &mWd, &starts, &until, &maxCount, &tplID, &sr.GeneratedThrough)
	if err != nil {
		return nil, notFound(err)
	}
	r := recur.Rule{Freq: recur.Freq(freq), Every: every, MonthDay: int(mDay.Int64), MonthWeek: int(mWeek.Int64),
		MonthWeekday: time.Weekday(mWd.Int64), Count: int(maxCount.Int64)}
	for _, p := range strings.Split(weekdays, ",") {
		if p = strings.TrimSpace(p); p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("bad weekdays %q", weekdays)
			}
			r.Weekdays = append(r.Weekdays, time.Weekday(n))
		}
	}
	if r.Start, err = recur.ParseDate(starts); err != nil {
		return nil, fmt.Errorf("bad series start %q", starts)
	}
	if until != "" {
		if r.Until, err = recur.ParseDate(until); err != nil {
			return nil, fmt.Errorf("bad series end %q", until)
		}
	}
	sr.Rule = r
	sr.TemplateBookingID = tplID.Int64
	return &sr, nil
}

// GetSeries loads a series with its rule.
func (s *Store) GetSeries(id int64) (*Series, error) { return loadSeries(s.DB, id) }

func insertSeries(q querier, r recur.Rule) (int64, error) {
	wds := make([]int, len(r.Weekdays))
	for i, w := range r.Weekdays {
		wds[i] = int(w)
	}
	sort.Ints(wds)
	parts := make([]string, len(wds))
	for i, w := range wds {
		parts[i] = strconv.Itoa(w)
	}
	var mDay, mWeek, mWd, maxCount any
	if r.Freq == recur.Monthly {
		if r.MonthDay != 0 {
			mDay = r.MonthDay
		} else {
			mWeek, mWd = r.MonthWeek, int(r.MonthWeekday)
		}
	}
	if r.Count > 0 {
		maxCount = r.Count
	}
	until := ""
	if !r.Until.IsZero() {
		until = recur.FormatDate(r.Until)
	}
	res, err := q.Exec(`INSERT INTO series (freq, every, weekdays, month_day, month_week, month_weekday, starts_on, until, max_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(r.Freq), r.Every, strings.Join(parts, ","), mDay, mWeek, mWd, recur.FormatDate(r.Start), until, maxCount)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// shiftDate moves a YYYY-MM-DD date by days; anything else comes back as is.
func shiftDate(d string, days int) string {
	t, err := time.Parse(DateLayout, d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, days).Format(DateLayout)
}

// daysBetween is how many days b is after a.
func daysBetween(a, b string) int {
	ta, err1 := time.Parse(DateLayout, a)
	tb, err2 := time.Parse(DateLayout, b)
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(tb.Sub(ta).Round(24*time.Hour).Hours() / 24)
}

// horizon is the last date repeat dates are made for, given today.
func horizon(today string) (string, error) {
	t, err := time.Parse(DateLayout, today)
	if err != nil {
		return "", errors.New("today isn't a date")
	}
	return t.AddDate(0, SeriesHorizonMonths, 0).Format(DateLayout), nil
}

// templateOf is the booking to copy for a series: its template, else the
// latest remaining date; 0 when none is left.
func templateOf(q querier, sr *Series) (int64, error) {
	if sr.TemplateBookingID != 0 {
		return sr.TemplateBookingID, nil
	}
	var id int64
	err := q.QueryRow(`SELECT id FROM bookings WHERE series_id = ? ORDER BY occurrence_date DESC LIMIT 1`, sr.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// makeOccurrence copies the template booking, with its rooms, to date as a
// date of the series. A multi-day booking keeps its length. A cancelled
// template still makes confirmed dates: cancelling is for one date.
func makeOccurrence(tx *sql.Tx, seriesID, tpl int64, date string) (int64, error) {
	var starts, ends string
	if err := tx.QueryRow(`SELECT starts_on, ends_on FROM bookings WHERE id = ?`, tpl).Scan(&starts, &ends); err != nil {
		return 0, err
	}
	if ends != "" {
		ends = shiftDate(ends, daysBetween(starts, date))
	}
	res, err := tx.Exec(`INSERT INTO bookings (source, series_id, occurrence_date, title, kind, starts_on, start_time, ends_on, end_time,
			visibility, description, url, status, created_by)
		SELECT source, ?, ?, title, kind, ?, start_time, ?, end_time, visibility, description, url,
			CASE status WHEN 'cancelled' THEN 'confirmed' ELSE status END, created_by
		FROM bookings WHERE id = ?`, seriesID, date, date, ends, tpl)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO booking_rooms (booking_id, room_id, room_name)
		SELECT ?, room_id, room_name FROM booking_rooms WHERE booking_id = ?`, id, tpl)
	return id, err
}

func stringSet(q querier, query string, args ...any) (map[string]bool, error) {
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// generate makes the dates the rule gives from the later of floor and the
// day after generated_through, up to the horizon. It skips dates that
// already have a booking (moved ones count, by occurrence date) and skipped
// dates. With no template and no date left it does nothing.
func generate(tx *sql.Tx, seriesID int64, today, floor string) (int, error) {
	sr, err := loadSeries(tx, seriesID)
	if err != nil {
		return 0, err
	}
	hz, err := horizon(today)
	if err != nil {
		return 0, err
	}
	tpl, err := templateOf(tx, sr)
	if err != nil || tpl == 0 {
		return 0, err
	}
	have, err := stringSet(tx, `SELECT occurrence_date FROM bookings WHERE series_id = ?`, seriesID)
	if err != nil {
		return 0, err
	}
	skipped, err := stringSet(tx, `SELECT date FROM series_skips WHERE series_id = ?`, seriesID)
	if err != nil {
		return 0, err
	}
	created := 0
	for _, d := range sr.Rule.Between(sr.Rule.Start, mustDate(hz)) {
		ds := recur.FormatDate(d)
		if ds < floor || ds <= sr.GeneratedThrough || have[ds] || skipped[ds] {
			continue
		}
		if _, err := makeOccurrence(tx, seriesID, tpl, ds); err != nil {
			return created, err
		}
		created++
	}
	if hz > sr.GeneratedThrough {
		if _, err := tx.Exec(`UPDATE series SET generated_through = ? WHERE id = ?`, hz, seriesID); err != nil {
			return created, err
		}
	}
	return created, nil
}

func mustDate(s string) time.Time {
	t, _ := time.Parse(DateLayout, s)
	return t
}

// MakeSeries turns a one-off hand-made booking into a repeat. The booking
// becomes the first date and the template, even if the rule wouldn't fall
// on it; r.Start is replaced by its starts_on (a Count counts the rule's
// dates from there). Dates from today on are then made up to the horizon.
func (s *Store) MakeSeries(bookingID int64, r recur.Rule, today string) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	b, err := getBooking(tx, bookingID)
	if err != nil {
		return 0, err
	}
	if b.SeriesID != 0 {
		return 0, ErrAlreadyInSeries
	}
	if !b.HandMade() {
		return 0, errors.New("only hand-made bookings can repeat")
	}
	if r.Start, err = recur.ParseDate(b.StartsOn); err != nil {
		return 0, errors.New("the booking's start date isn't a date")
	}
	if err := r.Validate(); err != nil {
		return 0, err
	}
	id, err := insertSeries(tx, r)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE bookings SET series_id = ?, occurrence_date = starts_on WHERE id = ?`, id, bookingID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE series SET template_booking_id = ? WHERE id = ?`, bookingID, id); err != nil {
		return 0, err
	}
	if _, err := generate(tx, id, today, today); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ExtendSeries tops up every series to the horizon from today, returning
// how many dates it made. Safe to run as often as you like.
func (s *Store) ExtendSeries(today string) (int, error) {
	rows, err := s.DB.Query(`SELECT id FROM series ORDER BY id`)
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
	total := 0
	for _, id := range ids {
		tx, err := s.DB.Begin()
		if err != nil {
			return total, err
		}
		n, err := generate(tx, id, today, today)
		if err == nil {
			err = tx.Commit()
		}
		tx.Rollback()
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// Occurrences lists a series' bookings by occurrence date.
func (s *Store) Occurrences(seriesID int64) ([]Booking, error) {
	return queryBookings(s.DB, `SELECT `+bookingCols+` FROM bookings WHERE series_id = ? ORDER BY occurrence_date`, seriesID)
}

// DeleteBooking deletes a booking. A date of a repeat is remembered as
// skipped so topping up doesn't bring it back, and no other date changes;
// if it was the template, the next later date becomes it (else the latest
// earlier one).
func (s *Store) DeleteBooking(id int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seriesID sql.NullInt64
	var date string
	if err := tx.QueryRow(`SELECT series_id, occurrence_date FROM bookings WHERE id = ?`, id).Scan(&seriesID, &date); err != nil {
		return notFound(err)
	}
	if seriesID.Valid {
		var tpl sql.NullInt64
		if err := tx.QueryRow(`SELECT template_booking_id FROM series WHERE id = ?`, seriesID.Int64).Scan(&tpl); err != nil {
			return err
		}
		if tpl.Valid && tpl.Int64 == id {
			var next sql.NullInt64
			err := tx.QueryRow(`SELECT COALESCE(
				(SELECT id FROM bookings WHERE series_id = ?1 AND occurrence_date > ?2 ORDER BY occurrence_date LIMIT 1),
				(SELECT id FROM bookings WHERE series_id = ?1 AND occurrence_date < ?2 ORDER BY occurrence_date DESC LIMIT 1))`,
				seriesID.Int64, date).Scan(&next)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE series SET template_booking_id = ? WHERE id = ?`, nullInt(next.Int64), seriesID.Int64); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO series_skips (series_id, date) VALUES (?, ?)`, seriesID.Int64, date); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM bookings WHERE id = ?`, id); err != nil {
		return err
	}
	if seriesID.Valid {
		if err := dropEmptySeries(tx, seriesID.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dropEmptySeries deletes a series with no dates left.
func dropEmptySeries(tx *sql.Tx, seriesID int64) error {
	_, err := tx.Exec(`DELETE FROM series WHERE id = ? AND NOT EXISTS (SELECT 1 FROM bookings WHERE series_id = ?)`, seriesID, seriesID)
	return err
}

// EndSeriesFrom ends the repeat on the day before from and deletes every
// date with occurrence_date >= from (a moved date is judged by the date the
// rule gave it); earlier ones are untouched. A series left with no dates is
// deleted. Returns how many bookings were deleted.
func (s *Store) EndSeriesFrom(seriesID int64, from string) (int, error) {
	if _, err := time.Parse(DateLayout, from); err != nil {
		return 0, errors.New("pick a date")
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	sr, err := loadSeries(tx, seriesID)
	if err != nil {
		return 0, err
	}
	until := shiftDate(from, -1)
	if !sr.Rule.Until.IsZero() && recur.FormatDate(sr.Rule.Until) < until {
		until = recur.FormatDate(sr.Rule.Until) // the earlier end wins
	}
	if _, err := tx.Exec(`UPDATE series SET until = ? WHERE id = ?`, until, seriesID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM bookings WHERE series_id = ? AND occurrence_date >= ?`, seriesID, from)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.Exec(`DELETE FROM series_skips WHERE series_id = ? AND date >= ?`, seriesID, from); err != nil {
		return 0, err
	}
	if err := dropEmptySeries(tx, seriesID); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// BookingChange holds the fields that changed; nil fields are left alone.
// Dates and status are never applied to later dates: they're per date.
type BookingChange struct {
	Title, Kind, StartTime, EndTime, Visibility, Description *string
	Rooms                                                    *[]int64
}

// Empty reports whether nothing changed.
func (c BookingChange) Empty() bool {
	return c.Title == nil && c.Kind == nil && c.StartTime == nil && c.EndTime == nil &&
		c.Visibility == nil && c.Description == nil && c.Rooms == nil
}

// ApplyForward applies the changed fields to this date and every later one
// (occurrence_date >= this one's) and makes this date the series' template.
// Returns how many bookings were updated.
func (s *Store) ApplyForward(bookingID int64, c BookingChange) (int, error) {
	var sets []string
	var args []any
	add := func(col string, p *string) {
		if p != nil {
			sets = append(sets, col+" = ?")
			args = append(args, *p)
		}
	}
	if c.Visibility != nil {
		v := NormalizeVisibility(*c.Visibility)
		c.Visibility = &v
	}
	add("title", c.Title)
	add("kind", c.Kind)
	add("start_time", c.StartTime)
	add("end_time", c.EndTime)
	add("visibility", c.Visibility)
	add("description", c.Description)

	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var seriesID sql.NullInt64
	var date string
	if err := tx.QueryRow(`SELECT series_id, occurrence_date FROM bookings WHERE id = ?`, bookingID).Scan(&seriesID, &date); err != nil {
		return 0, notFound(err)
	}
	if !seriesID.Valid {
		return 0, ErrNotInSeries
	}
	var ids []int64
	rows, err := tx.Query(`SELECT id FROM bookings WHERE series_id = ? AND occurrence_date >= ?`, seriesID.Int64, date)
	if err != nil {
		return 0, err
	}
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
		if len(sets) > 0 {
			if _, err := tx.Exec(`UPDATE bookings SET `+strings.Join(sets, ", ")+`, updated_at = datetime('now') WHERE id = ?`, append(args, id)...); err != nil {
				return 0, err
			}
		}
		if c.Rooms != nil {
			if err := setRooms(tx, id, *c.Rooms); err != nil {
				return 0, err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE series SET template_booking_id = ? WHERE id = ?`, bookingID, seriesID.Int64); err != nil {
		return 0, err
	}
	return len(ids), tx.Commit()
}
