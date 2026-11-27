package main

import (
	"context"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// The sync button's rules without a database (D297): which queued days hold today's scores back, and the healthkit
// source's status per gate mode. The route itself is tested with Postgres in syncstatus_db_test.go.

// syncFreshness: a day holds the button when it is today or yesterday (any age of its queue row), or one of the last
// 30 days and queued within 10 minutes (scoring a past day rescores the days after it, today included).
func TestSyncFreshness(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	const (
		today     = "2026-11-27"
		yesterday = "2026-11-26"
		twoBack   = "2026-11-25"
		back10    = "2026-11-17"
		back30    = "2026-10-28" // today - 30: the oldest day a fresh queue row can hold
		back31    = "2026-10-27"
		back40    = "2026-10-18"
	)
	pd := func(day string, queued time.Time) store.PendingDay {
		return store.PendingDay{Day: day, QueuedAt: queued}
	}
	cases := []struct {
		name  string
		today string // defaults to today
		days  []store.PendingDay
		hold  bool
		since time.Time // zero: nil
	}{
		{name: "nothing queued"},
		{name: "today, queued days ago", days: []store.PendingDay{pd(today, ago(5*24*time.Hour))}, hold: true, since: ago(5 * 24 * time.Hour)},
		{name: "yesterday, queued days ago", days: []store.PendingDay{pd(yesterday, ago(3*24*time.Hour))}, hold: true, since: ago(3 * 24 * time.Hour)},
		{name: "two days back, queued days ago", days: []store.PendingDay{pd(twoBack, ago(3*24*time.Hour))}},
		{name: "two days back, queued 9 minutes ago", days: []store.PendingDay{pd(twoBack, ago(9*time.Minute))}, hold: true, since: ago(9 * time.Minute)},
		{name: "10 days back, queued now", days: []store.PendingDay{pd(back10, now)}, hold: true, since: now},
		{name: "10 days back, the same row 11 minutes later", days: []store.PendingDay{pd(back10, ago(11*time.Minute))}},
		{name: "today - 30, queued 9 minutes ago", days: []store.PendingDay{pd(back30, ago(9*time.Minute))}, hold: true, since: ago(9 * time.Minute)},
		{name: "today - 30, queued 11 minutes ago", days: []store.PendingDay{pd(back30, ago(11*time.Minute))}},
		{name: "today - 30, queued 9 min 59 s ago", days: []store.PendingDay{pd(back30, ago(10*time.Minute-time.Second))}, hold: true, since: ago(10*time.Minute - time.Second)},
		{name: "today - 30, queued exactly 10 minutes ago: the window is open at its end", days: []store.PendingDay{pd(back30, ago(10*time.Minute))}},
		{name: "a day after today (a timezone change), queued now", days: []store.PendingDay{pd("2026-11-28", now)}},
		{name: "a day after today, queued days ago", days: []store.PendingDay{pd("2026-11-28", ago(3*24*time.Hour)), pd("2026-12-05", now)}},
		{name: "today - 31, queued now", days: []store.PendingDay{pd(back31, now)}},
		{name: "today - 40, queued now", days: []store.PendingDay{pd(back40, now)}},
		{name: "since is the earliest of the days that hold",
			days: []store.PendingDay{pd(today, ago(2*time.Hour)), pd(yesterday, ago(5*time.Hour)), pd(back10, ago(3*time.Minute))},
			hold: true, since: ago(5 * time.Hour)},
		{name: "a day that does not hold never sets since, however old",
			days: []store.PendingDay{pd("2026-11-22", ago(30*time.Hour)), pd(today, ago(time.Hour))},
			hold: true, since: ago(time.Hour)},
		{name: "only days that do not hold",
			days: []store.PendingDay{pd("2026-11-22", ago(30*time.Hour)), pd(back40, ago(time.Minute)), pd(back31, ago(time.Minute))}},
		{name: "the month boundary: yesterday is February 28, today - 30 is January 30", today: "2026-03-01",
			days: []store.PendingDay{pd("2026-02-28", ago(9*24*time.Hour)), pd("2026-01-30", ago(time.Minute)), pd("2026-01-29", ago(time.Minute))},
			hold: true, since: ago(9 * 24 * time.Hour)},
		{name: "the month boundary: today - 30 and today - 31 alone", today: "2026-03-01",
			days: []store.PendingDay{pd("2026-01-29", ago(time.Minute))}},
		{name: "a malformed today holds nothing", today: "not-a-day", days: []store.PendingDay{pd(today, now)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			day := c.today
			if day == "" {
				day = today
			}
			hold, since := syncFreshness(c.days, day, now)
			if hold != c.hold {
				t.Fatalf("holds: %v, want %v", hold, c.hold)
			}
			switch {
			case c.since.IsZero() && since != nil:
				t.Fatalf("since %v, want nil", since)
			case !c.since.IsZero() && since == nil:
				t.Fatalf("since nil, want %v", c.since)
			case !c.since.IsZero() && !since.Equal(c.since):
				t.Fatalf("since %v, want %v", since, c.since)
			}
			if since != nil && since.Location() != time.UTC {
				t.Fatalf("since is in %v, want UTC", since.Location())
			}
		})
	}
}

// since is UTC whatever zone the database driver handed the time in.
func TestSyncFreshnessAnswersInUTC(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	queued := now.Add(-time.Hour).In(berlin)
	hold, since := syncFreshness([]store.PendingDay{{Day: "2026-11-27", QueuedAt: queued}}, "2026-11-27", now)
	if !hold || since == nil || !since.Equal(queued) || since.Location() != time.UTC {
		t.Fatalf("hold %v, since %v", hold, since)
	}
}

// The healthkit source against each gate on its own mode (D297): on refuses, warn and off do not (in warn the status
// stays ok and only `gate` says what is missing). The invite is named first. gateFor, which answers the first failing
// gate whatever its mode, is not what decides the status.
func TestSyncSourcesHealthKitStatus(t *testing.T) {
	const policy = "2026-10-20"
	current := &model.Consent{PolicyVersion: policy}
	stale := &model.Consent{PolicyVersion: "2026-01-01"}
	on, warn, off := config.ModeOn, config.ModeWarn, config.ModeOff
	cases := []struct {
		name          string
		gate, consent config.Mode
		user          model.User
		reason        string // "" is ok
	}{
		{"both on, no invite, no consent: the invite first", on, on, model.User{}, model.CodeInviteRequired},
		{"both on, no invite, consent", on, on, model.User{Consent: current}, model.CodeInviteRequired},
		{"both on, invite, no consent", on, on, model.User{BetaAccess: true}, model.CodeConsentRequired},
		{"both on, invite, a consent to an older policy", on, on, model.User{BetaAccess: true, Consent: stale}, model.CodeConsentRequired},
		{"both on, invite, consent", on, on, model.User{BetaAccess: true, Consent: current}, ""},
		{"beta on, no invite, consent off", on, off, model.User{}, model.CodeInviteRequired},
		{"beta on, invite, consent warn, no consent: warn does not refuse", on, warn, model.User{BetaAccess: true}, ""},
		{"beta warn, no invite, consent on, no consent: the consent, not the warned invite", warn, on, model.User{}, model.CodeConsentRequired},
		{"beta warn, invite, consent on, no consent", warn, on, model.User{BetaAccess: true}, model.CodeConsentRequired},
		{"beta warn, no invite, consent on, consent", warn, on, model.User{Consent: current}, ""},
		{"both warn, nothing: syncs pass", warn, warn, model.User{}, ""},
		{"beta off, no invite, consent on, consent: access does not matter", off, on, model.User{Consent: current}, ""},
		{"beta off, consent on, no consent", off, on, model.User{}, model.CodeConsentRequired},
		{"beta off, consent off", off, off, model.User{}, ""},
		{"beta off, consent warn, no consent", off, warn, model.User{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &handler{beta: config.Beta{Gate: c.gate, Consent: c.consent, PrivacyPolicyVersion: policy}} // h.conn nil: no providers
			sources, since, err := h.syncSources(context.Background(), c.user, store.SyncState{})
			if err != nil || since != nil {
				t.Fatalf("since %v, err %v", since, err)
			}
			if len(sources) != 1 || sources[0].Source != model.SyncSourceHealthKit {
				t.Fatalf("sources %+v, want exactly healthkit", sources)
			}
			got := sources[0]
			if c.reason == "" {
				if got.Status != model.SyncSourceOK || got.Reason != nil {
					t.Fatalf("%+v, want ok without a reason", got)
				}
				return
			}
			if got.Status != model.SyncSourceError || got.Reason == nil || *got.Reason != c.reason {
				t.Fatalf("status %q reason %v, want error/%s", got.Status, got.Reason, c.reason)
			}
		})
	}
}

// healthkit's last_sync_at is the route's, in UTC; null before the first sync.
func TestSyncSourcesHealthKitLastSync(t *testing.T) {
	h := &handler{beta: config.Beta{Gate: config.ModeOff, Consent: config.ModeOff}}
	sources, _, err := h.syncSources(context.Background(), model.User{}, store.SyncState{})
	if err != nil || sources[0].LastSyncAt != nil {
		t.Fatalf("before the first sync: %+v %v", sources, err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 11, 27, 9, 30, 0, 0, time.UTC).In(berlin)
	sources, _, err = h.syncSources(context.Background(), model.User{}, store.SyncState{LastSyncAt: &last})
	if err != nil || sources[0].LastSyncAt == nil || !sources[0].LastSyncAt.Equal(last) || sources[0].LastSyncAt.Location() != time.UTC {
		t.Fatalf("after: %+v %v", sources, err)
	}
}
