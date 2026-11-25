package store

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

// Integration tests of the connections store (0042, D257), opt-in like the rest of the package.

const testProvider = "fake"

func newAccount(t *testing.T, st *Store) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := st.EnsureUser(context.Background(), Identity{ID: id, Email: id.String()[:8] + "@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(context.Background(), id, "2026-10-26", nil); err != nil {
		t.Fatal(err)
	}
	return id
}

func identityReseal(a, r []byte) ([]byte, []byte, error) { return a, r, nil }

// callback stores a pending grant for the account as the callback does, and returns the ticket.
func callback(t *testing.T, st *Store, user uuid.UUID, puid string, cap int) string {
	t.Helper()
	ticket := connect.NewTicket()
	err := st.PutPendingCapped(context.Background(), Pending{UserID: user, Provider: testProvider, ProviderUserID: puid,
		Permissions: []string{"read_sleep"}, AccessEnc: []byte("a:" + puid), RefreshEnc: []byte("r:" + puid),
		RefreshDue: time.Now().Add(time.Hour), ConfirmHash: connect.Hash(ticket), Expires: time.Now().Add(StateTTL)}, cap)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	return ticket
}

func confirm(st *Store, user uuid.UUID, ticket string, cap int) (ConfirmOut, error) {
	return st.ConfirmConnection(context.Background(), ConfirmIn{UserID: user, Provider: testProvider, TicketHash: connect.Hash(ticket),
		Cap: cap, PolicyVersion: "2026-10-26", Reseal: identityReseal})
}

func connectAs(t *testing.T, st *Store, user uuid.UUID, puid string) ConfirmOut {
	t.Helper()
	out, err := confirm(st, user, callback(t, st, user, puid, 100), 100)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	return out
}

func directRows(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) (samples, sleep, sources int) {
	t.Helper()
	err := pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM samples WHERE user_id=$1 AND origin='direct'),
		(SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND origin='direct'),
		(SELECT count(*) FROM user_sources WHERE user_id=$1 AND origin='direct')`, user).Scan(&samples, &sleep, &sources)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// pushNight stores a night and an HRV for the account through the inbox and the consumer's apply, as ingest does.
func pushNight(t *testing.T, st *Store, pool *pgxpool.Pool, user uuid.UUID, puid, recordID string, night time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, kind := range []string{connect.KindSleep, connect.KindHRV} {
		outs, err := st.StoreInbox(ctx, testProvider, "2026-10-26", []HookItem{{Kind: kind, RecordID: puid + recordID + kind, ProviderUserID: puid, Version: 1,
			Payload: []byte(`{"n":"` + night.String() + kind + `"}`)}}, nil)
		if err != nil || (outs[0] != HookStored && outs[0] != HookUpdated) {
			t.Fatalf("store inbox: %v %v", outs, err)
		}
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM provider_inbox WHERE provider=$1 AND kind=$2 AND record_id=$3`, testProvider, kind, puid+recordID+kind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		r := DirectRecord{InboxID: id, Revision: 1, UserID: user, Provider: testProvider, Kind: kind, RecordID: puid + recordID + kind, Loc: time.UTC}
		if kind == connect.KindSleep {
			r.NightState = model.RecordFinal
			r.Sleep = []model.SleepSegment{{Start: night, End: night.Add(4 * time.Hour), Stage: "core"}, {Start: night.Add(4 * time.Hour), End: night.Add(7 * time.Hour), Stage: "deep"}}
		} else {
			r.Samples = []model.Sample{{Type: model.SampleHRVRMSSD, Start: night.Add(time.Hour), End: night.Add(time.Hour + 5*time.Minute), Value: 48, Unit: "ms"}}
		}
		if res, err := st.ApplyDirect(ctx, r); err != nil || res.Outcome != DirectApplied {
			t.Fatalf("apply: %+v %v", res, err)
		}
	}
}

func TestConnectFlowAndRepeat(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	ticket := callback(t, st, u, "puid-1", 5)
	// Until the confirm the account has no connection (B1) and the grant is pending with its tokens.
	if rows, _ := st.Connections(ctx, u); len(rows) != 0 {
		t.Fatalf("a pending grant is listed: %+v", rows)
	}
	out, err := confirm(st, u, ticket, 5)
	if err != nil || out.Outcome != ConfirmOK || out.Connection.Status != model.ConnectionConnected || *out.Connection.ProviderUserID != "puid-1" {
		t.Fatalf("confirm: %+v %v", out, err)
	}
	// The tokens moved, the pending row holds none and keeps the ticket's hash.
	if n := countRows(t, pool, `SELECT count(*) FROM connection_tokens WHERE user_id=$1`, u); n != 1 {
		t.Fatalf("tokens: %d", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_pending WHERE user_id=$1 AND access_enc IS NULL AND confirmed_at IS NOT NULL`, u); n != 1 {
		t.Fatalf("pending after confirm: %d", n)
	}
	// A repeat within the window answers the connection again (S3).
	rep, err := confirm(st, u, ticket, 5)
	if err != nil || rep.Outcome != ConfirmRepeat || rep.Connection.Provider != testProvider {
		t.Fatalf("repeat: %+v %v", rep, err)
	}
	// Another ticket, or none, is invalid.
	if _, err := confirm(st, u, connect.NewTicket(), 5); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("wrong ticket: %v", err)
	}
	if _, err := confirm(st, newAccount(t, st), ticket, 5); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("another account's ticket: %v", err)
	}
}

func TestConfirmExpiredDropsTheGrant(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	ticket := callback(t, st, u, "puid-x", 5)
	if _, err := pool.Exec(ctx, `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if _, err := confirm(st, u, ticket, 5); !errors.Is(err, ErrConfirmExpired) {
		t.Fatalf("late confirm: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_pending WHERE user_id=$1`, u); n != 0 {
		t.Fatal("the expired grant stays")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_deregistrations WHERE user_id=$1 AND reason='confirm_expired' AND access_enc IS NOT NULL`, u); n != 1 {
		t.Fatal("the expired grant is not queued")
	}
	// The ticket is gone now: unknown.
	if _, err := confirm(st, u, ticket, 5); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("confirm after the drop: %v", err)
	}
	// A repeat after the window of a confirmed grant is expired too, though connected (API.md).
	t2 := callback(t, st, u, "puid-x", 5)
	if _, err := confirm(st, u, t2, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if _, err := confirm(st, u, t2, 5); !errors.Is(err, ErrConfirmExpired) {
		t.Fatalf("repeat after the window: %v", err)
	}
	if rows, _ := st.Connections(ctx, u); len(rows) != 1 || rows[0].Status != model.ConnectionConnected {
		t.Fatalf("the connection must survive: %+v", rows)
	}
	// ...and a confirmed grant that expired queues nothing (no tokens).
	if n := countRows(t, pool, `SELECT count(*) FROM connection_deregistrations WHERE user_id=$1`, u); n != 1 {
		t.Fatalf("queue rows: %d, want only the first drop", n)
	}
}

// An unconfirmed reconnect leaves the live row and its tokens byte for byte (review B1).
func TestUnconfirmedReconnectLeavesTheLiveConnection(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "puid-live")
	tok := func() (a, r []byte, rows []ConnectionRow) {
		if err := pool.QueryRow(ctx, `SELECT access_enc, refresh_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&a, &r); err != nil {
			t.Fatal(err)
		}
		rows, _ = st.Connections(ctx, u)
		return
	}
	a0, r0, c0 := tok()
	callback(t, st, u, "puid-live", 5) // a reconnect's callback, never confirmed
	a1, r1, c1 := tok()
	if !bytes.Equal(a0, a1) || !bytes.Equal(r0, r1) || !c0[0].ConnectedAt.Equal(c1[0].ConnectedAt) {
		t.Fatalf("the live connection changed: %+v -> %+v", c0, c1)
	}
	// Expiring the pending grant queues its tokens, not the live ones; the live link stays.
	if _, err := pool.Exec(ctx, `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if p, err := st.PurgeConnect(ctx); err != nil || p.Pending != 1 {
		t.Fatalf("purge: %+v %v", p, err)
	}
	a2, _, c2 := tok()
	if !bytes.Equal(a0, a2) || c2[0].Status != model.ConnectionConnected {
		t.Fatal("the purge touched the live connection")
	}
	var queued []byte
	if err := pool.QueryRow(ctx, `SELECT access_enc FROM connection_deregistrations WHERE user_id=$1 AND reason='confirm_expired'`, u).Scan(&queued); err != nil || string(queued) != "a:puid-live" {
		t.Fatalf("queued grant: %q %v", queued, err)
	}
}

func TestConfirmSwapSameProviderAccountKeepsData(t *testing.T) {
	st, pool := testStore(t)
	u := newAccount(t, st)
	connectAs(t, st, u, "puid-1")
	pushNight(t, st, pool, u, "puid-1", "n1", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	out, err := confirm(st, u, callback(t, st, u, "puid-1", 5), 5)
	if err != nil || out.Outcome != ConfirmOK || len(out.Queued) != 0 {
		t.Fatalf("reconnect to the same provider account: %+v %v", out, err)
	}
	if s, sl, _ := directRows(t, pool, u); s != 1 || sl != 2 {
		t.Fatalf("data after a same-account reconnect: %d samples, %d sleep", s, sl)
	}
	if out.Connection.LastDataAt == nil {
		t.Fatal("last_data_at lost on a reconnect")
	}
}

func TestConfirmSwapDifferentProviderAccountDisconnectsTheOld(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "puid-old")
	pushNight(t, st, pool, u, "puid-old", "n1", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	var emitted [][]string
	in := ConfirmIn{UserID: u, Provider: testProvider, Cap: 5, PolicyVersion: "2026-10-26", Reseal: identityReseal,
		Emit: func(tx pgx.Tx, user uuid.UUID, days []string) error { emitted = append(emitted, days); return nil }}
	ticket := callback(t, st, u, "puid-new", 5)
	in.TicketHash = connect.Hash(ticket)
	out, err := st.ConfirmConnection(ctx, in)
	if err != nil || out.Outcome != ConfirmReplaced || len(out.Queued) != 1 {
		t.Fatalf("replace: %+v %v", out, err)
	}
	if s, sl, src := directRows(t, pool, u); s+sl+src != 0 {
		t.Fatalf("the old connection's data is left: %d %d %d", s, sl, src)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE user_id=$1`, u); n != 0 {
		t.Fatalf("inbox rows left: %d", n)
	}
	if len(emitted) != 1 || len(emitted[0]) == 0 {
		t.Fatalf("no event with days for the deleted data: %v", emitted)
	}
	var reason, puid string
	if err := pool.QueryRow(ctx, `SELECT reason, provider_user_id FROM connection_deregistrations WHERE id=$1`, out.Queued[0]).Scan(&reason, &puid); err != nil || reason != "replaced" || puid != "puid-old" {
		t.Fatalf("queue row: %s %s %v", reason, puid, err)
	}
	if *out.Connection.ProviderUserID != "puid-new" || out.Connection.LastDataAt != nil {
		t.Fatalf("new connection %+v", out.Connection)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM score_queue WHERE user_id=$1 AND scored_at IS NULL`, u); n == 0 {
		t.Fatal("the days of the deleted data are not queued")
	}
}

func TestConfirmRelinkMovesTheProviderAccount(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b := newAccount(t, st), newAccount(t, st)
	connectAs(t, st, a, "shared")
	pushNight(t, st, pool, a, "shared", "n1", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	out, err := confirm(st, b, callback(t, st, b, "shared", 5), 5)
	if err != nil || out.Outcome != ConfirmRelinked || !out.Relinked || len(out.Queued) != 0 {
		t.Fatalf("relink: %+v %v", out, err)
	}
	rows, _ := st.Connections(ctx, a)
	if len(rows) != 1 || rows[0].Status != model.ConnectionRevoked || rows[0].ProviderUserID != nil || len(rows[0].Permissions) != 0 {
		t.Fatalf("the first account's entry: %+v", rows)
	}
	if s, sl, src := directRows(t, pool, a); s+sl+src != 0 {
		t.Fatal("the first account keeps data")
	}
	// Its tokens are deleted and nothing is queued: a deregistration would end the new grant too.
	if n := countRows(t, pool, `SELECT count(*) FROM connection_tokens WHERE user_id=$1`, a); n != 0 {
		t.Fatal("tokens of the relinked connection stay")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_deregistrations`); n != 0 {
		t.Fatalf("%d queue rows after a relink", n)
	}
	if rows, _ := st.Connections(ctx, b); len(rows) != 1 || rows[0].Status != model.ConnectionConnected {
		t.Fatalf("the second account: %+v", rows)
	}
}

func TestReconnectAfterRevokedStartsOver(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	if _, err := pool.Exec(ctx, `UPDATE connections SET status='revoked', status_reason='provider_revoked', provider_user_id=NULL, permissions='{}', last_data_at=now() WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	out, err := confirm(st, u, callback(t, st, u, "p1", 5), 5)
	if err != nil || out.Connection.Status != model.ConnectionConnected || out.Connection.LastDataAt != nil {
		t.Fatalf("reconnect after revoked: %+v %v", out, err)
	}
}

func TestDisconnectDeletesExactlyTheProvidersDirectRows(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u, other := newAccount(t, st), newAccount(t, st)
	connectAs(t, st, u, "p1")
	connectAs(t, st, other, "p2")
	now := time.Now().UTC().Truncate(time.Second)
	pushNight(t, st, pool, u, "p1", "n1", now.Add(-10*time.Hour))
	pushNight(t, st, pool, other, "p2", "n1", now.Add(-10*time.Hour))
	// HealthKit rows of the same vendor and another provider's rows must stay.
	if _, err := st.UpsertSleep(ctx, u, []model.SleepSegment{{Start: now.Add(-9 * time.Hour), End: now.Add(-3 * time.Hour), Stage: "core", Source: "Garmin Connect"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source, origin, vendor) VALUES ($1,'hrv_rmssd',$2,$2,50,'ms','ryft-direct-other','direct','other')`, u, now); err != nil {
		t.Fatal(err)
	}
	var emitted []string
	out, err := st.Disconnect(ctx, u, testProvider, func(tx pgx.Tx, user uuid.UUID, days []string) error { emitted = days; return nil })
	if err != nil || !out.Existed || len(out.Queued) != 1 {
		t.Fatalf("disconnect: %+v %v", out, err)
	}
	if s, sl, src := directRows(t, pool, u); s != 1 || sl != 0 || src != 0 { // only the other vendor's sample
		t.Fatalf("direct rows left: %d %d %d", s, sl, src)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND source='Garmin Connect'`, u); n != 1 {
		t.Fatal("the HealthKit copy was deleted")
	}
	if s, sl, src := directRows(t, pool, other); s != 1 || sl != 2 || src != 2 {
		t.Fatalf("another account's data changed: %d %d %d", s, sl, src)
	}
	if len(emitted) == 0 {
		t.Fatal("no days queued for the deleted data")
	}
	if rows, _ := st.Connections(ctx, u); len(rows) != 0 {
		t.Fatalf("connection left: %+v", rows)
	}
	// Idempotent: the second call ends nothing and queues nothing.
	again, err := st.Disconnect(ctx, u, testProvider, nil)
	if err != nil || again.Existed || len(again.Queued) != 0 {
		t.Fatalf("second disconnect: %+v %v", again, err)
	}
}

func TestDisconnectCancelsAFlowWaitingForItsConfirm(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	ticket := callback(t, st, u, "p9", 5)
	if err := st.StartConnect(ctx, u, testProvider, 5, NewState{Hash: connect.Hash("s"), VerifierEnc: []byte("v"), PolicyVersion: "x", Expires: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	out, err := st.Disconnect(ctx, u, testProvider, nil)
	if err != nil || !out.Existed || len(out.Queued) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := confirm(st, u, ticket, 5); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("confirm after disconnect: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_states WHERE user_id=$1`, u); n != 0 {
		t.Fatal("an open state survives the disconnect")
	}
}

func TestStatesAreSingleUseAndCapped(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	var hashes [][32]byte
	for i := 0; i < 4; i++ {
		h := connect.Hash(connect.NewState())
		hashes = append(hashes, h)
		if err := st.StartConnect(ctx, u, testProvider, 5, NewState{Hash: h, VerifierEnc: []byte("v"), PolicyVersion: "p", Expires: time.Now().Add(StateTTL)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_states WHERE user_id=$1`, u); n != MaxOpenStates {
		t.Fatalf("open states: %d, want %d", n, MaxOpenStates)
	}
	if _, err := st.ConsumeState(ctx, hashes[0], testProvider); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the oldest state survived: %v", err)
	}
	f, err := st.ConsumeState(ctx, hashes[3], testProvider)
	if err != nil || f.UserID != u || string(f.VerifierEnc) != "v" {
		t.Fatalf("consume: %+v %v", f, err)
	}
	if _, err := st.ConsumeState(ctx, hashes[3], testProvider); !errors.Is(err, ErrNotFound) {
		t.Fatal("a state worked twice")
	}
	if _, err := st.ConsumeState(ctx, hashes[2], "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("another provider's state worked")
	}
	if _, err := st.ConsumeState(ctx, hashes[2], testProvider); err != nil {
		t.Fatalf("a state of the right provider: %v", err)
	}
}

// Two accounts start at once with one place left: exactly one gets it.
func TestCapHoldsUnderConcurrentStarts(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	users := []uuid.UUID{newAccount(t, st), newAccount(t, st), newAccount(t, st)}
	var wg sync.WaitGroup
	errs := make([]error, len(users))
	for i, u := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = st.StartConnect(ctx, u, testProvider, 1, NewState{Hash: connect.Hash(connect.NewState()), VerifierEnc: []byte("v"), PolicyVersion: "p", Expires: time.Now().Add(StateTTL)})
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrCapFull):
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d starts got the one place", ok)
	}
}

// Cancel the sheet, tap Connect again at the last free place: the caller's own open flow is not counted against
// them, at start, callback, confirm and accepting; another account's is.
func TestTheCallersOwnFlowNeverCountsAgainstTheCap(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	u, v := newAccount(t, st), newAccount(t, st)
	start := func(who uuid.UUID) error {
		return st.StartConnect(ctx, who, testProvider, 1, NewState{Hash: connect.Hash(connect.NewState()), VerifierEnc: []byte("v"), PolicyVersion: "p", Expires: time.Now().Add(StateTTL)})
	}
	if err := start(u); err != nil {
		t.Fatal(err)
	}
	if err := start(u); err != nil { // the sheet was cancelled; the same account starts again
		t.Fatalf("start again at the last place: %v", err)
	}
	if err := start(v); !errors.Is(err, ErrCapFull) {
		t.Fatalf("another account took the held place: %v", err)
	}
	if ok, err := st.Accepting(ctx, u, testProvider, 1); err != nil || !ok {
		t.Fatalf("accepting for the holder of the place: %v %v", ok, err)
	}
	if ok, _ := st.Accepting(ctx, v, testProvider, 1); ok {
		t.Fatal("accepting for another account while the place is held")
	}
	// Callback and confirm count the caller out too.
	ticket := callback(t, st, u, "p-u", 1)
	if out, err := confirm(st, u, ticket, 1); err != nil || out.Connection.Status != model.ConnectionConnected {
		t.Fatalf("confirm at the last place: %+v %v", out, err)
	}
	// Now u holds the place for good; v cannot, but u's reconnect is always accepted.
	if err := start(v); !errors.Is(err, ErrCapFull) {
		t.Fatal(err)
	}
	if err := start(u); err != nil {
		t.Fatalf("a reconnect meets the cap: %v", err)
	}
	if err := st.PutPendingCapped(ctx, Pending{UserID: v, Provider: testProvider, ProviderUserID: "p-v", AccessEnc: []byte("a"), RefreshEnc: []byte("r"),
		ConfirmHash: connect.Hash("t"), Expires: time.Now().Add(time.Minute)}, 1); !errors.Is(err, ErrCapFull) {
		t.Fatalf("callback over the cap: %v", err)
	}
}

func TestDeleteUserQueuesTheTokensAndCoversEveryConnectionTable(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "gone-1")
	pushNight(t, st, pool, u, "gone-1", "n1", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	callback(t, st, u, "gone-2", 5) // a grant waiting for its confirm
	if err := st.StartConnect(ctx, u, testProvider, 5, NewState{Hash: connect.Hash("s1"), VerifierEnc: []byte("v"), PolicyVersion: "p", Expires: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"connections", "connection_tokens", "connection_pending", "connection_states", "provider_inbox"} {
		if n := countRows(t, pool, `SELECT count(*) FROM `+tbl+` WHERE user_id=$1`, u); n != 0 {
			t.Errorf("%s: %d rows of the deleted user", tbl, n)
		}
	}
	rows, err := st.UserDeregistrations(ctx, u)
	if err != nil || len(rows) != 2 {
		t.Fatalf("queue: %+v %v", rows, err)
	}
	for _, r := range rows {
		if r.Reason != "account_deleted" || len(r.AccessEnc) == 0 {
			t.Errorf("queue row %+v", r)
		}
	}
	if s, sl, src := directRows(t, pool, u); s+sl+src != 0 {
		t.Fatal("direct rows left")
	}
}

func TestDeregistrationQueue(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "q1")
	if held, err := st.HoldsLiveGrant(ctx, testProvider, "q1"); err != nil || !held {
		t.Fatalf("live grant: %v %v", held, err)
	}
	if held, _ := st.HoldsLiveGrant(ctx, testProvider, "nobody"); held {
		t.Fatal("a grant nobody holds is live")
	}
	id, err := st.QueueDeregistration(ctx, u, testProvider, "q1", "cap", []byte("a"), []byte("r"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimDeregistrations(ctx, 10)
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("claim: %+v %v", got, err)
	}
	// Claimed: not due again until its claim lapses.
	if again, _ := st.ClaimDeregistrations(ctx, 10); len(again) != 0 {
		t.Fatal("a claimed row is claimed twice")
	}
	if err := st.RetryDeregistration(ctx, id, "unreachable", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.ClaimDeregistrations(ctx, 10); len(again) != 1 || again[0].Attempts != 1 {
		t.Fatalf("after a retry: %+v", again)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_deregistrations SET created_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	gone, err := st.GiveUpDeregistrations(ctx)
	if err != nil || len(gone) != 1 || gone[0].Reason != "cap" {
		t.Fatalf("give up: %+v %v", gone, err)
	}
	if _, err := st.Deregistration(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// P2-6: the live-grant rule counts a grant another account has waiting for its confirm (unconfirmed, inside its
// window), and the check takes the cap lock a confirm takes, so it cannot interleave with a confirm's swap.
func TestHoldsLiveGrantCountsPendingAndTakesTheCapLock(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	callback(t, st, u, "pend-1", 100)
	if held, err := st.HoldsLiveGrant(ctx, testProvider, "pend-1"); err != nil || !held {
		t.Fatalf("an unconfirmed pending grant is live: %v %v", held, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if held, _ := st.HoldsLiveGrant(ctx, testProvider, "pend-1"); held {
		t.Fatal("a pending grant past its window is live")
	}
	ticket := callback(t, st, u, "pend-2", 100)
	if _, err := confirm(st, u, ticket, 100); err != nil {
		t.Fatal(err)
	}
	if held, _ := st.HoldsLiveGrant(ctx, testProvider, "pend-2"); !held {
		t.Fatal("a confirmed connection is live")
	}

	// A confirm in flight holds the lock: the check waits for it, then sees what it committed.
	other := newAccount(t, st)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, lockConnectCap, testProvider); err != nil {
		t.Fatal(err)
	}
	type res struct {
		held bool
		err  error
	}
	done := make(chan res, 1)
	go func() {
		h, err := st.HoldsLiveGrant(ctx, testProvider, "late-1")
		done <- res{h, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the check ran without the cap lock: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := tx.Exec(ctx, `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1,$2,'late-1','connected','{}','p')`, other, testProvider); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || !r.held {
			t.Fatalf("after the confirm committed: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the check never got the lock")
	}
}

func TestRefreshClaimsAndFailures(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "r1")
	if got, _ := st.ClaimRefreshes(ctx, 10); len(got) != 0 {
		t.Fatalf("refresh not due yet: %+v", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	got, err := st.ClaimRefreshes(ctx, 10)
	if err != nil || len(got) != 1 || got[0].UserID != u {
		t.Fatalf("claim: %+v %v", got, err)
	}
	if again, _ := st.ClaimRefreshes(ctx, 10); len(again) != 0 {
		t.Fatal("claimed twice")
	}
	if err := st.RefreshFailed(ctx, u, testProvider, "unreachable", 1, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.ClaimRefreshes(ctx, 10); len(got) != 1 || got[0].Attempts != 1 {
		t.Fatalf("after a failure: %+v", got)
	}
	if err := st.NeedsReauth(ctx, u, testProvider, "invalid_grant"); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.Connections(ctx, u)
	if rows[0].Status != model.ConnectionNeedsReauth {
		t.Fatalf("status %s", rows[0].Status)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM connection_tokens WHERE user_id=$1 AND access_enc IS NOT NULL`, u); n != 1 {
		t.Fatal("the tokens of a needs_reauth link are kept")
	}
	// A needs_reauth link is no longer refreshed, but is still live (the live-grant rule).
	if held, _ := st.HoldsLiveGrant(ctx, testProvider, "r1"); !held {
		t.Fatal("needs_reauth is live")
	}
	var claimed []byte
	if err := pool.QueryRow(ctx, `SELECT refresh_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	// P2-25: the save is a compare-and-swap on the refresh token the refresh started from.
	if saved, err := st.SaveRefresh(ctx, u, testProvider, []byte("not the stored token"), []byte("a2"), nil, time.Now(), time.Time{}); err != nil || saved {
		t.Fatalf("saved over other tokens: %v %v", saved, err)
	}
	if saved, err := st.SaveRefresh(ctx, u, testProvider, claimed, []byte("a2"), nil, time.Now(), time.Time{}); err != nil || !saved {
		t.Fatalf("%v %v", saved, err)
	}
	// P2-19: after a disconnect there is no token row: nothing is saved, and the caller is told.
	if _, err := st.Disconnect(ctx, u, testProvider, nil); err != nil {
		t.Fatal(err)
	}
	if saved, err := st.SaveRefresh(ctx, u, testProvider, claimed, []byte("a3"), nil, time.Now(), time.Time{}); err != nil || saved {
		t.Fatalf("saved after a disconnect: %v %v", saved, err)
	}
}

func TestPurgeConnect(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "pp")
	if err := st.StartConnect(ctx, u, testProvider, 5, NewState{Hash: connect.Hash("old"), VerifierEnc: []byte("v"), PolicyVersion: "p", Expires: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	// A revoked connection's tokens go; the row stays 90 days.
	if _, err := pool.Exec(ctx, `UPDATE connections SET status='revoked', status_reason='provider_revoked', provider_user_id=NULL, permissions='{}' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	p, err := st.PurgeConnect(ctx)
	if err != nil || p.Tokens != 1 || p.Revoked != 0 {
		t.Fatalf("purge: %+v %v", p, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE connections SET status_at = now() - interval '91 days'`); err != nil {
		t.Fatal(err)
	}
	if p, err = st.PurgeConnect(ctx); err != nil || p.Revoked != 1 {
		t.Fatalf("purge revoked: %+v %v", p, err)
	}
}

func TestGauges(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b := newAccount(t, st), newAccount(t, st)
	connectAs(t, st, a, "g1")
	connectAs(t, st, b, "g2")
	counts, err := st.ConnectionCounts(ctx)
	if err != nil || counts[testProvider][model.ConnectionConnected] != 2 {
		t.Fatalf("counts %v %v", counts, err)
	}
	if n, _ := st.StaleConnections(ctx, testProvider); n != 0 {
		t.Fatal("fresh links are stale")
	}
	if _, err := pool.Exec(ctx, `UPDATE connections SET connected_at = now() - interval '2 days' WHERE user_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.StaleConnections(ctx, testProvider); n != 1 {
		t.Fatalf("stale %d, want 1", n)
	}
	if n, err := st.ProviderLoad(ctx, testProvider, uuid.Nil); err != nil || n != 2 {
		t.Fatalf("load %d %v", n, err)
	}
	if n, _ := st.LiveConnectionCount(ctx, a); n != 1 {
		t.Fatal("live count")
	}
}

// ---- review round 2 (P2-12 .. P2-21) ------------------------------------------------------------------------------

// P2-14: the relinking confirm takes the other account's apply lock before its connection row, as ApplyDirect and the
// rest do. An apply holding the other account's lock and then touching its row must not meet the confirm holding the
// row (a deadlock, 40P01).
func TestRelinkTakesTheOtherAccountsApplyLockFirst(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	holder, newcomer := newAccount(t, st), newAccount(t, st)
	connectAs(t, st, holder, "shared-1")
	ticket := callback(t, st, newcomer, "shared-1", 100)

	tx, err := pool.Begin(ctx) // what ApplyDirect does: the account's apply lock, then its connection row
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, lockApply, holder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := confirm(st, newcomer, ticket, 100); done <- err }()
	time.Sleep(500 * time.Millisecond) // the confirm has found the holder and waits for its apply lock
	if _, err := tx.Exec(ctx, `UPDATE connections SET last_data_at = now() WHERE user_id=$1`, holder); err != nil {
		t.Fatalf("the apply could not touch its row while the confirm waited (it holds the row first): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("confirm: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the confirm never finished")
	}
	rows, _ := st.Connections(ctx, holder)
	if len(rows) != 1 || rows[0].Status != model.ConnectionRevoked {
		t.Fatalf("the holder was not relinked: %+v", rows)
	}
}

// P2-20: the repeat of a confirm whose connection was relinked away answers the ticket as spent.
func TestRepeatConfirmAfterRelinkAwayIsInvalid(t *testing.T) {
	st, _ := testStore(t)
	first, second := newAccount(t, st), newAccount(t, st)
	ticket := callback(t, st, first, "rel-1", 100)
	if _, err := confirm(st, first, ticket, 100); err != nil {
		t.Fatal(err)
	}
	connectAs(t, st, second, "rel-1") // relinks the provider user to the second account: the first is revoked
	if _, err := confirm(st, first, ticket, 100); !errors.Is(err, ErrTicketInvalid) {
		t.Fatalf("repeat after a relink away: %v, want ErrTicketInvalid", err)
	}
}

// P2-21: a confirm after the link was revoked clears the account's inbox, so a later identical deregistration notice
// is not taken for a repeat of the processed one.
func TestConfirmAfterRevokedClearsTheInbox(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "dr-1")
	notice := item("deregistration", "dereg-1", "dr-1", 1, `{"kind":"deregistration"}`)
	if got := store1(t, st, notice); got != HookStored {
		t.Fatal(got)
	}
	if res, err := st.ApplyDeregistration(ctx, inboxID(t, pool, "deregistration", "dereg-1"), 1, u, testProvider, policy, nil); err != nil || res.Outcome != DirectApplied {
		t.Fatalf("%+v %v", res, err)
	}
	connectAs(t, st, u, "dr-1") // reconnects the same provider account
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE user_id=$1`, u); n != 0 {
		t.Fatalf("%d inbox rows survive the reconnect", n)
	}
	if got := store1(t, st, notice); got != HookStored {
		t.Fatalf("a later identical notice is %s, want stored", got)
	}
}

// P2-18: a disconnect with nothing to end does not scan the account's direct data.
func TestDisconnectWithNothingToEndDeletesNothing(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	if _, err := pool.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source, origin, vendor, record_id)
		VALUES ($1,'hrv_rmssd',now() - interval '2 hours',now() - interval '1 hour',40,'ms','ryft-direct-fake','direct','fake','hrv:x')`, u); err != nil {
		t.Fatal(err)
	}
	emitted := 0
	out, err := st.Disconnect(ctx, u, testProvider, func(pgx.Tx, uuid.UUID, []string) error { emitted++; return nil })
	if err != nil || out.Existed || len(out.Queued) != 0 || emitted != 0 {
		t.Fatalf("%+v %v emitted %d", out, err, emitted)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM samples WHERE user_id=$1 AND origin='direct'`, u); n != 1 {
		t.Fatal("a disconnect with no connection scanned and deleted direct data")
	}
}

// P2-26: the confirm takes both connection rows in user id order, as StoreInbox's FOR SHARE does: a hook holding the
// lower id's row and then asking for the higher one meets no confirm that holds the higher one first.
func TestConfirmLocksRowsInUserIDOrder(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	var holder, newcomer uuid.UUID
	for { // the holder has the lower id, the confirming account the higher: the old order locked the higher first
		holder, newcomer = newAccount(t, st), newAccount(t, st)
		if bytes.Compare(holder[:], newcomer[:]) < 0 {
			break
		}
	}
	connectAs(t, st, holder, "ord-shared")
	connectAs(t, st, newcomer, "ord-old") // the confirming account has a link row of its own (a replace)
	ticket := callback(t, st, newcomer, "ord-shared", 100)

	tx, err := pool.Begin(ctx) // the hook: FOR SHARE on the lower id's row, then the higher one's
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM connections WHERE user_id=$1 FOR SHARE`, holder); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := confirm(st, newcomer, ticket, 100); done <- err }()
	time.Sleep(500 * time.Millisecond) // the confirm waits for the lower row
	if _, err := tx.Exec(ctx, `SELECT 1 FROM connections WHERE user_id=$1 FOR SHARE`, newcomer); err != nil {
		t.Fatalf("the confirm held the higher id's row while waiting for the lower one: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("confirm: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the confirm never finished")
	}
	if rows, _ := st.Connections(ctx, holder); len(rows) != 1 || rows[0].Status != model.ConnectionRevoked {
		t.Fatalf("the holder was not relinked: %+v", rows)
	}
}
