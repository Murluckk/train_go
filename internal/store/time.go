package store

import "time"

// Times are stored as RFC3339 with a nanosecond fraction and an explicit
// offset. Dates are stored as local calendar days: "today" has to mean today
// where the user is sitting, not in UTC.
const (
	timeLayout = time.RFC3339Nano
	dayLayout  = "2006-01-02"
)

func formatTime(t time.Time) string { return t.Format(timeLayout) }

func formatDay(t time.Time) string { return t.Format(dayLayout) }

func parseTime(s string) (time.Time, error) {
	return time.Parse(timeLayout, s)
}

// Day is a local calendar date in YYYY-MM-DD form.
type Day = string

// DayOf returns the local calendar date of t.
func DayOf(t time.Time) Day { return formatDay(t) }

// AddDays shifts a calendar date by n days, staying in the location of base.
func AddDays(base time.Time, n int) Day {
	return formatDay(base.AddDate(0, 0, n))
}

func parseDay(s string) (time.Time, error) {
	return time.ParseInLocation(dayLayout, s, time.Local)
}
