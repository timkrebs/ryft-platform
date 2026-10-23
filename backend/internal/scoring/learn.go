package scoring

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/metrics"
)

// The weekly sleep-baseline estimate (docs/SCORING.md → Sleep, D67).
const (
	learnHour    = 4                  // Sunday from 04:00 local time
	learnEvery   = 6 * 24 * time.Hour // an estimate younger than this is this week's
	learnDays    = 30                 // scored days the estimate looks at
	learnRescore = 7                  // days rescored when the baseline in use moves
)

// Outcomes of LearnBaseline (the metric's label).
const (
	LearnApplied      = "applied"
	LearnKept         = "kept"
	LearnInsufficient = "insufficient"
)

// LearnDue says whether the weekly estimate is due: now is the user's local
// time, learnedAt the last estimate (nil: none yet).
func LearnDue(now time.Time, learnedAt *time.Time) bool {
	if now.Weekday() != time.Sunday || now.Hour() < learnHour {
		return false
	}
	return learnedAt == nil || now.Sub(*learnedAt) >= learnEvery
}

// LearnBaseline estimates the user's sleep baseline from the last 30 scored
// days up to today (local) and stores it. When the baseline in use moves (the
// user did not set it by hand), the last 7 days are rescored with it here,
// oldest first, so the need and debt chain through the new value.
func (e *Engine) LearnBaseline(ctx context.Context, userID uuid.UUID, today string) (outcome string, err error) {
	defer func() {
		if err != nil {
			outcome = "error"
		}
		metrics.SleepBaseLearned.WithLabelValues(outcome).Inc()
	}()
	d, err := time.Parse("2006-01-02", today)
	if err != nil {
		return "", fmt.Errorf("bad day %q: %w", today, err)
	}
	hist, err := e.st.ScoreHistory(ctx, userID, d.AddDate(0, 0, 1).Format("2006-01-02"), learnDays)
	if err != nil {
		return "", err
	}
	minutes, ok := LearnedSleepBase(hist)
	if !ok {
		return LearnInsufficient, nil
	}
	changed, err := e.st.SetLearnedBase(ctx, userID, minutes)
	if err != nil {
		return "", err
	}
	// Not the minutes: a person's usual sleep is a health value.
	e.log.InfoContext(ctx, "sleep baseline learned", "applied", changed)
	if !changed {
		return LearnKept, nil
	}
	from := d.AddDate(0, 0, -(learnRescore - 1)).Format("2006-01-02")
	if err := e.ComputeRange(ctx, userID, from, today); err != nil {
		return "", fmt.Errorf("rescore after learning: %w", err)
	}
	return LearnApplied, nil
}
