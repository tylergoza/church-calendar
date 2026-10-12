package server

import (
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/tylergoza/church-calendar/internal/recur"
	"github.com/tylergoza/church-calendar/internal/store"
)

// dayOpt is one weekday in the repeat fields: a checkbox for weekly rules
// and an option for "the 2nd Sunday".
type dayOpt struct {
	Value    int
	Name     string
	Short    string
	Checked  bool
	Selected bool
}

type weekOpt struct {
	Value    int
	Label    string
	Selected bool
}

// repeatForm is the "Repeats" fields of the booking form.
type repeatForm struct {
	Freq      string // none | daily | weekly | monthly
	Every     int
	Days      []dayOpt
	MonthMode string // day | weekday
	MonthDay  int
	MonthWeek int // 1..4 or -1
	Weeks     []weekOpt
	End       string // never | on | after
	Until     string
	Count     int
	NoNever   bool // hide "Never": the booking already repeats
}

// startDate parses a booking date, falling back to today.
func startDate(start string) time.Time {
	if t, err := recur.ParseDate(start); err == nil {
		return t
	}
	t, _ := recur.ParseDate(time.Now().Format(store.DateLayout))
	return t
}

// weekOfMonth is which weekday-of-the-month d is: 1..4, or -1 for the 5th
// (which is always the last).
func weekOfMonth(d time.Time) int {
	w := (d.Day()-1)/7 + 1
	if w > 4 {
		return -1
	}
	return w
}

func (f *repeatForm) build(weekdays []time.Weekday, monthWeekday time.Weekday) {
	f.Days = f.Days[:0]
	for i := 0; i < 7; i++ {
		wd := time.Weekday(i)
		f.Days = append(f.Days, dayOpt{Value: i, Name: wd.String(), Short: wd.String()[:3],
			Checked: slices.Contains(weekdays, wd), Selected: wd == monthWeekday})
	}
	f.Weeks = nil
	for _, w := range []struct {
		v int
		l string
	}{{1, "1st"}, {2, "2nd"}, {3, "3rd"}, {4, "4th"}, {-1, "last"}} {
		f.Weeks = append(f.Weeks, weekOpt{Value: w.v, Label: w.l, Selected: w.v == f.MonthWeek})
	}
}

// newRepeat is the form for "doesn't repeat yet", defaulted from the start date.
func newRepeat(start string) repeatForm {
	d := startDate(start)
	f := repeatForm{Freq: "none", Every: 1, MonthMode: "day", MonthDay: d.Day(), MonthWeek: weekOfMonth(d), End: "never", Count: 10}
	f.build([]time.Weekday{d.Weekday()}, d.Weekday())
	return f
}

// repeatFromRule fills the form from an existing rule.
func repeatFromRule(r recur.Rule, start string) repeatForm {
	f := newRepeat(start)
	f.Freq, f.Every = string(r.Freq), r.Every
	wds := r.Weekdays
	if len(wds) == 0 {
		wds = []time.Weekday{startDate(start).Weekday()}
	}
	mwd := startDate(start).Weekday()
	if r.Freq == recur.Monthly {
		if r.MonthDay > 0 {
			f.MonthDay = r.MonthDay
		} else {
			f.MonthMode, f.MonthWeek, mwd = "weekday", r.MonthWeek, r.MonthWeekday
		}
	}
	switch {
	case !r.Until.IsZero():
		f.End, f.Until = "on", recur.FormatDate(r.Until)
	case r.Count > 0:
		f.End, f.Count = "after", r.Count
	}
	f.build(wds, mwd)
	return f
}

// readRepeat reads the repeat fields. start is the date the rule starts
// from. set is false when the booking doesn't repeat (the other fields are
// then ignored, as are the ones that don't apply to the chosen frequency).
func readRepeat(r *http.Request, start string) (f repeatForm, rule recur.Rule, set bool, errs []string) {
	f = newRepeat(start)
	switch v := formStr(r, "repeat_freq"); v {
	case "daily", "weekly", "monthly":
		f.Freq = v
	default:
		return f, rule, false, nil
	}
	sd, err := recur.ParseDate(start)
	if err != nil {
		return f, rule, true, []string{"Pick the day it starts before choosing how it repeats."}
	}
	bad := func(msg string) { errs = append(errs, msg) }

	f.Every = 1
	if n, err := strconv.Atoi(formStr(r, "repeat_every")); err != nil || n < 1 || n > 999 {
		bad("Repeat every: use a whole number from 1 to 999.")
		f.Every = 1
	} else {
		f.Every = n
	}
	rule = recur.Rule{Freq: recur.Freq(f.Freq), Every: f.Every, Start: sd}

	var weekdays []time.Weekday
	for _, v := range r.PostForm["repeat_weekday"] {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 6 && !slices.Contains(weekdays, time.Weekday(n)) {
			weekdays = append(weekdays, time.Weekday(n))
		}
	}
	if len(weekdays) == 0 {
		weekdays = []time.Weekday{sd.Weekday()}
	}
	mwd := sd.Weekday()
	switch f.Freq {
	case "weekly":
		rule.Weekdays = weekdays
	case "monthly":
		if formStr(r, "repeat_month_mode") == "weekday" {
			f.MonthMode = "weekday"
			w, err := strconv.Atoi(formStr(r, "repeat_month_week"))
			if err != nil || !(w == -1 || (w >= 1 && w <= 4)) {
				bad("Pick which week of the month.")
			} else {
				f.MonthWeek = w
			}
			if n, err := strconv.Atoi(formStr(r, "repeat_month_weekday")); err != nil || n < 0 || n > 6 {
				bad("Pick a day of the week.")
			} else {
				mwd = time.Weekday(n)
			}
			rule.MonthWeek, rule.MonthWeekday = f.MonthWeek, mwd
		} else {
			f.MonthMode = "day"
			if n, err := strconv.Atoi(formStr(r, "repeat_month_day")); err != nil || n < 1 || n > 31 {
				bad("Day of the month: use a number from 1 to 31.")
			} else {
				f.MonthDay = n
			}
			rule.MonthDay = f.MonthDay
		}
	}

	switch formStr(r, "repeat_end") {
	case "on":
		f.End, f.Until = "on", formStr(r, "repeat_until")
		if u, err := recur.ParseDate(f.Until); err != nil {
			bad("Pick the date the repeats stop.")
		} else {
			rule.Until = u
		}
	case "after":
		f.End = "after"
		if n, err := strconv.Atoi(formStr(r, "repeat_count")); err != nil || n < 1 || n > 1000 {
			bad("Number of times: use a whole number from 1 to 1000.")
		} else {
			f.Count, rule.Count = n, n
		}
	}
	f.build(weekdays, mwd)
	if len(errs) == 0 {
		if err := rule.Validate(); err != nil {
			bad("Repeat: " + err.Error() + ".")
		}
	}
	return f, rule, true, errs
}

// repeatSummary is "Repeats: <Describe>".
func repeatSummary(rule recur.Rule) string { return "Repeats: " + rule.Describe() }
