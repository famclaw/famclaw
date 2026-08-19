package reminder

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseTime parses natural language time expressions into a time.Time.
// Supports relative times (in X minutes/hours/days) and absolute times (at HH:MM, tomorrow HH:MM).
// Returns the parsed time in UTC.
func ParseTime(input string, now time.Time, loc *time.Location) (time.Time, error) {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return time.Time{}, errors.New("empty time expression")
	}

	// Try absolute time first: "at HH:MM" or "at H:MM am/pm" or "tomorrow at HH:MM"
	if t, ok := parseAbsolute(input, now, loc); ok {
		return t.UTC(), nil
	}

	// Try relative time: "in X minutes/hours/days"
	if t, ok := parseRelative(input, now, loc); ok {
		return t.UTC(), nil
	}

	// Try shorthand: "5m", "2h", "1d"
	if t, ok := parseShorthand(input, now, loc); ok {
		return t.UTC(), nil
	}

	return time.Time{}, fmt.Errorf("could not parse time expression %q; try e.g. \"in 10 minutes\", \"in 2 hours\", \"in 3 days at 17:00\", \"at 9:00\"", input)
}

// inDaysColonRegex matches "in N days [at] HH:MM [am/pm]" — a multi-day
// offset with an explicit time of day (e.g. "in 2 days at 5:00 pm").
var inDaysColonRegex = regexp.MustCompile(`^in\s+(\d+)\s+days?\s+(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?$`)

// inDaysAPMRegex matches "in N days [at] H am/pm" without minutes
// (e.g. "in 2 days at 5 pm"). Hours without a meridiem are ambiguous and
// deliberately rejected.
var inDaysAPMRegex = regexp.MustCompile(`^in\s+(\d+)\s+days?\s+(?:at\s+)?(\d{1,2})\s+(am|pm)$`)

func parseAbsolute(input string, now time.Time, loc *time.Location) (time.Time, bool) {
	// "at HH:MM" or "at H:MM am/pm"
	atRegex := regexp.MustCompile(`^at\s+(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := atRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		h = applyMeridiem(h, m[3])
		t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, loc)
		if t.Before(now) {
			t = t.Add(24 * time.Hour)
		}
		return t, true
	}

	// "tomorrow at HH:MM" or "tomorrow HH:MM"
	tomorrowRegex := regexp.MustCompile(`^tomorrow\s+(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := tomorrowRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		h = applyMeridiem(h, m[3])
		tomorrow := now.Add(24 * time.Hour)
		return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), h, min, 0, 0, loc), true
	}

	// "today at HH:MM"
	todayRegex := regexp.MustCompile(`^today\s+(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := todayRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		h = applyMeridiem(h, m[3])
		t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, loc)
		if t.Before(now) {
			t = t.Add(24 * time.Hour)
		}
		return t, true
	}

	// "tomorrow morning"/"tomorrow afternoon"/"tomorrow evening"/"tomorrow night"
	tomorrowPeriodRegex := regexp.MustCompile(`^tomorrow\s+(morning|afternoon|evening|night)$`)
	if m := tomorrowPeriodRegex.FindStringSubmatch(input); m != nil {
		tomorrow := now.Add(24 * time.Hour)
		hour := periodStartHour(m[1])
		return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), hour, 0, 0, 0, loc), true
	}

	// Day of week: "monday at HH:MM", "next friday 10am"
	dowRegex := regexp.MustCompile(`^(?:next\s+)?(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\s+(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := dowRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[2])
		min, _ := strconv.Atoi(m[3])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		h = applyMeridiem(h, m[4])
		targetDOW := parseDayOfWeek(m[1])
		daysAhead := (targetDOW - now.Weekday() + 7) % 7
		if strings.HasPrefix(input, "next ") || daysAhead == 0 {
			daysAhead += 7
		}
		t := now.AddDate(0, 0, int(daysAhead))
		return time.Date(t.Year(), t.Month(), t.Day(), h, min, 0, 0, loc), true
	}

	return time.Time{}, false
}

// applyMeridiem normalizes a 12-hour clock value with an optional am/pm
// meridiem to a 24-hour value. An empty meridiem leaves h unchanged.
func applyMeridiem(h int, meridiem string) int {
	if meridiem == "pm" && h < 12 {
		h += 12
	}
	if meridiem == "am" && h == 12 {
		h = 0
	}
	return h
}

func parseDayOfWeek(s string) time.Weekday {
	switch s {
	case "monday":
		return time.Monday
	case "tuesday":
		return time.Tuesday
	case "wednesday":
		return time.Wednesday
	case "thursday":
		return time.Thursday
	case "friday":
		return time.Friday
	case "saturday":
		return time.Saturday
	case "sunday":
		return time.Sunday
	default:
		return time.Sunday
	}
}

// halfHourRegex matches "in half an hour" / "in half hour" (-> 30 minutes).
var halfHourRegex = regexp.MustCompile(`^in\s+half(?:\s+an)?\s+hour$`)

func parseRelative(input string, now time.Time, loc *time.Location) (time.Time, bool) {
	// "in N days [at] HH:MM [am/pm]" or "in N days [at] H am/pm" — a day
	// offset combined with an explicit time of day is honored exactly: the
	// reminder lands at HH:MM on the calendar day N days from now. This is
	// what makes "in 2 days at 5:00 pm" work (issue #366: the time used to
	// be silently dropped for multi-day offsets).
	if m := inDaysColonRegex.FindStringSubmatch(input); m != nil {
		days, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		min, _ := strconv.Atoi(m[3])
		if h > 23 || min > 59 {
			return time.Time{}, false
		}
		h = applyMeridiem(h, m[4])
		target := now.AddDate(0, 0, days)
		t := time.Date(target.Year(), target.Month(), target.Day(), h, min, 0, 0, loc)
		if t.Before(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}
	if m := inDaysAPMRegex.FindStringSubmatch(input); m != nil {
		days, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		if h < 1 || h > 12 {
			return time.Time{}, false
		}
		h = applyMeridiem(h, m[3])
		target := now.AddDate(0, 0, days)
		t := time.Date(target.Year(), target.Month(), target.Day(), h, 0, 0, 0, loc)
		if t.Before(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	}

	// "in half an hour" / "in half hour" -> 30 minutes
	if halfHourRegex.MatchString(input) {
		return now.Add(30 * time.Minute), true
	}

	// "in hour" is retained as a shorthand for "in an hour"; no other bare unit
	// form (e.g. "in minute", "in day") is accepted.
	if input == "in hour" {
		return now.Add(time.Hour), true
	}

	// "in <n> <unit>" or "in a/an <unit>": the leading token is REQUIRED and must
	// be either a number or an a/an article, so bare units such as "in minute",
	// "in second", "in day" are rejected. Units accept common abbreviations
	// (min, mins, sec, secs, hr, hrs, m, h) as well as full words.
	inRegex := regexp.MustCompile(`^in\s+(?:(\d+)\s+|an?\s+)(minute|minutes|min|mins|second|seconds|sec|secs|hour|hours|hr|hrs|day|days|m|h|s|d)$`)
	if m := inRegex.FindStringSubmatch(input); m != nil {
		amount := 1
		if m[1] != "" {
			amount, _ = strconv.Atoi(m[1])
		}
		dur := unitToDuration(m[2])
		if dur == 0 {
			return time.Time{}, false
		}
		return now.Add(dur * time.Duration(amount)), true
	}
	return time.Time{}, false
}

func parseShorthand(input string, now time.Time, loc *time.Location) (time.Time, bool) {
	// "5m", "2h", "1d", "30s"
	shorthandRegex := regexp.MustCompile(`^(\d+)([smhd])$`)
	if m := shorthandRegex.FindStringSubmatch(input); m != nil {
		val, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "s":
			return now.Add(time.Duration(val) * time.Second), true
		case "m":
			return now.Add(time.Duration(val) * time.Minute), true
		case "h":
			return now.Add(time.Duration(val) * time.Hour), true
		case "d":
			return now.Add(time.Duration(val) * 24 * time.Hour), true
		}
	}
	return time.Time{}, false
}

// unitToDuration maps a (possibly abbreviated) time unit to its duration.
// It returns 0 for anything it does not recognise.
func unitToDuration(unit string) time.Duration {
	switch unit {
	case "minute", "minutes", "min", "mins", "m":
		return time.Minute
	case "second", "seconds", "sec", "secs", "s":
		return time.Second
	case "hour", "hours", "hr", "hrs", "h":
		return time.Hour
	case "day", "days", "d":
		return 24 * time.Hour
	}
	return 0
}

// periodStartHour returns a default hour for a named time-of-day period.
func periodStartHour(period string) int {
	switch period {
	case "morning":
		return 9
	case "afternoon":
		return 14
	case "evening":
		return 18
	case "night":
		return 21
	}
	return 9
}

// FormatDuration formats a duration in a human-readable way.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute"
		}
		return strconv.Itoa(m) + " minutes"
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		if h == 1 {
			return "1 hour"
		}
		return strconv.Itoa(h) + " hours"
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day"
	}
	return strconv.Itoa(days) + " days"
}
