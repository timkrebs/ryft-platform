package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

// The store's side of the sync status (0067, D297, D300): CloseQueue, MarkScored's closed_reason, ExpectedScoreSec
// without closed rows, and SyncConnections. Opt-in like the rest of the package (RYFT_TEST_POSTGRES_URL).

type queueRow struct {
	scored *time.Time
	reason *string
}

func queueRowOf(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, day string) queueRow {
	t.Helper()
	var r queueRow
	if err := pool.QueryRow(context.Background(), `SELECT scored_at, closed_reason FROM score_queue WHERE user_id = $1 AND day = $2::date`, user, day).
		Scan(&r.scored, &r.reason); err != nil {
		t.Fatal(err)
	}
	return r
}

func newBareAccount(t *testing.T, st *Store) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := st.EnsureUser(context.Background(), Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	return id
}

// CloseQueue closes the user's unscored rows, with the reason, and nothing else.
func TestCloseQueueTouchesOnlyTheUsersUnscoredRows(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b := newBareAccount(t, st), newBareAccount(t, st)
	if err := st.QueueDays(ctx, a, []string{"2026-11-24", "2026-11-25", "2026-11-26"}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueDays(ctx, b, []string{"2026-11-25"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.MarkScored(ctx, a, "2026-11-24", time.Now().Add(time.Second)); err != nil || !ok { // scored for real before
		t.Fatalf("mark: %v %v", ok, err)
	}
	scoredBefore := queueRowOf(t, pool, a, "2026-11-24")

	before := time.Now().Add(time.Minute)
	n, err := st.CloseQueue(ctx, a, QueueClosedNoConsent, before)
	if err != nil || n != 2 {
		t.Fatalf("closed %d rows: %v, want the two unscored ones", n, err)
	}
	for _, day := range []string{"2026-11-25", "2026-11-26"} {
		if r := queueRowOf(t, pool, a, day); r.scored == nil || r.reason == nil || *r.reason != "no_consent" {
			t.Fatalf("%s: %+v, want scored_at set and closed_reason no_consent", day, r)
		}
	}
	// The day scored for real stays as it was: no reason, the same scored_at.
	if r := queueRowOf(t, pool, a, "2026-11-24"); r.reason != nil || r.scored == nil || !r.scored.Equal(*scoredBefore.scored) {
		t.Fatalf("a scored day: %+v, was %+v", r, scoredBefore)
	}
	// Another account's row is untouched, and still pending.
	if r := queueRowOf(t, pool, b, "2026-11-25"); r.scored != nil || r.reason != nil {
		t.Fatalf("the other account: %+v", r)
	}
	if p, err := st.PendingDays(ctx, a); err != nil || len(p) != 0 {
		t.Fatalf("pending after the close: %v %v", p, err)
	}
	if p, err := st.PendingDays(ctx, b); err != nil || len(p) != 1 {
		t.Fatalf("the other account's pending: %v %v", p, err)
	}

	// Closing twice is a no-op: nothing closed, nothing moved.
	closed := queueRowOf(t, pool, a, "2026-11-25")
	if n, err := st.CloseQueue(ctx, a, QueueClosedNoConsent, before); err != nil || n != 0 {
		t.Fatalf("the second close: %d %v", n, err)
	}
	if again := queueRowOf(t, pool, a, "2026-11-25"); !again.scored.Equal(*closed.scored) || *again.reason != "no_consent" {
		t.Fatalf("the second close changed a closed row: %+v, was %+v", again, closed)
	}
	// An account with no queue rows.
	if n, err := st.CloseQueue(ctx, uuid.New(), QueueClosedNoConsent, before); err != nil || n != 0 {
		t.Fatalf("an unknown account: %d %v", n, err)
	}
}

// A row queued after the cutoff stays open: a re-consent and a sync racing the close (D300). The cutoff is
// inclusive, like MarkScored's started.
func TestCloseQueueLeavesARowQueuedAfterTheCutoff(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newBareAccount(t, st)
	if err := st.QueueDays(ctx, u, []string{"2026-11-24", "2026-11-25", "2026-11-26"}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	set := func(day string, queued time.Time) {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE score_queue SET queued_at = $3 WHERE user_id = $1 AND day = $2::date`, u, day, queued); err != nil {
			t.Fatal(err)
		}
	}
	set("2026-11-24", cutoff.Add(-time.Minute)) // before the cutoff
	set("2026-11-25", cutoff)                   // at it
	set("2026-11-26", cutoff.Add(time.Minute))  // queued again after the account was read

	n, err := st.CloseQueue(ctx, u, QueueClosedNoConsent, cutoff)
	if err != nil || n != 2 {
		t.Fatalf("closed %d rows: %v, want the two queued at or before the cutoff", n, err)
	}
	for _, day := range []string{"2026-11-24", "2026-11-25"} {
		if r := queueRowOf(t, pool, u, day); r.scored == nil || r.reason == nil || *r.reason != "no_consent" {
			t.Fatalf("%s: %+v, want it closed", day, r)
		}
	}
	if r := queueRowOf(t, pool, u, "2026-11-26"); r.scored != nil || r.reason != nil {
		t.Fatalf("the row queued after the cutoff: %+v, want it open", r)
	}
	if p, err := st.PendingDays(ctx, u); err != nil || len(p) != 1 || p[0] != "2026-11-26" {
		t.Fatalf("pending after the close: %v %v", p, err)
	}
}

// The reasons are a closed set: the database refuses any other, and the failed statement closes nothing.
func TestCloseQueueRefusesAnUnknownReason(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newBareAccount(t, st)
	if err := st.QueueDays(ctx, u, []string{"2026-11-26"}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CloseQueue(ctx, u, "because", time.Now().Add(time.Minute)); err == nil || n != 0 {
		t.Fatalf("an unknown reason: %d %v", n, err)
	}
	if r := queueRowOf(t, pool, u, "2026-11-26"); r.scored != nil || r.reason != nil {
		t.Fatalf("the row after the refused close: %+v", r)
	}
}

// A day really scored carries no reason: MarkScored clears a stale closed_reason (a re-queue, scored_at NULL again,
// may leave one), and a closed row is no longer pending, so MarkScored leaves it alone.
func TestMarkScoredClearsClosedReason(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newBareAccount(t, st)
	day := "2026-11-26"
	if err := st.QueueDays(ctx, u, []string{day}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CloseQueue(ctx, u, QueueClosedNoConsent, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A closed row is not pending: scoring's mark finds nothing to finish and keeps it closed.
	if _, ok, err := st.MarkScored(ctx, u, day, time.Now().Add(time.Second)); err != nil || ok {
		t.Fatalf("mark of a closed row: %v %v", ok, err)
	}
	if r := queueRowOf(t, pool, u, day); r.reason == nil {
		t.Fatalf("the closed row lost its reason: %+v", r)
	}

	// Consent given again, a sync queues the day: pending, whatever the reason column still holds.
	if err := st.QueueDays(ctx, u, []string{day}); err != nil {
		t.Fatal(err)
	}
	if p, err := st.PendingDays(ctx, u); err != nil || len(p) != 1 || p[0] != day {
		t.Fatalf("pending after a re-queue: %v %v", p, err)
	}
	// Make the stale value certain, as a re-queue is allowed to leave it.
	if _, err := pool.Exec(ctx, `UPDATE score_queue SET closed_reason = 'no_consent' WHERE user_id = $1 AND day = $2::date`, u, day); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.MarkScored(ctx, u, day, time.Now().Add(time.Second)); err != nil || !ok {
		t.Fatalf("mark: %v %v", ok, err)
	}
	if r := queueRowOf(t, pool, u, day); r.scored == nil || r.reason != nil {
		t.Fatalf("a day really scored: %+v, want scored_at set and no closed_reason", r)
	}
}

// ExpectedScoreSec is the p95 of sync-to-score: a row closed without scoring is no such time.
func TestExpectedScoreSecIgnoresClosedRows(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newBareAccount(t, st)
	// Ten closed rows, "latency" five hours: they would be the p95 if they counted, and they are enough to count.
	if _, err := pool.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at, closed_reason)
		SELECT $1, DATE '2026-01-01' + n, now() - interval '5 hours', now() - interval '1 minute', 'no_consent' FROM generate_series(1, 10) n`, u); err != nil {
		t.Fatal(err)
	}
	if sec, err := st.ExpectedScoreSec(ctx); err != nil || sec != DefaultExpectedSec {
		t.Fatalf("only closed rows is no history: %d %v, want %d", sec, err, DefaultExpectedSec)
	}
	// Ten scored rows of 1..10 s next to them: the p95 of those alone.
	if _, err := pool.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at)
		SELECT $1, DATE '2026-02-01' + n, now() - interval '30 seconds', now() - interval '30 seconds' + (n || ' seconds')::interval
		FROM generate_series(1, 10) n`, u); err != nil {
		t.Fatal(err)
	}
	if sec, err := st.ExpectedScoreSec(ctx); err != nil || sec != 10 {
		t.Fatalf("p95 of 1..10 s next to closed rows: %d %v, want 10", sec, err)
	}
}

// SyncConnections lists connected and needs_reauth connections by provider, with the earliest delivery still
// pending, and nothing of another account's, a failed or a processed delivery, or a revoked connection.
func TestSyncConnections(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u, other := newAccount(t, st), newAccount(t, st)
	if rows, err := st.SyncConnections(ctx, u); err != nil || len(rows) != 0 {
		t.Fatalf("no connection: %+v %v", rows, err)
	}
	connectAs(t, st, u, "puid-sync-u")
	connectAs(t, st, other, "puid-sync-other")
	if _, err := pool.Exec(ctx, `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1, 'garmin', 'puid-sync-garmin', 'connected', '{}', '2026-10-26')`, u); err != nil {
		t.Fatal(err)
	}

	rows, err := st.SyncConnections(ctx, u)
	if err != nil || len(rows) != 2 || rows[0].Provider != testProvider || rows[1].Provider != "garmin" {
		t.Fatalf("by provider: %+v %v", rows, err)
	}
	for _, r := range rows {
		if r.Status != model.ConnectionConnected || r.LastDataAt != nil || r.PendingSince != nil {
			t.Fatalf("nothing delivered yet: %+v", r)
		}
	}

	push := func(user uuid.UUID, puid string) int64 {
		t.Helper()
		rec := "rec-" + uuid.NewString()
		outs, err := st.StoreInbox(ctx, testProvider, "2026-10-26", []HookItem{{Kind: connect.KindSleep, RecordID: rec, ProviderUserID: puid, Version: 1,
			Payload: []byte(`{"n":1}`)}}, nil)
		if err != nil || len(outs) != 1 || outs[0] != HookStored {
			t.Fatalf("store: %v %v", outs, err)
		}
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM provider_inbox WHERE provider = $1 AND record_id = $2`, testProvider, rec).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	at := func(id int64, ago time.Duration) time.Time {
		t.Helper()
		var got time.Time
		if err := pool.QueryRow(ctx, `UPDATE provider_inbox SET received_at = $2 WHERE id = $1 RETURNING received_at`, id, time.Now().Add(-ago)).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got.UTC()
	}
	pendingSince := func(user uuid.UUID) *time.Time {
		t.Helper()
		rows, err := st.SyncConnections(ctx, user)
		if err != nil || len(rows) == 0 || rows[0].Provider != testProvider {
			t.Fatalf("rows: %+v %v", rows, err)
		}
		return rows[0].PendingSince
	}

	older, newer := push(u, "puid-sync-u"), push(u, "puid-sync-u")
	otherRow := push(other, "puid-sync-other")
	olderAt, newerAt := at(older, 5*time.Minute), at(newer, time.Minute)
	at(otherRow, 20*time.Minute) // earlier than both, another account's: no leak
	if got := pendingSince(u); got == nil || !got.Equal(olderAt) {
		t.Fatalf("the earliest waiting delivery: %v, want %v", got, olderAt)
	}
	// garmin has none waiting: the subquery is per provider.
	if rows, _ := st.SyncConnections(ctx, u); rows[1].PendingSince != nil {
		t.Fatalf("garmin: %+v", rows[1])
	}

	// A failed delivery waits for nothing; the next one does.
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET state = 'failed', error = 'test_failed' WHERE id = $1`, older); err != nil {
		t.Fatal(err)
	}
	if got := pendingSince(u); got == nil || !got.Equal(newerAt) {
		t.Fatalf("after the older one failed: %v, want %v", got, newerAt)
	}
	// A stored one neither.
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET state = 'processed', payload = NULL, processed_at = now() WHERE id = $1`, newer); err != nil {
		t.Fatal(err)
	}
	if got := pendingSince(u); got != nil {
		t.Fatalf("nothing waits: %v", got)
	}

	// needs_reauth is listed, revoked is not.
	if _, err := pool.Exec(ctx, `UPDATE connections SET status = 'needs_reauth', status_reason = 'refresh_failed' WHERE user_id = $1 AND provider = $2`, u, testProvider); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.SyncConnections(ctx, u); err != nil || len(rows) != 2 || rows[0].Status != model.ConnectionNeedsReauth {
		t.Fatalf("needs_reauth: %+v %v", rows, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connections SET status = 'revoked', status_reason = 'provider_revoked', provider_user_id = NULL, permissions = '{}'
		WHERE user_id = $1 AND provider = $2`, u, testProvider); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.SyncConnections(ctx, u); err != nil || len(rows) != 1 || rows[0].Provider != "garmin" {
		t.Fatalf("revoked: %+v %v", rows, err)
	}

	// last_data_at is what ApplyDirect set.
	if _, err := pool.Exec(ctx, `UPDATE connections SET last_data_at = '2026-11-24T05:02:11Z' WHERE user_id = $1 AND provider = 'garmin'`, u); err != nil {
		t.Fatal(err)
	}
	rows, err = st.SyncConnections(ctx, u)
	if err != nil || len(rows) != 1 || rows[0].LastDataAt == nil || !strings.HasPrefix(rows[0].LastDataAt.UTC().Format(time.RFC3339), "2026-11-24T05:02:11") {
		t.Fatalf("last data: %+v %v", rows, err)
	}
}
