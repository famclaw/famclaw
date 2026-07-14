package reminder

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseTime parses natural language time expressions into a time.Time.
// Supports relative times (in X minutes/hours/days) and absolute times (at HH:MM, tomorrow HH:MM).
// Returns the parsed time in UTC.
func ParseTime(input string, now time.Time) (time.Time, error) {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return time.Time{}, errors.New("empty time expression")
	}

	// Try absolute time first: "at HH:MM" or "at H:MM am/pm" or "tomorrow at HH:MM"
	if t, ok := parseAbsolute(input, now); ok {
		return t, nil
	}

	// Try relative time: "in X minutes/hours/days"
	if t, ok := parseRelative(input, now); ok {
		return t, nil
	}

	// Try shorthand: "5m", "2h", "1d"
	if t, ok := parseShorthand(input, now); ok {
		return t, nil
	}

	return time.Time{}, errors.New("could not parse time expression: " + input)
}

func parseAbsolute(input string, now time.Time) (time.Time, bool) {
	// "at HH:MM" or "at H:MM am/pm"
	atRegex := regexp.MustCompile(`^at\s+(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := atRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		if m[3] != "" {
			if m[3] == "pm" && h < 12 {
				h += 12
			}
			if m[3] == "am" && h == 12 {
				h = 0
			}
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, time.UTC)
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
		if m[3] != "" {
			if m[3] == "pm" && h < 12 {
				h += 12
			}
			if m[3] == "am" && h == 12 {
				h = 0
			}
		}
		tomorrow := now.Add(24 * time.Hour)
		return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), h, min, 0, 0, time.UTC), true
	}

	// "today at HH:MM"
	todayRegex := regexp.MustCompile(`^today\s+(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := todayRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		if m[3] != "" {
			if m[3] == "pm" && h < 12 {
				h += 12
			}
			if m[3] == "am" && h == 12 {
				h = 0
			}
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), h, min, 0, 0, time.UTC)
		if t.Before(now) {
			t = t.Add(24 * time.Hour)
		}
		return t, true
	}

	// Day of week: "monday at HH:MM", "next friday 10am"
	dowRegex := regexp.MustCompile(`^(?:next\s+)?(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\s+(?:at\s+)?(\d{1,2}):(\d{2})\s*(am|pm)?$`)
	if m := dowRegex.FindStringSubmatch(input); m != nil {
		h, _ := strconv.Atoi(m[2])
		min, _ := strconv.Atoi(m[3])
		if h < 0 || h > 23 || min < 0 || min > 59 {
			return time.Time{}, false
		}
		if m[4] != "" {
			if m[4] == "pm" && h < 12 {
				h += 12
			}
			if m[4] == "am" && h == 12 {
				h = 0
			}
		}
		targetDOW := parseDayOfWeek(m[1])
		daysAhead := (targetDOW - now.Weekday() + 7) % 7
		if strings.HasPrefix(input, "next ") || daysAhead == 0 {
			daysAhead += 7
		}
		t := now.AddDate(0, 0, int(daysAhead))
		return time.Date(t.Year(), t.Month(), t.Day(), h, min, 0, 0, time.UTC), true
	}

	return time.Time{}, false
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

func parseRelative(input string, now time.Time) (time.Time, bool) {
	// "in X minutes/hours/days" or "in a minute/hour/day"
	inRegex := regexp.MustCompile(`^in\s+(?:a\s+|an\s+|(\d+)\s+)(minute|minutes|hour|hours|day|days)$`)
	if m := inRegex.FindStringSubmatch(input); m != nil {
		var amount int
		if m[1] != "" {
			amount, _ = strconv.Atoi(m[1])
		} else {
			amount = 1
		}
		switch m[2] {
		case "minute", "minutes":
			return now.Add(time.Duration(amount) * time.Minute), true
		case "hour", "hours":
			return now.Add(time.Duration(amount) * time.Hour), true
		case "day", "days":
			return now.Add(time.Duration(amount) * 24 * time.Hour), true
		}
	}
	return time.Time{}, false
}

func parseShorthand(input string, now time.Time) (time.Time, bool) {
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