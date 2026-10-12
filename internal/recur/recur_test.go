package recur

import (
	"strings"
	"testing"
	"time"
)

// mustDate parses a test date, panicking on bad input.
func mustDate(s string) time.Time {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// join formats dates as space-separated "2006-01-02" strings for comparison.
func join(ts []time.Time) string {
	s := make([]string, 0, len(ts))
	for _, t := range ts {
		s = append(s, FormatDate(t))
	}
	return strings.Join(s, " ")
}

func TestParseFormat(t *testing.T) {
	d, err := ParseDate("2027-03-01")
	if err != nil {
		t.Fatalf("ParseDate: %v", err)
	}
	if d.Location() != time.UTC || d.Hour() != 0 {
		t.Errorf("ParseDate gave %v, want 00:00 UTC", d)
	}
	if got := FormatDate(d); got != "2027-03-01" {
		t.Errorf("FormatDate = %q", got)
	}
	if _, err := ParseDate("01/03/2027"); err == nil {
		t.Error("ParseDate accepted a bad layout")
	}
}

func TestBetween(t *testing.T) {
	tests := []struct {
		name     string
		rule     Rule
		from, to string
		want     string
	}{
		{
			name: "daily every day",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11")},
			from: "2026-10-11", to: "2026-10-13",
			want: "2026-10-11 2026-10-12 2026-10-13",
		},
		{
			name: "daily every 3",
			rule: Rule{Freq: Daily, Every: 3, Start: mustDate("2026-10-11")},
			from: "2026-10-01", to: "2026-10-20",
			want: "2026-10-11 2026-10-14 2026-10-17 2026-10-20",
		},
		{
			name: "daily range starts after Start",
			rule: Rule{Freq: Daily, Every: 3, Start: mustDate("2026-10-11")},
			from: "2026-10-12", to: "2026-10-18",
			want: "2026-10-14 2026-10-17",
		},
		{
			name: "daily range before Start is empty",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11")},
			from: "2026-10-01", to: "2026-10-10",
			want: "",
		},
		{
			name: "to before from is empty",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11")},
			from: "2026-10-20", to: "2026-10-10",
			want: "",
		},
		{
			name: "weekly every week on Wednesday, Start Thursday",
			rule: Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday}, Start: mustDate("2026-10-15")},
			from: "2026-10-01", to: "2026-11-04",
			want: "2026-10-21 2026-10-28 2026-11-04",
		},
		{
			name: "weekly no weekdays uses Start weekday",
			rule: Rule{Freq: Weekly, Every: 1, Start: mustDate("2026-10-15")},
			from: "2026-10-01", to: "2026-10-31",
			want: "2026-10-15 2026-10-22 2026-10-29",
		},
		{
			name: "weekly every 2 weeks Wed+Sat, Start Thursday",
			rule: Rule{Freq: Weekly, Every: 2, Weekdays: []time.Weekday{time.Saturday, time.Wednesday}, Start: mustDate("2026-10-15")},
			from: "2026-10-01", to: "2026-11-30",
			want: "2026-10-17 2026-10-28 2026-10-31 2026-11-11 2026-11-14 2026-11-25 2026-11-28",
		},
		{
			name: "weekly Mon Wed Fri, Start Sunday",
			rule: Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Friday, time.Monday, time.Wednesday}, Start: mustDate("2026-10-11")},
			from: "2026-10-11", to: "2026-10-18",
			want: "2026-10-12 2026-10-14 2026-10-16",
		},
		{
			name: "monthly on the 15th",
			rule: Rule{Freq: Monthly, Every: 1, MonthDay: 15, Start: mustDate("2026-10-11")},
			from: "2026-10-01", to: "2026-12-31",
			want: "2026-10-15 2026-11-15 2026-12-15",
		},
		{
			name: "monthly every 3 on the 1st, Start after month-0 date",
			rule: Rule{Freq: Monthly, Every: 3, MonthDay: 1, Start: mustDate("2026-01-15")},
			from: "2026-01-01", to: "2026-12-31",
			want: "2026-04-01 2026-07-01 2026-10-01",
		},
		{
			name: "monthly 31st clamps in non-leap February",
			rule: Rule{Freq: Monthly, Every: 1, MonthDay: 31, Start: mustDate("2027-01-31")},
			from: "2027-01-01", to: "2027-04-30",
			want: "2027-01-31 2027-02-28 2027-03-31 2027-04-30",
		},
		{
			name: "monthly 31st clamps in leap February",
			rule: Rule{Freq: Monthly, Every: 1, MonthDay: 31, Start: mustDate("2028-01-31")},
			from: "2028-01-01", to: "2028-03-31",
			want: "2028-01-31 2028-02-29 2028-03-31",
		},
		{
			name: "monthly 31st clamps in 30-day months",
			rule: Rule{Freq: Monthly, Every: 1, MonthDay: 31, Start: mustDate("2026-03-31")},
			from: "2026-03-01", to: "2026-07-31",
			want: "2026-03-31 2026-04-30 2026-05-31 2026-06-30 2026-07-31",
		},
		{
			name: "monthly every 2 first Monday",
			rule: Rule{Freq: Monthly, Every: 2, MonthWeek: 1, MonthWeekday: time.Monday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-05-31",
			want: "2026-01-05 2026-03-02 2026-05-04",
		},
		{
			name: "monthly 1st Monday",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: 1, MonthWeekday: time.Monday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-03-31",
			want: "2026-01-05 2026-02-02 2026-03-02",
		},
		{
			name: "monthly 2nd Sunday",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: 2, MonthWeekday: time.Sunday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-03-31",
			want: "2026-01-11 2026-02-08 2026-03-08",
		},
		{
			name: "monthly 3rd Wednesday",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: 3, MonthWeekday: time.Wednesday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-03-31",
			want: "2026-01-21 2026-02-18 2026-03-18",
		},
		{
			name: "monthly 4th Thursday in a month with five Thursdays",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: 4, MonthWeekday: time.Thursday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-03-31",
			want: "2026-01-22 2026-02-26 2026-03-26",
		},
		{
			name: "monthly last Thursday",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: -1, MonthWeekday: time.Thursday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-03-31",
			want: "2026-01-29 2026-02-26 2026-03-26",
		},
		{
			name: "monthly last Friday",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: -1, MonthWeekday: time.Friday, Start: mustDate("2026-01-01")},
			from: "2026-01-01", to: "2026-03-31",
			want: "2026-01-30 2026-02-27 2026-03-27",
		},
		{
			name: "monthly weekday skips month-0 date before Start",
			rule: Rule{Freq: Monthly, Every: 1, MonthWeek: 3, MonthWeekday: time.Wednesday, Start: mustDate("2026-01-22")},
			from: "2026-01-01", to: "2026-02-28",
			want: "2026-02-18",
		},
		{
			name: "until is inclusive",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2027-02-27"), Until: mustDate("2027-03-01")},
			from: "2027-02-01", to: "2027-03-31",
			want: "2027-02-27 2027-02-28 2027-03-01",
		},
		{
			name: "count limits from Start",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11"), Count: 3},
			from: "2026-10-01", to: "2026-10-31",
			want: "2026-10-11 2026-10-12 2026-10-13",
		},
		{
			name: "count with from later than Start still counts from Start",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11"), Count: 3},
			from: "2026-10-13", to: "2026-10-31",
			want: "2026-10-13",
		},
		{
			name: "count exhausted before from",
			rule: Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11"), Count: 3},
			from: "2026-10-14", to: "2026-10-31",
			want: "",
		},
		{
			name: "monthly count with from later than Start",
			rule: Rule{Freq: Monthly, Every: 1, MonthDay: 15, Start: mustDate("2026-10-11"), Count: 2},
			from: "2026-01-01", to: "2026-12-31",
			want: "2026-10-15 2026-11-15",
		},
		{
			name: "invalid rule gives nothing",
			rule: Rule{},
			from: "2026-01-01", to: "2026-12-31",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := join(tt.rule.Between(mustDate(tt.from), mustDate(tt.to)))
			if got != tt.want {
				t.Errorf("Between(%s, %s) =\n  %q\nwant\n  %q", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestWeeklyThreeYears(t *testing.T) {
	r := Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Monday}, Start: mustDate("2026-01-05")}
	got := r.Between(mustDate("2026-01-01"), mustDate("2028-12-31"))
	if len(got) != 156 {
		t.Fatalf("got %d Mondays over three years, want 156", len(got))
	}
	if first := FormatDate(got[0]); first != "2026-01-05" {
		t.Errorf("first = %s, want 2026-01-05", first)
	}
	if last := FormatDate(got[len(got)-1]); last != "2028-12-25" {
		t.Errorf("last = %s, want 2028-12-25", last)
	}
}

func TestNext(t *testing.T) {
	tests := []struct {
		name  string
		rule  Rule
		after string
		want  string
		ok    bool
	}{
		{
			name:  "weekly Wed, before Start",
			rule:  Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday}, Start: mustDate("2026-10-15")},
			after: "2026-10-01", want: "2026-10-21", ok: true,
		},
		{
			name:  "weekly Wed, strictly after",
			rule:  Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday}, Start: mustDate("2026-10-15")},
			after: "2026-10-21", want: "2026-10-28", ok: true,
		},
		{
			name:  "weekly Wed, mid-gap",
			rule:  Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday}, Start: mustDate("2026-10-15")},
			after: "2026-10-20", want: "2026-10-21", ok: true,
		},
		{
			name:  "monthly 31st, Feb clamp",
			rule:  Rule{Freq: Monthly, Every: 1, MonthDay: 31, Start: mustDate("2027-01-31")},
			after: "2027-01-31", want: "2027-02-28", ok: true,
		},
		{
			name:  "count exhausted",
			rule:  Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11"), Count: 2},
			after: "2026-10-12", want: "", ok: false,
		},
		{
			name:  "count not yet exhausted",
			rule:  Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11"), Count: 2},
			after: "2026-10-11", want: "2026-10-12", ok: true,
		},
		{
			name:  "until passed",
			rule:  Rule{Freq: Daily, Every: 1, Start: mustDate("2026-10-11"), Until: mustDate("2026-10-13")},
			after: "2026-10-13", want: "", ok: false,
		},
		{
			name:  "invalid rule",
			rule:  Rule{Freq: Daily},
			after: "2026-10-13", want: "", ok: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.rule.Next(mustDate(tt.after))
			if ok != tt.ok {
				t.Fatalf("Next ok = %v, want %v", ok, tt.ok)
			}
			if ok && FormatDate(got) != tt.want {
				t.Errorf("Next = %s, want %s", FormatDate(got), tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	start := mustDate("2026-10-11")
	wed := []time.Weekday{time.Wednesday}
	tests := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{"daily ok", Rule{Freq: Daily, Every: 1, Start: start}, false},
		{"weekly ok", Rule{Freq: Weekly, Every: 2, Weekdays: []time.Weekday{time.Wednesday, time.Saturday}, Start: start}, false},
		{"weekly no weekdays ok", Rule{Freq: Weekly, Every: 1, Start: start}, false},
		{"monthly by date ok", Rule{Freq: Monthly, Every: 1, MonthDay: 31, Start: start}, false},
		{"monthly by weekday ok", Rule{Freq: Monthly, Every: 1, MonthWeek: -1, MonthWeekday: time.Friday, Start: start}, false},
		{"until equal to start ok", Rule{Freq: Daily, Every: 1, Start: start, Until: start}, false},
		{"zero start", Rule{Freq: Daily, Every: 1}, true},
		{"every zero", Rule{Freq: Daily, Every: 0, Start: start}, true},
		{"every negative", Rule{Freq: Daily, Every: -1, Start: start}, true},
		{"negative count", Rule{Freq: Daily, Every: 1, Start: start, Count: -1}, true},
		{"until before start", Rule{Freq: Daily, Every: 1, Start: start, Until: mustDate("2026-10-10")}, true},
		{"unknown freq", Rule{Freq: "hourly", Every: 1, Start: start}, true},
		{"daily with weekdays", Rule{Freq: Daily, Every: 1, Weekdays: wed, Start: start}, true},
		{"daily with month day", Rule{Freq: Daily, Every: 1, MonthDay: 3, Start: start}, true},
		{"daily with month weekday", Rule{Freq: Daily, Every: 1, MonthWeekday: time.Friday, Start: start}, true},
		{"weekly with month day", Rule{Freq: Weekly, Every: 1, MonthDay: 3, Start: start}, true},
		{"weekly with month week", Rule{Freq: Weekly, Every: 1, MonthWeek: 1, Start: start}, true},
		{"weekly weekday out of range", Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{7}, Start: start}, true},
		{"weekly duplicate weekday", Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Monday, time.Monday}, Start: start}, true},
		{"monthly with nothing set", Rule{Freq: Monthly, Every: 1, Start: start}, true},
		{"monthly with both modes", Rule{Freq: Monthly, Every: 1, MonthDay: 3, MonthWeek: 1, MonthWeekday: time.Monday, Start: start}, true},
		{"monthly day 0 with weekday mode off", Rule{Freq: Monthly, Every: 1, MonthWeekday: time.Monday, Start: start}, true},
		{"monthly day 32", Rule{Freq: Monthly, Every: 1, MonthDay: 32, Start: start}, true},
		{"monthly by date with weekday set", Rule{Freq: Monthly, Every: 1, MonthDay: 3, MonthWeekday: time.Friday, Start: start}, true},
		{"monthly week 5", Rule{Freq: Monthly, Every: 1, MonthWeek: 5, MonthWeekday: time.Monday, Start: start}, true},
		{"monthly week -2", Rule{Freq: Monthly, Every: 1, MonthWeek: -2, MonthWeekday: time.Monday, Start: start}, true},
		{"monthly weekday out of range", Rule{Freq: Monthly, Every: 1, MonthWeek: 1, MonthWeekday: 9, Start: start}, true},
		{"monthly with weekdays", Rule{Freq: Monthly, Every: 1, MonthDay: 3, Weekdays: wed, Start: start}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && (tt.rule.Between(start, start.AddDate(1, 0, 0)) != nil || func() bool { _, ok := tt.rule.Next(start); return ok }()) {
				t.Error("invalid rule should yield no dates")
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	start := mustDate("2026-10-11")
	until := mustDate("2027-03-01")
	tests := []struct {
		name string
		rule Rule
		want string
	}{
		{"daily every 1", Rule{Freq: Daily, Every: 1, Start: start}, "Every day"},
		{"daily every 3", Rule{Freq: Daily, Every: 3, Start: start}, "Every 3 days"},
		{"weekly Wed", Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Wednesday}, Start: start}, "Every week on Wednesday"},
		{"weekly every 2 Wed+Sat", Rule{Freq: Weekly, Every: 2, Weekdays: []time.Weekday{time.Saturday, time.Wednesday}, Start: start}, "Every 2 weeks on Wednesday and Saturday"},
		{"weekly three days in week order", Rule{Freq: Weekly, Every: 1, Weekdays: []time.Weekday{time.Friday, time.Monday, time.Wednesday}, Start: start}, "Every week on Monday, Wednesday and Friday"},
		{"weekly no weekdays uses Start", Rule{Freq: Weekly, Every: 1, Start: mustDate("2026-10-15")}, "Every week on Thursday"},
		{"monthly 15th", Rule{Freq: Monthly, Every: 1, MonthDay: 15, Start: start}, "Every month on the 15th"},
		{"monthly every 3 on 1st", Rule{Freq: Monthly, Every: 3, MonthDay: 1, Start: start}, "Every 3 months on the 1st"},
		{"monthly 2nd Sunday", Rule{Freq: Monthly, Every: 1, MonthWeek: 2, MonthWeekday: time.Sunday, Start: start}, "Every month on the 2nd Sunday"},
		{"monthly last Friday", Rule{Freq: Monthly, Every: 1, MonthWeek: -1, MonthWeekday: time.Friday, Start: start}, "Every month on the last Friday"},
		{"monthly every 2 on 3rd Tuesday", Rule{Freq: Monthly, Every: 2, MonthWeek: 3, MonthWeekday: time.Tuesday, Start: start}, "Every 2 months on the 3rd Tuesday"},
		{"until suffix", Rule{Freq: Daily, Every: 1, Start: start, Until: until}, "Every day, until 1 March 2027"},
		{"count suffix", Rule{Freq: Daily, Every: 1, Start: start, Count: 10}, "Every day, 10 times"},
		{"count 1 is once", Rule{Freq: Daily, Every: 1, Start: start, Count: 1}, "Every day, once"},
		{"until then count", Rule{Freq: Daily, Every: 1, Start: start, Until: until, Count: 10}, "Every day, until 1 March 2027, 10 times"},
		{"ordinal 1st", Rule{Freq: Monthly, Every: 1, MonthDay: 1, Start: start}, "Every month on the 1st"},
		{"ordinal 2nd", Rule{Freq: Monthly, Every: 1, MonthDay: 2, Start: start}, "Every month on the 2nd"},
		{"ordinal 3rd", Rule{Freq: Monthly, Every: 1, MonthDay: 3, Start: start}, "Every month on the 3rd"},
		{"ordinal 4th", Rule{Freq: Monthly, Every: 1, MonthDay: 4, Start: start}, "Every month on the 4th"},
		{"ordinal 11th", Rule{Freq: Monthly, Every: 1, MonthDay: 11, Start: start}, "Every month on the 11th"},
		{"ordinal 12th", Rule{Freq: Monthly, Every: 1, MonthDay: 12, Start: start}, "Every month on the 12th"},
		{"ordinal 13th", Rule{Freq: Monthly, Every: 1, MonthDay: 13, Start: start}, "Every month on the 13th"},
		{"ordinal 21st", Rule{Freq: Monthly, Every: 1, MonthDay: 21, Start: start}, "Every month on the 21st"},
		{"ordinal 22nd", Rule{Freq: Monthly, Every: 1, MonthDay: 22, Start: start}, "Every month on the 22nd"},
		{"ordinal 23rd", Rule{Freq: Monthly, Every: 1, MonthDay: 23, Start: start}, "Every month on the 23rd"},
		{"ordinal 31st", Rule{Freq: Monthly, Every: 1, MonthDay: 31, Start: start}, "Every month on the 31st"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rule.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}
