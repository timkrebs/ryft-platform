package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

func inboxID(t *testing.T, pool *pgxpool.Pool, kind, rec string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT id FROM provider_inbox WHERE kind=$1 AND record_id=$2`, kind, rec).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func nightRecord(user uuid.UUID, id int64, night time.Time, state string, stages ...string) DirectRecord {
	r := DirectRecord{InboxID: id, Revision: 1, UserID: user, Provider: testProvider, Kind: connect.KindSleep, RecordID: "n1", NightState: state, Loc: time.UTC}
	at := night
	for _, st := range stages {
		r.Sleep = append(r.Sleep, model.SleepSegment{Start: at, End: at.Add(2 * time.Hour), Stage: st})
		at = at.Add(2 * time.Hour)
	}
	return r
}

// D100: the same event three times leaves the same rows, one processed row and the queue as after the first; a
// changed re-push replaces the night.
func TestApplyDirectIsIdempotentAndReplacesTheNight(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	night := time.Now().Add(-12 * time.Hour).UTC().Truncate(time.Second)
	store1(t, st, item("sleep", "n1", "p1", 1, `{"v":1}`))
	id := inboxID(t, pool, "sleep", "n1")
	events := 0
	emit := func(tx pgx.Tx, days []string) error { events++; return nil }
	r := nightRecord(u, id, night, model.RecordProvisional, "core", "deep", "rem")
	r.Emit = emit
	first, err := st.ApplyDirect(ctx, r)
	if err != nil || first.Outcome != DirectApplied || len(first.Days) == 0 {
		t.Fatalf("first: %+v %v", first, err)
	}
	queued := queuedDays(t, pool, u)
	for i := 0; i < 2; i++ {
		again, err := st.ApplyDirect(ctx, r)
		if err != nil || again.Outcome != DirectNotPending {
			t.Fatalf("repeat %d: %+v %v", i, again, err)
		}
	}
	if events != 1 {
		t.Fatalf("%d events for one record delivered three times", events)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND origin='direct' AND vendor='fake' AND source='ryft-direct-fake' AND record_id='sleep:n1' AND record_state='provisional'`, u); n != 3 {
		t.Fatalf("rows %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE state='processed' AND payload IS NULL`); n != 1 {
		t.Fatal("not exactly one processed row")
	}
	if got := queuedDays(t, pool, u); len(got) != len(queued) {
		t.Fatalf("queue changed: %v -> %v", queued, got)
	}
	var lastData *time.Time
	if err := pool.QueryRow(ctx, `SELECT last_data_at FROM connections WHERE user_id=$1`, u).Scan(&lastData); err != nil || lastData == nil {
		t.Fatalf("last_data_at %v %v", lastData, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM user_sources WHERE user_id=$1 AND name='ryft-direct-fake' AND kind='sleep' AND origin='direct'`, u); n != 1 {
		t.Fatal("the direct source is not noted")
	}
	// The same record re-sent final, with other stages: the night is replaced, not added to.
	if got := store1(t, st, item("sleep", "n1", "p1", 2, `{"v":2}`)); got != HookUpdated {
		t.Fatal(got)
	}
	r2 := nightRecord(u, id, night, model.RecordFinal, "core", "deep")
	r2.Revision = 2 // the re-push is the row's second revision
	if res, err := st.ApplyDirect(ctx, r2); err != nil || res.Outcome != DirectApplied {
		t.Fatalf("re-push: %+v %v", res, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND origin='direct'`, u); n != 2 {
		t.Fatalf("a replaced night holds %d rows, want 2", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND record_state='final'`, u); n != 2 {
		t.Fatal("the new state is not on the rows")
	}
}

// P2-3: records given as Also are stored in the same transaction under one Emit with the union of the days; a row
// handled meanwhile is skipped, and the others' own deliveries find them processed.
func TestApplyDirectStoresAlsoRecordsInOneEvent(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	now := time.Now().UTC().Truncate(time.Second)
	var recs []DirectRecord
	for i, rec := range []string{"a", "b", "c"} {
		store1(t, st, item("sleep", rec, "p1", 1, `{"v":1}`))
		r := nightRecord(u, inboxID(t, pool, "sleep", rec), now.Add(-time.Duration(12+24*i)*time.Hour), model.RecordFinal, "core", "deep")
		r.RecordID = rec
		recs = append(recs, r)
	}
	// "b" is handled by another delivery before the batch's lock.
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET state='processed', payload=NULL WHERE id=$1`, recs[1].InboxID); err != nil {
		t.Fatal(err)
	}
	var emitted [][]string
	main := recs[0]
	main.Emit = func(tx pgx.Tx, days []string) error { emitted = append(emitted, days); return nil }
	main.Also = []DirectRecord{recs[1], recs[2]}
	res, err := st.ApplyDirect(ctx, main)
	if err != nil || res.Outcome != DirectApplied {
		t.Fatalf("%+v %v", res, err)
	}
	if len(res.Also) != 1 || res.Also[0] != recs[2].InboxID {
		t.Fatalf("Also %v, want only the pending record %d", res.Also, recs[2].InboxID)
	}
	if len(emitted) != 1 || len(emitted[0]) < 2 {
		t.Fatalf("emitted %v: want one event with the days of both records", emitted)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND origin='direct' AND record_id IN ('sleep:a','sleep:c')`, u); n != 4 {
		t.Fatalf("%d rows of the two stored records", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND record_id='sleep:b'`, u); n != 0 {
		t.Fatal("the record handled meanwhile was stored")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE state='processed' AND payload IS NULL`); n != 3 {
		t.Fatalf("%d processed rows, want 3 (a, b by the other delivery, c)", n)
	}
	// Their own deliveries find them handled, and another account's record in Also is never stored.
	again, err := st.ApplyDirect(ctx, recs[2])
	if err != nil || again.Outcome != DirectNotPending {
		t.Fatalf("own delivery: %+v %v", again, err)
	}
	other := newAccount(t, st)
	foreign := recs[2]
	foreign.UserID = other
	store1(t, st, item("sleep", "d", "p1", 1, `{"v":1}`))
	mine := nightRecord(u, inboxID(t, pool, "sleep", "d"), now.Add(-100*time.Hour), model.RecordFinal, "core")
	mine.RecordID = "d"
	mine.Also = []DirectRecord{foreign}
	if res, err := st.ApplyDirect(ctx, mine); err != nil || len(res.Also) != 0 {
		t.Fatalf("a foreign record was batched: %+v %v", res, err)
	}
}

func TestApplyDirectDropsARowWhoseLinkEnded(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	store1(t, st, item("sleep", "n1", "p1", 1, `{}`))
	id := inboxID(t, pool, "sleep", "n1")
	if _, err := pool.Exec(ctx, `DELETE FROM connections WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	res, err := st.ApplyDirect(ctx, nightRecord(u, id, time.Now().Add(-9*time.Hour).UTC(), model.RecordFinal, "core"))
	if err != nil || res.Outcome != DirectNoConnection {
		t.Fatalf("%+v %v", res, err)
	}
	var state, code string
	if err := pool.QueryRow(ctx, `SELECT state, error FROM provider_inbox WHERE id=$1`, id).Scan(&state, &code); err != nil || state != "dropped" || code != "no_connection" {
		t.Fatalf("%s %s %v", state, code, err)
	}
	if s, sl, _ := directRows(t, pool, u); s+sl != 0 {
		t.Fatal("rows stored for an ended link")
	}
}

func TestApplyDeregistrationRevokesAndDeletes(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	pushNight(t, st, pool, u, "p1", "n", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	store1(t, st, item("sleep", "late", "p1", 1, `{}`)) // another waiting row: deleted
	store1(t, st, item("deregistration", "d1", "p1", 1, `{"kind":"deregistration"}`))
	id := inboxID(t, pool, "deregistration", "d1")
	var days []string
	res, err := st.ApplyDeregistration(ctx, id, 1, u, testProvider, func(tx pgx.Tx, d []string) error { days = d; return nil })
	if err != nil || res.Outcome != DirectApplied || len(days) == 0 {
		t.Fatalf("%+v %v %v", res, err, days)
	}
	rows, _ := st.Connections(ctx, u)
	if len(rows) != 1 || rows[0].Status != model.ConnectionRevoked || rows[0].ProviderUserID != nil || len(rows[0].Permissions) != 0 {
		t.Fatalf("connection %+v", rows)
	}
	if s, sl, src := directRows(t, pool, u); s+sl+src != 0 {
		t.Fatal("direct data left")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE user_id=$1`, u); n != 1 {
		t.Fatalf("inbox rows %d, want the processed notice only", n)
	}
	// The tokens stay for api's purge; a repeat does nothing.
	if n := countRows(t, pool, `SELECT count(*) FROM connection_tokens WHERE user_id=$1`, u); n != 1 {
		t.Fatal("ingest touched the tokens")
	}
	if again, err := st.ApplyDeregistration(ctx, id, 1, u, testProvider, nil); err != nil || again.Outcome != DirectNotPending {
		t.Fatalf("repeat %+v %v", again, err)
	}
	// No record of a revoked link is applied afterwards.
	if got := store1(t, st, item("sleep", "after", "p1", 1, `{}`)); got != HookUnknownUser {
		t.Fatalf("push after revocation: %s", got)
	}
}

func TestApplyPermissions(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	status := func() (string, *string) {
		var s string
		var reason *string
		if err := pool.QueryRow(ctx, `SELECT status, status_reason FROM connections WHERE user_id=$1`, u).Scan(&s, &reason); err != nil {
			t.Fatal(err)
		}
		return s, reason
	}
	apply := func(rec string, missing bool) {
		store1(t, st, item("permissions", rec, "p1", 1, `{"r":"`+rec+`"}`))
		if o, err := st.ApplyPermissions(ctx, inboxID(t, pool, "permissions", rec), 1, u, testProvider, missing); err != nil || o != DirectApplied {
			t.Fatalf("%s %v", o, err)
		}
	}
	apply("a", true)
	if s, r := status(); s != "needs_reauth" || r == nil || *r != "permissions" {
		t.Fatalf("after a missing permission: %s %v", s, r)
	}
	apply("b", false)
	if s, r := status(); s != "connected" || r != nil {
		t.Fatalf("after it is back: %s %v", s, r)
	}
	// needs_reauth for a failed refresh is not undone by a permissions notice.
	if err := st.NeedsReauth(ctx, u, testProvider, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	apply("c", false)
	if s, r := status(); s != "needs_reauth" || r == nil || *r != "refresh_failed" {
		t.Fatalf("a refresh failure was undone: %s %v", s, r)
	}
}

// S2: after each of the four paths that end a connection, no direct row of that vendor is left in the three
// tables.
func TestEveryEndOfAConnectionDeletesTheSameDirectData(t *testing.T) {
	night := time.Now().Add(-10 * time.Hour).UTC().Truncate(time.Second)
	left := func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) {
		t.Helper()
		if s, sl, src := directRows(t, pool, u); s+sl+src != 0 {
			t.Errorf("direct rows left: %d samples, %d sleep, %d sources", s, sl, src)
		}
	}
	t.Run("disconnect", func(t *testing.T) {
		st, pool := testStore(t)
		u := newAccount(t, st)
		connectAs(t, st, u, "p1")
		pushNight(t, st, pool, u, "p1", "n", night)
		if _, err := st.Disconnect(context.Background(), u, testProvider, nil); err != nil {
			t.Fatal(err)
		}
		left(t, pool, u)
	})
	t.Run("replace", func(t *testing.T) {
		st, pool := testStore(t)
		u := newAccount(t, st)
		connectAs(t, st, u, "p1")
		pushNight(t, st, pool, u, "p1", "n", night)
		connectAs(t, st, u, "p2")
		left(t, pool, u)
	})
	t.Run("relink", func(t *testing.T) {
		st, pool := testStore(t)
		u, v := newAccount(t, st), newAccount(t, st)
		connectAs(t, st, u, "p1")
		pushNight(t, st, pool, u, "p1", "n", night)
		connectAs(t, st, v, "p1")
		left(t, pool, u)
	})
	t.Run("deregistration notice", func(t *testing.T) {
		st, pool := testStore(t)
		u := newAccount(t, st)
		connectAs(t, st, u, "p1")
		pushNight(t, st, pool, u, "p1", "n", night)
		store1(t, st, item("deregistration", "d", "p1", 1, `{}`))
		if _, err := st.ApplyDeregistration(context.Background(), inboxID(t, pool, "deregistration", "d"), 1, u, testProvider, nil); err != nil {
			t.Fatal(err)
		}
		left(t, pool, u)
	})
}

// A sync record cannot touch a direct row: the reserved source is dropped at ingest, and a HealthKit copy of the
// same vendor lives beside the direct one.
func TestHealthKitUpsertsNameTheVendor(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	bundle := "com.garmin.connect.mobile"
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := st.UpsertSleep(ctx, u, []model.SleepSegment{{Start: now.Add(-8 * time.Hour), End: now, Stage: "core", Source: "Garmin Connect", SourceBundleID: &bundle}}); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := st.UpsertSamples(ctx, u, []model.Sample{
		{Type: "hrv_sdnn", Start: now, End: now, Value: 40, Unit: "ms", Source: "Garmin Connect", SourceBundleID: &bundle, ID: &id},
		{Type: "steps", Start: now, End: now, Value: 40, Unit: "count", Source: "iPhone"}}); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND vendor='garmin' AND origin='healthkit'`, u); n != 1 {
		t.Fatal("sleep vendor")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM samples WHERE user_id=$1 AND vendor='garmin' AND origin='healthkit'`, u); n != 1 {
		t.Fatal("sample vendor")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM samples WHERE user_id=$1 AND vendor IS NULL`, u); n != 1 {
		t.Fatal("an unknown bundle must leave the vendor NULL")
	}
}

func TestUserSourcesShowOrigin(t *testing.T) {
	st, pool := testStore(t)
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	pushNight(t, st, pool, u, "p1", "n", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	if err := st.UpsertSources(context.Background(), u, []SourceSeen{{Name: "Garmin Connect", Kind: model.KindSleep, LastSeen: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.UserSources(context.Background(), u, time.Now().Add(-48*time.Hour))
	if err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	seen := map[string]string{}
	for _, s := range got {
		seen[s.Name] = s.Origin
	}
	if seen["Garmin Connect"] != "healthkit" || seen["ryft-direct-fake"] != "direct" {
		t.Fatalf("origins %v", seen)
	}
}
