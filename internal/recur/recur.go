// Package recur works out the dates of a repeating booking: every N
// days, weeks or months, optionally limited by an end date or a number of
// occurrences.
//
// All dates are date-only values at 00:00 UTC. Weeks run Sunday to Saturday.
package recur

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"

// Freq is how often a rule repeats.
type Freq string

const (
	Daily   Freq = "daily"
	Weekly  Freq = "weekly"
	Monthly Freq = "monthly"
)

// Rule describes a repeating event.
type Rule struct {
	Freq     Freq
	Every    int            // every N days/weeks/months, >= 1
	Weekdays []time.Weekday // Weekly: which days; empty = Start's weekday

	// Monthly by date: MonthDay 1..31. A month without that day uses its
	// last day. Zero when the rule is by weekday.
	MonthDay int
	// Monthly by weekday: MonthWeek 1..4, or -1 for the last such weekday.
	// Zero when the rule is by date.
	MonthWeek    int
	MonthWeekday time.Weekday // used with MonthWeek

	Start time.Time // anchor: first possible date; base for "every N"
	Until time.Time // zero = no end; otherwise last date allowed (inclusive)
	Count int       // 0 = no limit; otherwise only the first Count occurrences from Start
}

// ParseDate parses a "2006-01-02" date into a 00:00 UTC time.
func ParseDate(s string) (time.Time, error) {
	return time.Parse(dateLayout, s)
}

// FormatDate formats the calendar date of t as "2006-01-02".
func FormatDate(t time.Time) string {
	return day(t).Format(dateLayout)
}

// day returns the calendar date of t as a 00:00 UTC time.
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Validate reports whether the rule is well formed.
func (r Rule) Validate() error {
	if r.Start.IsZero() {
		return errors.New("start date is required")
	}
	if r.Every < 1 {
		return errors.New("every must be 1 or more")
	}
	if r.Count < 0 {
		return errors.New("count cannot be negative")
	}
	if !r.Until.IsZero() && day(r.Until).Before(day(r.Start)) {
		return errors.New("until must be on or after start")
	}

	switch r.Freq {
	case Daily:
		if len(r.Weekdays) > 0 {
			return errors.New("daily rules cannot set weekdays")
		}
		return r.checkNoMonthFields()
	case Weekly:
		if err := r.checkNoMonthFields(); err != nil {
			return err
		}
		return validateWeekdays(r.Weekdays)
	case Monthly:
		return r.validateMonthly()
	default:
		return fmt.Errorf("unknown frequency %q", string(r.Freq))
	}
}

// checkNoMonthFields rejects month settings on a daily or weekly rule. A
// MonthWeekday of Sunday is the zero value and cannot be detected here.
func (r Rule) checkNoMonthFields() error {
	if r.MonthDay != 0 || r.MonthWeek != 0 || r.MonthWeekday != 0 {
		return fmt.Errorf("%s rules cannot set month fields", r.Freq)
	}
	return nil
}

func (r Rule) validateMonthly() error {
	if len(r.Weekdays) > 0 {
		return errors.New("monthly rules cannot set weekdays")
	}
	byDate := r.MonthDay != 0
	byWeekday := r.MonthWeek != 0
	switch {
	case byDate && byWeekday:
		return errors.New("choose a day of the month or a week and weekday, not both")
	case byDate:
		if r.MonthDay < 1 || r.MonthDay > 31 {
			return errors.New("day of the month must be 1 to 31")
		}
		if r.MonthWeekday != 0 {
			return errors.New("a weekday needs a week number, not a day of the month")
		}
	case byWeekday:
		if r.MonthWeek != -1 && (r.MonthWeek < 1 || r.MonthWeek > 4) {
			return errors.New("week must be 1 to 4, or -1 for the last")
		}
		if r.MonthWeekday < time.Sunday || r.MonthWeekday > time.Saturday {
			return errors.New("weekday must be Sunday to Saturday")
		}
	default:
		return errors.New("monthly rules need a day of the month or a week and weekday")
	}
	return nil
}

func validateWeekdays(ws []time.Weekday) error {
	seen := make(map[time.Weekday]bool, len(ws))
	for _, w := range ws {
		if w < time.Sunday || w > time.Saturday {
			return errors.New("weekdays must be Sunday to Saturday")
		}
		if seen[w] {
			return fmt.Errorf("%s is listed more than once", w)
		}
		seen[w] = true
	}
	return nil
}

// Between returns every occurrence d with from <= d <= to, ascending.
// It returns nil if the rule is invalid or to is before from.
func (r Rule) Between(from, to time.Time) []time.Time {
	if r.Validate() != nil {
		return nil
	}
	from, to = day(from), day(to)
	if to.Before(from) {
		return nil
	}
	var out []time.Time
	r.walk(func(d time.Time) bool {
		if d.After(to) {
			return false
		}
		if !d.Before(from) {
			out = append(out, d)
		}
		return true
	})
	return out
}

// Next returns the first occurrence strictly after the given date.
// It returns false if there is none (or the rule is invalid).
func (r Rule) Next(after time.Time) (time.Time, bool) {
	if r.Validate() != nil {
		return time.Time{}, false
	}
	after = day(after)
	var found time.Time
	ok := false
	r.walk(func(d time.Time) bool {
		if d.After(after) {
			found, ok = d, true
			return false
		}
		return true
	})
	return found, ok
}

// walk yields occurrences in ascending order from Start, honouring Until and
// Count. It stops when yield returns false.
func (r Rule) walk(yield func(time.Time) bool) {
	var until time.Time
	if !r.Until.IsZero() {
		until = day(r.Until)
	}
	n := 0
	r.gen(func(d time.Time) bool {
		if !until.IsZero() && d.After(until) {
			return false
		}
		if r.Count > 0 && n >= r.Count {
			return false
		}
		n++
		return yield(d)
	})
}

// gen yields every occurrence from Start in ascending order, ignoring Until
// and Count. It only returns when yield returns false, so the caller must
// stop it. The rule must already be valid.
func (r Rule) gen(yield func(time.Time) bool) {
	start := day(r.Start)
	switch r.Freq {
	case Daily:
		for i := 0; ; i++ {
			if !yield(start.AddDate(0, 0, i*r.Every)) {
				return
			}
		}
	case Weekly:
		wds := r.sortedWeekdays()
		sunday := start.AddDate(0, 0, -int(start.Weekday()))
		for k := 0; ; k++ {
			week := sunday.AddDate(0, 0, 7*k*r.Every)
			for _, wd := range wds {
				d := week.AddDate(0, 0, int(wd))
				if d.Before(start) {
					continue
				}
				if !yield(d) {
					return
				}
			}
		}
	case Monthly:
		first := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
		for k := 0; ; k++ {
			m := first.AddDate(0, k*r.Every, 0)
			d := r.monthDate(m.Year(), m.Month())
			if d.Before(start) {
				continue
			}
			if !yield(d) {
				return
			}
		}
	}
}

// sortedWeekdays returns the weekly days in Sunday..Saturday order, or the
// Start's weekday when none are listed.
func (r Rule) sortedWeekdays() []time.Weekday {
	if len(r.Weekdays) == 0 {
		return []time.Weekday{day(r.Start).Weekday()}
	}
	ws := append([]time.Weekday(nil), r.Weekdays...)
	sort.Slice(ws, func(i, j int) bool { return ws[i] < ws[j] })
	return ws
}

// monthDate returns the occurrence within the given month.
func (r Rule) monthDate(y int, m time.Month) time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC)
	if r.MonthDay > 0 {
		d := r.MonthDay
		if d > last.Day() {
			d = last.Day()
		}
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	if r.MonthWeek > 0 {
		first := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
		off := (int(r.MonthWeekday) - int(first.Weekday()) + 7) % 7
		return first.AddDate(0, 0, off+7*(r.MonthWeek-1))
	}
	off := (int(last.Weekday()) - int(r.MonthWeekday) + 7) % 7
	return last.AddDate(0, 0, -off)
}

// Describe returns a plain-English description of the rule.
func (r Rule) Describe() string {
	if err := r.Validate(); err != nil {
		return "Invalid repeat rule: " + err.Error()
	}
	var s string
	switch r.Freq {
	case Daily:
		if r.Every == 1 {
			s = "Every day"
		} else {
			s = fmt.Sprintf("Every %d days", r.Every)
		}
	case Weekly:
		names := make([]string, 0, 7)
		for _, wd := range r.sortedWeekdays() {
			names = append(names, wd.String())
		}
		on := joinNames(names)
		if r.Every == 1 {
			s = "Every week on " + on
		} else {
			s = fmt.Sprintf("Every %d weeks on %s", r.Every, on)
		}
	case Monthly:
		var when string
		if r.MonthDay > 0 {
			when = "the " + ordinal(r.MonthDay)
		} else {
			week := "last"
			if r.MonthWeek != -1 {
				week = ordinal(r.MonthWeek)
			}
			when = "the " + week + " " + r.MonthWeekday.String()
		}
		if r.Every == 1 {
			s = "Every month on " + when
		} else {
			s = fmt.Sprintf("Every %d months on %s", r.Every, when)
		}
	}
	if !r.Until.IsZero() {
		u := day(r.Until)
		s += fmt.Sprintf(", until %d %s %d", u.Day(), u.Month(), u.Year())
	}
	switch {
	case r.Count == 1:
		s += ", once"
	case r.Count > 1:
		s += fmt.Sprintf(", %d times", r.Count)
	}
	return s
}

// joinNames joins ["A", "B", "C"] as "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " and " + names[last]
}

// ordinal returns n with its English suffix, e.g. 1st, 12th, 23rd.
func ordinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}
