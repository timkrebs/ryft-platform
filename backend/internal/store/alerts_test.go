package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

// ACTIVITY_SCORED's store side (0041, D253-D255). Postgres tests, opt-in like the rest of this
// package. The reads and the ledger run through a pool opened AS ryft_notify, the role they
// run as: a column its grant leaves out fails here as it would in production.

// aw is a workout row of these tests, inserted as the superuser.
type aw struct {
	id       uuid.UUID
	source   string
	start    time.Time
	end      time.Time
	strain   *float64
	bundle   *string
	device   *uuid.UUID
	dupOf    *uuid.UUID
	inserted *time.Time // created_at; nil: now
}

func putAW(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, w aw) uuid.UUID {
	t.Helper()
	if w.id == uuid.Nil {
		w.id = uuid.New()
	}
	if _, err := pool.Exec(context.Background(), `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source, strain,
		  source_bundle_id, device_id, duplicate_of, created_at)
		VALUES ($1, $2, 'running', $3, $4, $5, $6, $7, $8, $9, COALESCE($10, now()))`,
		w.id, user, w.start, w.end, w.source, w.strain, w.bundle, w.device, w.dupOf, w.inserted); err != nil {
		t.Fatal(err)
	}
	return w.id
}

func ledgerRows(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) map[uuid.UUID][2]any {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT workout_id, outcome, devices FROM workout_alerts WHERE user_id = $1`, user)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[uuid.UUID][2]any{}
	for rows.Next() {
		var id uuid.UUID
		var outcome string
		var devices int
		if err := rows.Scan(&id, &outcome, &devices); err != nil {
			t.Fatal(err)
		}
		out[id] = [2]any{outcome, devices}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// GET /v1/me lists ACTIVITY_SCORED, on until turned off (no database needed).
func TestPrefsListActivityScored(t *testing.T) {
	p := prefsOf(nil, nil, nil)
	if on, ok := p.Categories[model.NotifyActivityScored]; !ok || !on || len(p.Categories) != 5 {
		t.Errorf("default categories %v", p.Categories)
	}
	if off := prefsOf(map[string]bool{model.NotifyActivityScored: false}, nil, nil); off.Categories[model.NotifyActivityScored] {
		t.Error("ACTIVITY_SCORED off is listed on")
	}
}

func idsOf(cs []ActivityCandidate) []uuid.UUID {
	out := make([]uuid.UUID, len(cs))
	for k, c := range cs {
		out[k] = c.ID
	}
	return out
}

// The scan: the window, the visibility rule of ListWorkoutsBetween, the ledger, the members, and the two joins.
func TestActivityCandidates(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	device := uuid.New()
	if err := st.TouchDevice(ctx, user, device, "phone", nil, nil); err != nil {
		t.Fatal(err)
	}
	notify := New(dupRolePool(t, admin, "ryft_notify"))
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	fresh := putAW(t, admin, user, aw{source: "Apple Watch", start: ago(2 * time.Hour), end: ago(time.Hour), strain: ptr(12.8),
		bundle: ptr("com.apple.health"), device: &device})
	tooOld := putAW(t, admin, user, aw{source: "Apple Watch", start: ago(14 * time.Hour), end: ago(13 * time.Hour), strain: ptr(5.0)})
	hidden := putAW(t, admin, user, aw{source: "Garmin Connect", start: ago(2 * time.Hour), end: ago(time.Hour), dupOf: &fresh})
	orphan := putAW(t, admin, user, aw{source: "Garmin Connect", start: ago(210 * time.Minute), end: ago(3 * time.Hour), strain: ptr(7.0),
		dupOf: ptr(uuid.New())}) // its primary was deleted: visible
	decided := putAW(t, admin, user, aw{source: "Apple Watch", start: ago(4 * time.Hour), end: ago(220 * time.Minute), strain: ptr(6.0)})
	unseen := putAW(t, admin, user, aw{source: "Fitbit", start: ago(5 * time.Hour), end: ago(270 * time.Minute)}) // no strain yet: still a candidate

	if out, err := notify.DecideWorkoutAlert(ctx, user, AlertMark{WorkoutRef: WorkoutRef{ID: decided, Source: "Apple Watch", Activity: "running",
		Start: ago(4 * time.Hour), End: ago(220 * time.Minute)}, Day: now.Format(time.DateOnly)},
		func([]AlertMark, int) string { return AlertSent }); err != nil || out != AlertSent {
		t.Fatalf("decide: %q %v", out, err)
	}

	got, err := notify.ActivityCandidates(ctx, user, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := []uuid.UUID{unseen, orphan, fresh}; !slices.Equal(idsOf(got), want) {
		t.Fatalf("candidates %v, want %v (oldest start first; not the hidden duplicate, the decided one or the one past 12 h: %v %v %v)",
			idsOf(got), want, hidden, decided, tooOld)
	}
	byID := map[uuid.UUID]ActivityCandidate{}
	for _, c := range got {
		byID[c.ID] = c
	}
	f := byID[fresh]
	if f.Source != "Apple Watch" || f.Activity != "running" || f.Strain == nil || *f.Strain != 12.8 || f.SourceBundleID == nil ||
		*f.SourceBundleID != "com.apple.health" || f.DeviceID == nil || *f.DeviceID != device || !f.End.Equal(ago(time.Hour)) {
		t.Errorf("the fresh workout: %+v", f)
	}
	if f.DeviceSeen == nil || time.Since(*f.DeviceSeen) > 5*time.Minute || f.CreatedAt.IsZero() {
		t.Errorf("the device join: seen %v, row inserted %v", f.DeviceSeen, f.CreatedAt)
	}
	if len(f.Members) != 1 || f.Members[0].ID != hidden || f.Members[0].Source != "Garmin Connect" || f.Members[0].Activity != "running" {
		t.Errorf("members of the primary: %+v", f.Members)
	}
	if o := byID[orphan]; len(o.Members) != 0 || o.DeviceID != nil || o.DeviceSeen != nil {
		t.Errorf("the orphan: %+v", o)
	}
	if u := byID[unseen]; u.Strain != nil {
		t.Errorf("an unscored workout has no strain: %+v", u)
	}

	// Another account's workouts and ledger are not seen.
	other := dupUser(t, st)
	putAW(t, admin, other, aw{source: "Apple Watch", start: ago(2 * time.Hour), end: ago(time.Hour)})
	if again, err := notify.ActivityCandidates(ctx, user, now); err != nil || len(again) != 3 {
		t.Errorf("the other account's rows leak: %d %v", len(again), err)
	}
	// Nothing for an account with no recent workouts.
	if none, err := notify.ActivityCandidates(ctx, uuid.New(), now); err != nil || len(none) != 0 {
		t.Errorf("no account: %d %v", len(none), err)
	}
}

// Rule 5 reads the device row of the account that uploaded: one install used by two accounts has a
// row for each (devices' key is (user_id, id)), so the second account's first sync is its own
// device's first sight, whatever the first account's row says.
func TestActivityCandidatesDeviceSeenIsPerAccount(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	first, second := dupUser(t, st), dupUser(t, st)
	phone := uuid.New()
	for _, u := range []uuid.UUID{first, second} {
		if err := st.TouchDevice(ctx, u, phone, "phone", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `UPDATE devices SET created_at = now() - interval '30 days' WHERE user_id = $1`, first); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, u := range []uuid.UUID{first, second} {
		putAW(t, admin, u, aw{source: "Apple Watch", start: now.Add(-2 * time.Hour), end: now.Add(-time.Hour), strain: ptr(9.0), device: &phone})
	}
	notify := New(dupRolePool(t, admin, "ryft_notify"))
	for u, old := range map[uuid.UUID]bool{first: true, second: false} {
		cs, err := notify.ActivityCandidates(ctx, u, now)
		if err != nil || len(cs) != 1 || cs[0].DeviceSeen == nil {
			t.Fatalf("%v %v", cs, err)
		}
		if age := cs[0].CreatedAt.Sub(*cs[0].DeviceSeen); old != (age > 24*time.Hour) {
			t.Errorf("account with a 30-day-old device row: %v, stored %v after its device was first seen", old, age)
		}
	}
}

// Six deciders at once for one day's workouts: exactly three win the cap, whatever the interleaving.
func TestDecideWorkoutAlertHoldsTheCapAcrossGoroutines(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	notify := New(dupRolePool(t, admin, "ryft_notify"))
	now := time.Now().UTC().Truncate(time.Second)
	day := now.Format(time.DateOnly)

	var wg sync.WaitGroup
	outcomes := make(chan string, 6)
	for k := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mark := AlertMark{WorkoutRef: WorkoutRef{ID: uuid.New(), Source: fmt.Sprintf("S%d", k), Activity: "running",
				Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}, Day: day}
			out, err := notify.DecideWorkoutAlert(ctx, user, mark, func(_ []AlertMark, sent int) string {
				if sent >= 3 {
					return AlertCapped
				}
				return AlertSending
			})
			if err != nil {
				t.Errorf("decide %d: %v", k, err)
			}
			outcomes <- out
		}()
	}
	wg.Wait()
	close(outcomes)
	n := map[string]int{}
	for o := range outcomes {
		n[o]++
	}
	if n[AlertSending] != 3 || n[AlertCapped] != 3 {
		t.Fatalf("outcomes %v, want 3 sending and 3 capped", n)
	}
	var rows int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM workout_alerts WHERE user_id = $1 AND outcome = 'sending'`, user).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("%d sending rows in the ledger, %v", rows, err)
	}
}

// A workout is decided once; rows whose workout ended over two days ago are pruned (and only the user's);
// Finish and Release touch a claim in flight only.
func TestDecideWorkoutAlertOnceAndPrunes(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user, other := dupUser(t, st), dupUser(t, st)
	notify := New(dupRolePool(t, admin, "ryft_notify"))
	now := time.Now().UTC().Truncate(time.Second)
	day := now.Format(time.DateOnly)

	insert := func(u uuid.UUID, end time.Time, outcome string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := admin.Exec(ctx, `INSERT INTO workout_alerts (user_id, workout_id, source, activity, start_at, end_at, day, outcome)
			VALUES ($1, $2, 'Apple Watch', 'running', $3, $4, $5::text::date, $6)`, u, id, end.Add(-time.Hour), end,
			end.Format(time.DateOnly), outcome); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stale := insert(user, now.Add(-49*time.Hour), AlertSent) // ended over 2 days ago
	kept := insert(user, now.Add(-47*time.Hour), AlertSent)  // not yet
	theirs := insert(other, now.Add(-72*time.Hour), AlertSent)

	mark := AlertMark{WorkoutRef: WorkoutRef{ID: uuid.New(), Source: "Apple Watch", Activity: "running", Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}, Day: day}
	var seen []uuid.UUID
	out, err := notify.DecideWorkoutAlert(ctx, user, mark, func(ledger []AlertMark, sent int) string {
		for _, a := range ledger {
			seen = append(seen, a.ID)
		}
		if sent != 0 {
			t.Errorf("sent on the day: %d, want 0 (the rows are of other days)", sent)
		}
		return AlertSending
	})
	if err != nil || out != AlertSending {
		t.Fatalf("first decision: %q %v", out, err)
	}
	if !slices.Equal(seen, []uuid.UUID{kept}) {
		t.Errorf("the ledger decide saw: %v, want only %v (not the pruned %v, not another account's %v)", seen, kept, stale, theirs)
	}
	rows := ledgerRows(t, admin, user)
	if _, ok := rows[stale]; ok || len(rows) != 2 || rows[mark.ID][0] != AlertSending {
		t.Errorf("ledger after the first decision: %v", rows)
	}
	if _, ok := ledgerRows(t, admin, other)[theirs]; !ok {
		t.Error("another account's old row was pruned")
	}

	// The same workout again: decided already, nothing written.
	again := func(ledger []AlertMark, _ int) string {
		if slices.ContainsFunc(ledger, func(a AlertMark) bool { return a.ID == mark.ID }) {
			return AlertDuplicate
		}
		return AlertSending
	}
	if out, err := notify.DecideWorkoutAlert(ctx, user, mark, again); err != nil || out != AlertDuplicate {
		t.Fatalf("second decision: %q %v", out, err)
	}
	if n := len(ledgerRows(t, admin, user)); n != 2 {
		t.Errorf("a duplicate wrote a row: %d rows", n)
	}

	// A decide that returns an outcome for a workout that is there anyway (the impossible race): duplicate.
	if out, err := notify.DecideWorkoutAlert(ctx, user, mark, func([]AlertMark, int) string { return AlertSending }); err != nil || out != AlertDuplicate {
		t.Errorf("a conflicting insert: %q %v", out, err)
	}

	// Finish ends the claim in flight, and only that.
	if err := notify.FinishWorkoutAlert(ctx, user, mark.ID, AlertSent, 2); err != nil {
		t.Fatal(err)
	}
	if r := ledgerRows(t, admin, user)[mark.ID]; r[0] != AlertSent || r[1] != 2 {
		t.Errorf("after Finish: %v", r)
	}
	if err := notify.FinishWorkoutAlert(ctx, user, mark.ID, AlertFailed, 0); err != nil {
		t.Fatal(err)
	}
	if r := ledgerRows(t, admin, user)[mark.ID]; r[0] != AlertSent || r[1] != 2 {
		t.Errorf("a finished row changed again: %v", r)
	}
	if err := notify.ReleaseWorkoutAlert(ctx, user, mark.ID, nil); err != nil { // sent: stays
		t.Fatal(err)
	}
	if _, ok := ledgerRows(t, admin, user)[mark.ID]; !ok {
		t.Error("Release deleted a sent row")
	}
	claim := AlertMark{WorkoutRef: WorkoutRef{ID: uuid.New(), Source: "Apple Watch", Activity: "walking", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour)}, Day: day}
	if out, err := notify.DecideWorkoutAlert(ctx, user, claim, func([]AlertMark, int) string { return AlertSending }); err != nil || out != AlertSending {
		t.Fatalf("claim: %q %v", out, err)
	}
	if err := notify.ReleaseWorkoutAlert(ctx, user, claim.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := ledgerRows(t, admin, user)[claim.ID]; ok {
		t.Error("Release left the claim")
	}
}

// A duplicate group is decided with its primary: W is claimed with G hidden behind it, so G has a ledger row
// (inherited, its own source and times) from the same transaction. Deleting W makes G visible, and the scan
// no longer offers it. The inherited row is no alert: the cap's count does not include it.
func TestDecideWorkoutAlertRecordsTheMembersAsInherited(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	notify := New(dupRolePool(t, admin, "ryft_notify"))
	now := time.Now().UTC().Truncate(time.Second)
	day := now.Format(time.DateOnly)
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)

	w := putAW(t, admin, user, aw{source: "Apple Watch", start: start, end: end, strain: ptr(12.8)})
	g := putAW(t, admin, user, aw{source: "Garmin Connect", start: start.Add(20 * time.Second), end: end, dupOf: &w})

	cands, err := notify.ActivityCandidates(ctx, user, now)
	if err != nil || len(cands) != 1 || cands[0].ID != w || len(cands[0].Members) != 1 {
		t.Fatalf("the scan: %v %v", idsOf(cands), err)
	}
	c := cands[0]
	mark := AlertMark{WorkoutRef: c.WorkoutRef, Day: day, Members: c.Members}
	out, err := notify.DecideWorkoutAlert(ctx, user, mark, func([]AlertMark, int) string { return AlertSending })
	if err != nil || out != AlertSending {
		t.Fatalf("decide: %q %v", out, err)
	}
	rows := ledgerRows(t, admin, user)
	if len(rows) != 2 || rows[w][0] != AlertSending || rows[g][0] != AlertInherited || rows[g][1] != 0 {
		t.Fatalf("ledger after the decision: %v", rows)
	}
	var source, activity string
	var gotStart, gotEnd time.Time
	var gotDay string
	if err := admin.QueryRow(ctx, `SELECT source, activity, start_at, end_at, to_char(day, 'YYYY-MM-DD') FROM workout_alerts
		WHERE user_id = $1 AND workout_id = $2`, user, g).Scan(&source, &activity, &gotStart, &gotEnd, &gotDay); err != nil {
		t.Fatal(err)
	}
	if source != "Garmin Connect" || activity != "running" || !gotStart.Equal(start.Add(20*time.Second)) || !gotEnd.Equal(end) || gotDay != day {
		t.Errorf("G's row: %s %s %v %v %s", source, activity, gotStart, gotEnd, gotDay)
	}

	// The cap counts sending and sent: W is the only one of the day, G's row is not.
	var counted []int
	for _, id := range []uuid.UUID{uuid.New(), uuid.New()} {
		if _, err := notify.DecideWorkoutAlert(ctx, user, AlertMark{WorkoutRef: WorkoutRef{ID: id, Source: "Other", Activity: "cycling",
			Start: start.Add(-6 * time.Hour), End: end.Add(-6 * time.Hour)}, Day: day}, func(_ []AlertMark, sent int) string {
			counted = append(counted, sent)
			return AlertMuted
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(counted, []int{1, 1}) {
		t.Errorf("alerts counted toward the cap: %v, want W only", counted)
	}

	// The primary is deleted: G is visible on its own and is decided already.
	if _, err := admin.Exec(ctx, `DELETE FROM workouts WHERE user_id = $1 AND id = $2`, user, w); err != nil {
		t.Fatal(err)
	}
	if again, err := notify.ActivityCandidates(ctx, user, now); err != nil || len(again) != 0 {
		t.Fatalf("G after its primary is gone: %v %v", idsOf(again), err)
	}

	// A member that has a row keeps it; a duplicate decision writes no member rows.
	h := uuid.New()
	if _, err := admin.Exec(ctx, `INSERT INTO workout_alerts (user_id, workout_id, source, activity, start_at, end_at, day, outcome)
		VALUES ($1, $2, 'Fitbit', 'running', $3, $4, $5::text::date, 'sent')`, user, h, start, end, day); err != nil {
		t.Fatal(err)
	}
	m2 := AlertMark{WorkoutRef: WorkoutRef{ID: uuid.New(), Source: "Apple Watch", Activity: "walking", Start: start.Add(-9 * time.Hour), End: end.Add(-9 * time.Hour)},
		Day: day, Members: []WorkoutRef{{ID: h, Source: "Fitbit", Activity: "running", Start: start, End: end}}}
	if out, err := notify.DecideWorkoutAlert(ctx, user, m2, func([]AlertMark, int) string { return AlertCapped }); err != nil || out != AlertCapped {
		t.Fatalf("decide with an existing member: %q %v", out, err)
	}
	if r := ledgerRows(t, admin, user)[h]; r[0] != AlertSent {
		t.Errorf("a member with a row keeps it: %v", r)
	}
	dup := AlertMark{WorkoutRef: WorkoutRef{ID: uuid.New(), Source: "Apple Watch", Activity: "walking", Start: start.Add(-9 * time.Hour), End: end.Add(-9 * time.Hour)},
		Day: day, Members: []WorkoutRef{{ID: uuid.New(), Source: "Fitbit", Activity: "walking", Start: start, End: end}}}
	before := len(ledgerRows(t, admin, user))
	if out, err := notify.DecideWorkoutAlert(ctx, user, dup, func([]AlertMark, int) string { return AlertDuplicate }); err != nil || out != AlertDuplicate {
		t.Fatalf("a duplicate: %q %v", out, err)
	}
	if n := len(ledgerRows(t, admin, user)); n != before {
		t.Errorf("a duplicate decision wrote rows: %d, was %d", n, before)
	}
}

// Releasing a claim gives the whole group back: the primary's sending row and the members' inherited rows
// written with it. Left behind, a member row would make the retry inherit its own primary. A release of a
// row that is not sending (sent) leaves the members, and releasing twice is harmless.
func TestReleaseWorkoutAlertGivesTheGroupBack(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	notify := New(dupRolePool(t, admin, "ryft_notify"))
	now := time.Now().UTC().Truncate(time.Second)
	day := now.Format(time.DateOnly)
	start, end := now.Add(-2*time.Hour), now.Add(-time.Hour)

	w := putAW(t, admin, user, aw{source: "Apple Watch", start: start, end: end, strain: ptr(12.8)})
	g := putAW(t, admin, user, aw{source: "Garmin Connect", start: start.Add(20 * time.Second), end: end, dupOf: &w})
	unrelated := uuid.New() // an inherited row of another group: not ours to delete
	if _, err := admin.Exec(ctx, `INSERT INTO workout_alerts (user_id, workout_id, source, activity, start_at, end_at, day, outcome)
		VALUES ($1, $2, 'Fitbit', 'cycling', $3, $4, $5::text::date, 'inherited')`, user, unrelated, start.Add(-6*time.Hour), end.Add(-6*time.Hour), day); err != nil {
		t.Fatal(err)
	}

	cands, err := notify.ActivityCandidates(ctx, user, now)
	if err != nil || len(cands) != 1 || cands[0].ID != w || len(cands[0].Members) != 1 {
		t.Fatalf("the scan: %v %v", idsOf(cands), err)
	}
	c := cands[0]
	mark := AlertMark{WorkoutRef: c.WorkoutRef, Day: day, Members: c.Members}
	members := []uuid.UUID{c.Members[0].ID}
	claim := func() {
		t.Helper()
		if out, err := notify.DecideWorkoutAlert(ctx, user, mark, func([]AlertMark, int) string { return AlertSending }); err != nil || out != AlertSending {
			t.Fatalf("claim: %q %v", out, err)
		}
	}

	claim()
	if rows := ledgerRows(t, admin, user); len(rows) != 3 || rows[g][0] != AlertInherited {
		t.Fatalf("after the claim: %v", rows)
	}
	for range 2 { // the second release finds nothing: idempotent
		if err := notify.ReleaseWorkoutAlert(ctx, user, w, members); err != nil {
			t.Fatal(err)
		}
		rows := ledgerRows(t, admin, user)
		if _, ok := rows[w]; ok || len(rows) != 1 || rows[unrelated][0] != AlertInherited {
			t.Fatalf("after the release: %v, want only the unrelated row", rows)
		}
	}

	// The retry decides the workout again, as a fresh claim.
	if again, err := notify.ActivityCandidates(ctx, user, now); err != nil || len(again) != 1 || again[0].ID != w {
		t.Fatalf("the scan after the release: %v %v", idsOf(again), err)
	}
	claim()
	if rows := ledgerRows(t, admin, user); rows[w][0] != AlertSending || rows[g][0] != AlertInherited {
		t.Fatalf("after the second claim: %v", rows)
	}

	// Once the send is recorded, a release (a late retry) deletes neither the row nor its members.
	if err := notify.FinishWorkoutAlert(ctx, user, w, AlertSent, 1); err != nil {
		t.Fatal(err)
	}
	if err := notify.ReleaseWorkoutAlert(ctx, user, w, members); err != nil {
		t.Fatal(err)
	}
	if rows := ledgerRows(t, admin, user); rows[w][0] != AlertSent || rows[g][0] != AlertInherited {
		t.Errorf("a release after the send changed the group: %v", rows)
	}
}

// ryft_notify reads no health value of a workout and writes none; ryft_ingest has nothing to do with the ledger.
func TestAlertGrantsOfTheServiceRoles(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	now := time.Now().UTC().Truncate(time.Second)
	id := putAW(t, admin, user, aw{source: "Apple Watch", start: now.Add(-2 * time.Hour), end: now.Add(-time.Hour), strain: ptr(8.0)})

	notify := dupRolePool(t, admin, "ryft_notify")
	var hr *float64
	if err := notify.QueryRow(ctx, `SELECT avg_hr FROM workouts WHERE user_id = $1 AND id = $2`, user, id).Scan(&hr); sqlState(err) != "42501" {
		t.Errorf("ryft_notify reads avg_hr: %v, want 42501", err)
	}
	if _, err := notify.Exec(ctx, `UPDATE workouts SET strain = 1 WHERE user_id = $1 AND id = $2`, user, id); sqlState(err) != "42501" {
		t.Errorf("ryft_notify updates workouts: %v, want 42501", err)
	}
	ingest := dupRolePool(t, admin, "ryft_ingest")
	if _, err := ingest.Exec(ctx, `SELECT count(*) FROM workout_alerts`); sqlState(err) != "42501" {
		t.Errorf("ryft_ingest reads the ledger: %v, want 42501", err)
	}
}

// The merge and the account deletion run as ryft_api: its grants on the ledger (select, delete, update of
// user_id) are enough for both, and the mark of a workout moves to the kept account.
func TestAlertLedgerMergesAndErasesAsTheAPIRole(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	from, to := dupUser(t, st), dupUser(t, st)
	notify, api := New(dupRolePool(t, admin, "ryft_notify")), New(dupRolePool(t, admin, "ryft_api"))
	now := time.Now().UTC().Truncate(time.Second)
	shared, own := uuid.New(), uuid.New()
	for _, d := range []struct{ user, id uuid.UUID }{{from, shared}, {to, shared}, {from, own}} {
		mark := AlertMark{WorkoutRef: WorkoutRef{ID: d.id, Source: "Apple Watch", Activity: "running", Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)},
			Day: now.Format(time.DateOnly)}
		if out, err := notify.DecideWorkoutAlert(ctx, d.user, mark, func([]AlertMark, int) string { return AlertSent }); err != nil || out != AlertSent {
			t.Fatalf("decide: %q %v", out, err)
		}
	}
	res, err := api.MergeUsers(ctx, from, to, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range res.Tables {
		if tb.Table == "workout_alerts" && (tb.Moved != 1 || tb.Dropped != 1) {
			t.Errorf("merge plan for the ledger: moved %d dropped %d, want 1 and 1", tb.Moved, tb.Dropped)
		}
	}
	rows := ledgerRows(t, admin, to)
	if _, ok := rows[own]; !ok || len(rows) != 2 {
		t.Errorf("to's ledger after the merge: %v", rows)
	}
	if err := api.DeleteUser(ctx, from); err != nil {
		t.Fatal(err)
	}
	if n := len(ledgerRows(t, admin, from)); n != 0 {
		t.Errorf("from's ledger after the deletion: %d rows", n)
	}
	if err := api.DeleteUser(ctx, to); err != nil {
		t.Fatal(err)
	}
	if n := len(ledgerRows(t, admin, to)); n != 0 {
		t.Errorf("to's ledger after the deletion: %d rows", n)
	}
}

// The api's workout reads (GET /v1/workouts, GET /v1/workouts/{id}) carry source_bundle_id, which the
// export is built from, and omit it when none is stored. The install (device_id) is in no response.
func TestWorkoutReadsReturnTheSourceBundle(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	api := New(dupRolePool(t, admin, "ryft_api"))
	device := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	with := putAW(t, admin, user, aw{source: "Garmin Connect", start: now.Add(-3 * time.Hour), end: now.Add(-2 * time.Hour), bundle: ptr("com.garmin.connect.mobile"), device: &device})
	without := putAW(t, admin, user, aw{source: "Apple Watch", start: now.Add(-90 * time.Minute), end: now.Add(-30 * time.Minute)})

	list, err := api.ListWorkoutsBetween(ctx, user, now.Add(-4*time.Hour), now, false)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	bundles := map[uuid.UUID]*string{}
	for _, w := range list {
		bundles[w.ID] = w.SourceBundleID
	}
	if b := bundles[with]; b == nil || *b != "com.garmin.connect.mobile" || bundles[without] != nil {
		t.Errorf("list: bundles %v %v", bundles[with], bundles[without])
	}
	for id, want := range map[uuid.UUID]string{with: "com.garmin.connect.mobile", without: ""} {
		row, err := api.Workout(ctx, user, id)
		if err != nil {
			t.Fatal(err)
		}
		if (want == "") != (row.SourceBundleID == nil) || (want != "" && *row.SourceBundleID != want) {
			t.Errorf("detail of %s: bundle %v, want %q", id, row.SourceBundleID, want)
		}
		raw, _ := json.Marshal(row.Workout)
		if bytes.Contains(raw, []byte("device_id")) || bytes.Contains(raw, []byte(device.String())) ||
			bytes.Contains(raw, []byte("source_bundle_id")) != (want != "") {
			t.Errorf("detail JSON of %s: %s", id, raw)
		}
	}
}

// UpsertWorkouts: the device that first stored a row stays on it; the bundle is filled once.
func TestUpsertWorkoutsKeepsTheFirstDeviceAndFillsTheBundleOnce(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	d1, d2 := uuid.New(), uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	w := model.Workout{ID: uuid.New(), Activity: "running", Start: now.Add(-time.Hour), End: now, Source: "Watch"}

	read := func(id uuid.UUID) (device *uuid.UUID, bundle *string) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT device_id, source_bundle_id FROM workouts WHERE user_id = $1 AND id = $2`, user, id).Scan(&device, &bundle); err != nil {
			t.Fatal(err)
		}
		return device, bundle
	}
	upsert := func(device uuid.UUID, w model.Workout, want int, step string) {
		t.Helper()
		if n, err := st.UpsertWorkouts(ctx, user, device, []model.Workout{w}); err != nil || n != want {
			t.Fatalf("%s: %d %v, want %d", step, n, err, want)
		}
	}
	upsert(d1, w, 1, "first sync, no bundle")
	if d, b := read(w.ID); d == nil || *d != d1 || b != nil {
		t.Fatalf("first store: device %v bundle %v", d, b)
	}
	upsert(d2, w, 0, "the same workout from another device") // not a change: not counted, device kept
	if d, _ := read(w.ID); d == nil || *d != d1 {
		t.Fatalf("the first device is kept: %v", d)
	}
	w.SourceBundleID = ptr("com.garmin.connect.mobile")
	upsert(d2, w, 1, "the bundle arrives") // filled once, counted once
	if d, b := read(w.ID); *d != d1 || b == nil || *b != "com.garmin.connect.mobile" {
		t.Fatalf("bundle filled: device %v bundle %v", d, b)
	}
	upsert(d2, w, 0, "the same again")
	w.SourceBundleID = ptr("com.other")
	upsert(d2, w, 0, "another bundle later") // never changed once stored
	if _, b := read(w.ID); *b != "com.garmin.connect.mobile" {
		t.Fatalf("a stored bundle is kept: %v", *b)
	}
	// A real change rewrites the row and keeps both.
	kcal := 410.0
	w.EnergyKcal = &kcal
	upsert(d2, w, 1, "a changed workout")
	if d, b := read(w.ID); *d != d1 || *b != "com.garmin.connect.mobile" {
		t.Fatalf("after a change: device %v bundle %v", d, b)
	}
	// No device (uuid.Nil) stores NULL.
	n := model.Workout{ID: uuid.New(), Activity: "running", Start: now.Add(-3 * time.Hour), End: now.Add(-2 * time.Hour), Source: "Watch"}
	upsert(uuid.Nil, n, 1, "no device")
	if d, _ := read(n.ID); d != nil {
		t.Fatalf("uuid.Nil stores NULL: %v", d)
	}
}
