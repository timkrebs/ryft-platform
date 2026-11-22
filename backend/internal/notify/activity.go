package notify

import (
	"strings"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring/workoutdup"
	"github.com/timkrebs/ryft/internal/store"
)

// ACTIVITY_SCORED's rules as pure functions (docs/API.md → Push notifications →
// ACTIVITY_SCORED: the rules; D253–D255). The service reads the candidates and writes the
// ledger; nothing here touches either.

const (
	// OwnBundleID is the RYFT app's bundle id. A workout it, or anything under it (a watch app,
	// an extension), wrote to Apple Health is never announced.
	OwnBundleID = "dev.timkrebs.ryft"
	// ActivityWindow is how long after its end a workout may still be announced (rule 3).
	ActivityWindow = store.ActivityWindow
	// ActivityMinDuration is the shortest workout announced (rule 4).
	ActivityMinDuration = 10 * time.Minute
	// BackfillWithin is how soon after a device was first seen the workouts it uploads count as
	// its history, not news (rule 5).
	BackfillWithin = time.Hour
	// ActivityDailyCap is the most alerts an account gets for the workouts of one local day (rule 7).
	ActivityDailyCap = 3
)

// ActivityCandidate is a workout the scan found (store.ActivityCandidate).
type ActivityCandidate = store.ActivityCandidate

// Verdict is what the rules that need no ledger say about a candidate.
type Verdict int

const (
	// Skip means not (yet) a candidate. Nothing is recorded, so a later scan looks again: the
	// strain may arrive, a Train session may attach, the workout may end.
	Skip Verdict = iota
	// Backfill means history of a device first seen under an hour before. Decided silently.
	Backfill
	// Eligible means it passes rules 1 to 5; the ledger and the user's settings decide the rest.
	Eligible
)

// ActivityRule applies rules 1 to 5 to c at now. The ledger (6), the cap (7) and the
// settings (8) are ActivityOutcome's.
func ActivityRule(c ActivityCandidate, now time.Time) Verdict {
	switch {
	case c.Source == model.SessionSource || c.SessionID != nil: // 1a: a Train session's workout, or one a session attached to
		return Skip
	case c.SourceBundleID != nil && ownBundle(*c.SourceBundleID): // 1b
		return Skip
	case c.Strain == nil: // 2: unscored (0 counts)
		return Skip
	case c.End.After(now) || now.Sub(c.End) > ActivityWindow: // 3
		return Skip
	case c.End.Sub(c.Start) < ActivityMinDuration: // 4
		return Skip
	case backfill(c): // 5
		return Backfill
	}
	return Eligible
}

// backfill: the row was stored 0 up to BackfillWithin after its device was first seen. No device on
// record (nil) passes, and so does a row stored before its device row was created (a negative gap:
// a merge, or a re-created device row, is no first sync).
func backfill(c ActivityCandidate) bool {
	if c.DeviceSeen == nil {
		return false
	}
	d := c.CreatedAt.Sub(*c.DeviceSeen)
	return d >= 0 && d < BackfillWithin
}

// ownBundle: the RYFT bundle id or one under it, without regard to case.
func ownBundle(id string) bool {
	id = strings.ToLower(id)
	return id == OwnBundleID || strings.HasPrefix(id, OwnBundleID+".")
}

// Inherited says whether a ledger row decided c already under another id: a row holds the id of
// c or of a workout hidden behind it (a duplicate group's primary may change), or is the same
// workout as c or one of them by the fold's rule (a source that re-writes a workout under a new
// UUID, whether the old copy is folded, deleted through `deleted` or still stored).
func Inherited(c ActivityCandidate, ledger []store.AlertMark) bool {
	refs := make([]store.WorkoutRef, 0, 1+len(c.Members))
	refs = append(refs, c.WorkoutRef)
	refs = append(refs, c.Members...)
	for _, a := range ledger {
		for _, x := range refs {
			if a.ID == x.ID || workoutdup.SameWorkout(copyOf(a.WorkoutRef), copyOf(x)) {
				return true
			}
		}
	}
	return false
}

func copyOf(w store.WorkoutRef) workoutdup.Copy {
	return workoutdup.Copy{ID: w.ID, Source: w.Source, Activity: w.Activity, Start: w.Start, End: w.End}
}

// ActivityOutcome is the ledger outcome for c given the user's rows and how many alerts of
// c's local day are sending or sent. Order: decided already (store.AlertDuplicate, nothing is
// written), inherited, pre (what the caller knows: backfill, muted, quiet or no_token; empty
// when none), capped at limit, else sending. A muted or quiet workout does not count toward
// the cap, so the order of pre and the cap only changes the label on the row.
func ActivityOutcome(c ActivityCandidate, ledger []store.AlertMark, sentOnDay int, pre string, limit int) string {
	for _, a := range ledger {
		if a.ID == c.ID {
			return store.AlertDuplicate
		}
	}
	switch {
	case Inherited(c, ledger):
		return store.AlertInherited
	case pre != "":
		return pre
	case sentOnDay >= limit:
		return store.AlertCapped
	}
	return store.AlertSending
}
