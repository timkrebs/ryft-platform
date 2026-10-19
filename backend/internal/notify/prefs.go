package notify

import (
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// Allowed says whether the user wants category.
func Allowed(p model.NotificationPrefs, category string) bool {
	on, ok := p.Categories[category]
	return !ok || on
}

// ParseClock reads a local "HH:MM".
func ParseClock(s string) (h, m int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// QuietUntil says whether now (in the user's zone) falls in their quiet hours
// and, if so, when they end. from > to wraps midnight (22:00–07:00); from = to
// is no quiet time at all.
func QuietUntil(p model.NotificationPrefs, now time.Time) (time.Time, bool) {
	if p.QuietHours == nil {
		return time.Time{}, false
	}
	fh, fm, ok1 := ParseClock(p.QuietHours.From)
	th, tm, ok2 := ParseClock(p.QuietHours.To)
	if !ok1 || !ok2 {
		return time.Time{}, false
	}
	from, to := fh*60+fm, th*60+tm
	cur := now.Hour()*60 + now.Minute()
	at := func(day time.Time, minutes int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, now.Location())
	}
	switch {
	case from == to:
		return time.Time{}, false
	case from < to: // 13:00–15:00
		if cur >= from && cur < to {
			return at(now, to), true
		}
	default: // 22:00–07:00
		if cur >= from {
			return at(now.AddDate(0, 0, 1), to), true
		}
		if cur < to {
			return at(now, to), true
		}
	}
	return time.Time{}, false
}
