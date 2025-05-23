package datetime

import (
	"fmt"
	"regexp"
	"time"
)

var (
	reISODate     = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)
	reUSDate      = regexp.MustCompile(`\b(\d{1,2})/(\d{1,2})/(\d{4})\b`)
	reMonthDay    = regexp.MustCompile(`\b(January|February|March|…)\s+(\d{1,2}),\s*(\d{4})\b`)
	reNextWeekday = regexp.MustCompile(`\bnext\s+(Monday|Tuesday|…)\b`)
)

func NormalizeDates(text string) []string {
	var out []string
	// ISO
	for _, m := range reISODate.FindAllStringSubmatch(text, -1) {
		out = append(out, m[0])
	}
	// US -> ISO
	for _, m := range reUSDate.FindAllStringSubmatch(text, -1) {
		month, day, year := m[1], m[2], m[3]
		out = append(out, year+"-"+pad(month)+"-"+pad(day))
	}
	// Month Day, Year -> ISO
	for _, m := range reMonthDay.FindAllStringSubmatch(text, -1) {
		t, err := time.Parse("January 2, 2006", m[0])
		if err == nil {
			out = append(out, t.Format("2006-01-02"))
		}
	}
	// next Weekday -> compute
	for _, m := range reNextWeekday.FindAllStringSubmatch(text, -1) {
		if d, err := parseNextWeekday(m[1]); err == nil {
			out = append(out, d.Format("2006-01-02"))
		}
	}
	return out
}

// pad "M" -> "MM"
func pad(s string) string {
	if len(s) == 1 {
		return "0" + s
	}
	return s
}

// parseNextWeekday returns the date of the next named weekday.
func parseNextWeekday(dayName string) (time.Time, error) {
	// Map weekday names to time.Weekday
	names := map[string]time.Weekday{
		"Sunday":    time.Sunday,
		"Monday":    time.Monday,
		"Tuesday":   time.Tuesday,
		"Wednesday": time.Wednesday,
		"Thursday":  time.Thursday,
		"Friday":    time.Friday,
		"Saturday":  time.Saturday,
	}
	target, ok := names[dayName]
	if !ok {
		return time.Time{}, fmt.Errorf("unknown weekday: %s", dayName)
	}
	today := time.Now()
	// Walk forward until we hit the target weekday
	for i := 1; i <= 7; i++ {
		d := today.AddDate(0, 0, i)
		if d.Weekday() == target {
			return d, nil
		}
	}
	return time.Time{}, fmt.Errorf("could not find next weekday: %s", dayName)
}
