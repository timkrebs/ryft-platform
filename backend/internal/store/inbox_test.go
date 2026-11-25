package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

const policy = "2026-10-26"

func item(kind, rec, puid string, version int64, body string) HookItem {
	return HookItem{Kind: kind, RecordID: rec, ProviderUserID: puid, Version: version, Payload: []byte(body)}
}

func store1(t *testing.T, st *Store, it HookItem) HookOutcome {
	t.Helper()
	out, err := st.StoreInbox(context.Background(), testProvider, policy, []HookItem{it}, nil)
	if err != nil || len(out) != 1 {
		t.Fatalf("store inbox: %v %v", out, err)
	}
	return out[0]
}

func TestStoreInboxOutcomes(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	var events []model.InboxReceivedEvent
	emit := func(tx pgx.Tx, ev model.InboxReceivedEvent) error { events = append(events, ev); return nil }
	run := func(it HookItem) HookOutcome {
		out, err := st.StoreInbox(ctx, testProvider, policy, []HookItem{it}, emit)
		if err != nil {
			t.Fatal(err)
		}
		return out[0]
	}
	steps := []struct {
		it   HookItem
		want HookOutcome
	}{
		{item("sleep", "n1", "p1", 2, `{"a":1}`), HookStored},
		{item("sleep", "n1", "p1", 2, `{"a":1}`), HookDuplicate},
		{item("sleep", "n1", "p1", 1, `{"a":2}`), HookStale},   // lower version, other payload
		{item("sleep", "n1", "p1", 3, `{"a":2}`), HookUpdated}, // changed, version >= stored
		{item("sleep", "n1", "p1", 3, `{"a":2}`), HookDuplicate},
		{item("hrv", "n1", "p1", 0, `{"h":1}`), HookStored}, // the key is (provider, kind, record id)
		{item("sleep", "n9", "nobody", 1, `{}`), HookUnknownUser},
	}
	for i, s := range steps {
		if got := run(s.it); got != s.want {
			t.Fatalf("step %d: %s, want %s", i, got, s.want)
		}
	}
	if len(events) != 3 { // stored, updated, stored
		t.Fatalf("%d events, want one per new or changed row: %+v", len(events), events)
	}
	if events[1].Revision != 2 || events[0].Revision != 1 || events[0].UserID != u || events[0].Provider != testProvider {
		t.Fatalf("events %+v", events)
	}
	var state string
	var rev int
	if err := pool.QueryRow(ctx, `SELECT state, revision FROM provider_inbox WHERE kind='sleep' AND record_id='n1'`).Scan(&state, &rev); err != nil || state != "pending" || rev != 2 {
		t.Fatalf("row: %s %d %v", state, rev, err)
	}
	// Nothing was stored for nobody.
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox`); n != 2 {
		t.Fatalf("%d rows", n)
	}
}

func TestStoreInboxConcurrentDuplicatesEmitOnce(t *testing.T) {
	st, pool := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	it := item("sleep", "n1", "p1", 1, `{"a":1}`)
	type result struct {
		out []HookOutcome
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	emitted := make(chan model.InboxReceivedEvent, 2)
	for range 2 {
		go func() {
			<-start
			out, err := st.StoreInbox(ctx, testProvider, policy, []HookItem{it}, func(_ pgx.Tx, ev model.InboxReceivedEvent) error {
				emitted <- ev
				return nil
			})
			results <- result{out, err}
		}()
	}
	close(start)
	outcomes := map[HookOutcome]int{}
	for range 2 {
		got := <-results
		if got.err != nil || len(got.out) != 1 {
			t.Fatalf("duplicate delivery: %+v", got)
		}
		outcomes[got.out[0]]++
	}
	if outcomes[HookStored] != 1 || outcomes[HookDuplicate] != 1 || len(emitted) != 1 {
		t.Fatalf("outcomes=%v emitted=%d", outcomes, len(emitted))
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE user_id=$1 AND state='pending'`, u); n != 1 {
		t.Fatalf("pending rows: %d", n)
	}
}

// A changed re-push of a processed row goes back to pending with a new revision (design 7.4).
func TestChangedRepushReopensAProcessedRow(t *testing.T) {
	st, pool := testStore(t)
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	pushNight(t, st, pool, u, "p1", "n", time.Now().Add(-10*time.Hour).UTC().Truncate(time.Second))
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE state='processed' AND payload IS NULL`); n != 2 {
		t.Fatalf("processed rows %d", n)
	}
	if got := store1(t, st, item("sleep", "p1nsleep", "p1", 5, `{"changed":true}`)); got != HookUpdated {
		t.Fatalf("re-push: %s", got)
	}
	var state string
	var attempts int
	if err := pool.QueryRow(context.Background(), `SELECT state, attempts FROM provider_inbox WHERE record_id='p1nsleep'`).Scan(&state, &attempts); err != nil || state != "pending" || attempts != 0 {
		t.Fatalf("%s %d %v", state, attempts, err)
	}
}

// S1: another account's record id never writes into this account's row.
func TestInboxIDCollisionTouchesNothing(t *testing.T) {
	st, pool := testStore(t)
	a, b := newAccount(t, st), newAccount(t, st)
	connectAs(t, st, a, "pa")
	connectAs(t, st, b, "pb")
	if got := store1(t, st, item("sleep", "same", "pa", 1, `{"owner":"a"}`)); got != HookStored {
		t.Fatal(got)
	}
	var before string
	if err := pool.QueryRow(context.Background(), `SELECT payload::text FROM provider_inbox WHERE record_id='same'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if got := store1(t, st, item("sleep", "same", "pb", 9, `{"owner":"b"}`)); got != HookCollision {
		t.Fatalf("collision: %s", got)
	}
	var owner uuid.UUID
	var after string
	var rev int
	if err := pool.QueryRow(context.Background(), `SELECT user_id, payload::text, revision FROM provider_inbox WHERE record_id='same'`).Scan(&owner, &after, &rev); err != nil {
		t.Fatal(err)
	}
	if owner != a || after != before || rev != 1 {
		t.Fatalf("the row changed: %s %s %d", owner, after, rev)
	}
}

// S5: the 501st pending row of an account is not stored.
func TestInboxPendingCapPerAccount(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	if _, err := pool.Exec(ctx, `INSERT INTO provider_inbox (provider, kind, record_id, user_id, payload, payload_sha256)
		SELECT 'fake', 'hrv', 'bulk-' || g, $1, '{}', sha256(('x' || g)::bytea) FROM generate_series(1, $2) g`, u, MaxPendingPerUser); err != nil {
		t.Fatal(err)
	}
	if got := store1(t, st, item("sleep", "one-too-many", "p1", 1, `{}`)); got != HookOverCap {
		t.Fatalf("over the cap: %s", got)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE record_id='one-too-many'`); n != 0 {
		t.Fatal("stored over the cap")
	}
	// Another account is unaffected.
	v := newAccount(t, st)
	connectAs(t, st, v, "p2")
	if got := store1(t, st, item("sleep", "other", "p2", 1, `{}`)); got != HookStored {
		t.Fatal(got)
	}
}

func TestInboxStoresNothingWithoutConsentOrForAPendingGrant(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	w := newAccount(t, st)
	callback(t, st, w, "pending-only", 5) // never confirmed
	if got := store1(t, st, item("sleep", "x", "pending-only", 1, `{}`)); got != HookUnknownUser {
		t.Fatalf("a pending grant: %s", got)
	}
	// An older policy version, then a withdrawn consent: dropped, not stored.
	if _, err := pool.Exec(ctx, `UPDATE consents SET policy_version='2025-01-01' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if got := store1(t, st, item("sleep", "y", "p1", 1, `{}`)); got != HookNoConsent {
		t.Fatalf("old policy: %s", got)
	}
	if err := st.GrantConsent(ctx, u, policy, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeConsent(ctx, u); err != nil {
		t.Fatal(err)
	}
	if got := store1(t, st, item("sleep", "z", "p1", 1, `{}`)); got != HookNoConsent {
		t.Fatalf("withdrawn: %s", got)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox`); n != 0 {
		t.Fatalf("%d rows stored", n)
	}
}

// The inbox row and its event commit together: a failing event rolls the row back.
func TestInboxRowAndEventShareATransaction(t *testing.T) {
	st, pool := testStore(t)
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	boom := errors.New("outbox down")
	_, err := st.StoreInbox(context.Background(), testProvider, policy,
		[]HookItem{item("sleep", "a", "p1", 1, `{}`), item("sleep", "b", "p1", 1, `{"b":1}`)},
		func(tx pgx.Tx, ev model.InboxReceivedEvent) error {
			if ev.Kind == "sleep" && ev.InboxID > 0 {
				return boom
			}
			return nil
		})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox`); n != 0 {
		t.Fatalf("%d rows survived a failed event", n)
	}
}

func TestInboxSweeps(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	for _, id := range []string{"fresh", "old", "ancient"} {
		store1(t, st, item("sleep", id, "p1", 1, `{"id":"`+id+`"}`))
	}
	age := func(rec, interval string) {
		if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET received_at = now() - $2::interval WHERE record_id=$1`, rec, interval); err != nil {
			t.Fatal(err)
		}
	}
	age("old", "2 hours")
	age("ancient", "8 days")
	var republished []int64
	n, err := st.RepublishInbox(ctx, 100, func(tx pgx.Tx, ev model.InboxReceivedEvent) error {
		republished = append(republished, ev.InboxID)
		return nil
	})
	if err != nil || n != 2 {
		t.Fatalf("republish %d %v", n, err)
	}
	if exp, err := st.ExpireInbox(ctx, 500); err != nil || exp != 1 {
		t.Fatalf("expire %d %v", exp, err)
	}
	var state, code string
	var payload *string
	if err := pool.QueryRow(ctx, `SELECT state, error, payload::text FROM provider_inbox WHERE record_id='ancient'`).Scan(&state, &code, &payload); err != nil || state != "failed" || code != "expired" || payload == nil {
		t.Fatalf("expired row: %s %s %v %v", state, code, payload, err)
	}
	ages, err := st.InboxAges(ctx)
	if err != nil || ages.Pending < 7000 || ages.FailedRows != 1 {
		t.Fatalf("ages %+v %v", ages, err)
	}
	// Retention: failed after 7 days, processed and dropped after 30.
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET processed_at = now() - interval '8 days' WHERE state='failed'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO provider_inbox (provider, kind, record_id, user_id, state, payload_sha256, processed_at)
		VALUES ('fake','hrv','d1',$1,'processed','\x0000000000000000000000000000000000000000000000000000000000000000', now() - interval '31 days'),
		       ('fake','hrv','d2',$1,'dropped','\x0000000000000000000000000000000000000000000000000000000000000000', now() - interval '29 days')`, u); err != nil {
		t.Fatal(err)
	}
	purged, err := st.PurgeInbox(ctx, 500)
	if err != nil || purged["failed"] != 1 || purged["processed"] != 1 || purged["dropped"] != 0 {
		t.Fatalf("purge %v %v", purged, err)
	}
}

func TestInboxConsumerHelpers(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	store1(t, st, item("hrv", "h1", "p1", 1, `{"x": 1}`))
	var id int64
	if err := st.pool.QueryRow(ctx, `SELECT id FROM provider_inbox`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	row, err := st.InboxRow(ctx, id)
	if err != nil || row.Provider != testProvider || row.Kind != "hrv" || row.State != "pending" || string(row.Payload) != `{"x": 1}` {
		t.Fatalf("row %+v %v", row, err)
	}
	for want := 1; want <= 2; want++ {
		if n, err := st.BumpInboxAttempt(ctx, id, row.Revision); err != nil || n != want {
			t.Fatalf("bump %d %v", n, err)
		}
	}
	if err := st.FinishInbox(ctx, id, row.Revision, "failed", "parse"); err != nil {
		t.Fatal(err)
	}
	if row, _ = st.InboxRow(ctx, id); row.State != "failed" || row.Payload == nil {
		t.Fatalf("a failed row keeps its payload: %+v", row)
	}
	if _, err := st.BumpInboxAttempt(ctx, id, row.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bump after failed: %v", err)
	}
	if _, err := st.InboxRow(ctx, id+1000); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_ = connect.KindHRV
}

func TestInboxAttemptsAndFinishApplyOnlyToCurrentRevision(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "p1")
	store1(t, st, item("hrv", "h1", "p1", 1, `{"x": 1}`))
	id := inboxID(t, pool, "hrv", "h1")
	previous, err := st.InboxRow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := store1(t, st, item("hrv", "h1", "p1", 2, `{"x": 2}`)); got != HookUpdated {
		t.Fatal(got)
	}
	if _, err := st.BumpInboxAttempt(ctx, id, previous.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old attempt reached the new revision: %v", err)
	}
	if err := st.FinishInbox(ctx, id, previous.Revision, "dropped", "no_consent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old verdict reached the new revision: %v", err)
	}
	current, err := st.InboxRow(ctx, id)
	if err != nil || current.Revision != previous.Revision+1 || current.State != "pending" || string(current.Payload) != `{"x": 2}` {
		t.Fatalf("current row: %+v %v", current, err)
	}
	if n, err := st.BumpInboxAttempt(ctx, id, current.Revision); err != nil || n != 1 {
		t.Fatalf("current first attempt: %d %v", n, err)
	}
}

// P2-3: PendingDirect lists the account's other pending sleep and hrv rows, oldest first, with payloads, and only those.
func TestPendingDirect(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "pd1")
	for _, it := range []HookItem{item("sleep", "s1", "pd1", 1, `{"a":1}`), item("hrv", "h1", "pd1", 1, `{"a":2}`),
		item("sleep", "s2", "pd1", 1, `{"a":3}`), item("permissions", "p1", "pd1", 1, `{"a":4}`)} {
		store1(t, st, it)
	}
	first := inboxID(t, pool, "sleep", "s1")
	got, err := st.PendingDirect(ctx, u, testProvider, first, 10)
	if err != nil || len(got) != 2 || got[0].RecordID != "h1" || got[1].RecordID != "s2" || !strings.Contains(string(got[0].Payload), "2") {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := st.PendingDirect(ctx, u, testProvider, first, 1); len(got) != 1 {
		t.Fatalf("limit: %d", len(got))
	}
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET attempts = $1 WHERE record_id='h1'`, MaxInboxAttempts-1); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.PendingDirect(ctx, u, testProvider, first, 10); len(got) != 1 || got[0].RecordID != "s2" {
		t.Fatalf("a row on its last delivery is batched: %+v", got)
	}
	if got, _ := st.PendingDirect(ctx, newAccount(t, st), testProvider, first, 10); len(got) != 0 {
		t.Fatal("another account's rows listed")
	}
}

// P2-12: a re-push while the consumer holds the older payload makes the row another revision; the old apply finds a
// revision it did not read, stores nothing and leaves the row pending for the new revision's event. The batched Also
// rows have the same check.
func TestApplyDirectRefusesAnOlderRevision(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "rv1")
	now := time.Now().UTC().Truncate(time.Second)
	store1(t, st, item("sleep", "a", "rv1", 1, `{"v":1}`))
	store1(t, st, item("sleep", "b", "rv1", 1, `{"v":1}`))
	aID, bID := inboxID(t, pool, "sleep", "a"), inboxID(t, pool, "sleep", "b")
	a := nightRecord(u, aID, now.Add(-12*time.Hour), model.RecordProvisional, "core")
	a.RecordID, a.Revision = "a", 1
	b := nightRecord(u, bID, now.Add(-36*time.Hour), model.RecordProvisional, "core")
	b.RecordID, b.Revision = "b", 1
	got, err := st.InboxRow(ctx, aID)
	if err != nil || got.Revision != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	// The re-push of both records lands after the consumer read revision 1.
	store1(t, st, item("sleep", "a", "rv1", 2, `{"v":2}`))
	store1(t, st, item("sleep", "b", "rv1", 2, `{"v":2}`))
	if got, _ = st.InboxRow(ctx, aID); got.Revision != 2 {
		t.Fatalf("revision %d", got.Revision)
	}
	a.Also = []DirectRecord{b}
	res, err := st.ApplyDirect(ctx, a)
	if err != nil || res.Outcome != DirectNotPending || len(res.Also) != 0 {
		t.Fatalf("old revision applied: %+v %v", res, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND origin='direct'`, u); n != 0 {
		t.Fatal("rows of the old revision were stored")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE state='pending'`); n != 2 {
		t.Fatalf("%d pending rows, want both left for the new revision", n)
	}
	// As the main record at the current revision, a batched row at an old one is skipped.
	a.Revision = 2
	res, err = st.ApplyDirect(ctx, a)
	if err != nil || res.Outcome != DirectApplied || len(res.Also) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE id=$1 AND state='pending'`, bID); n != 1 {
		t.Fatal("the batched row of an old revision was marked processed")
	}
	// The deregistration and permission applies check it too.
	store1(t, st, item("permissions", "p", "rv1", 1, `{"x":1}`))
	pid := inboxID(t, pool, "permissions", "p")
	store1(t, st, item("permissions", "p", "rv1", 2, `{"x":2}`))
	if o, err := st.ApplyPermissions(ctx, pid, 1, u, testProvider, policy, true); err != nil || o != DirectNotPending {
		t.Fatalf("permissions at an old revision: %v %v", o, err)
	}
	if o, err := st.ApplyPermissions(ctx, pid, 2, u, testProvider, policy, true); err != nil || o != DirectApplied {
		t.Fatalf("permissions at the current revision: %v %v", o, err)
	}
}

// P2-16: the hook's owner lookup holds the link's row (FOR SHARE), so a confirm that replaces the link cannot slip
// between the lookup and the insert: it then finds the push is for nobody.
func TestStoreInboxHoldsTheLinkRow(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "own-1")
	tx, err := pool.Begin(ctx) // the replacing confirm's swap: the row FOR UPDATE
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM connections WHERE user_id=$1 AND provider=$2 FOR UPDATE`, u, testProvider); err != nil {
		t.Fatal(err)
	}
	type res struct {
		out []HookOutcome
		err error
	}
	done := make(chan res, 1)
	go func() {
		o, err := st.StoreInbox(ctx, testProvider, policy, []HookItem{item("sleep", "own", "own-1", 1, `{"a":1}`)}, nil)
		done <- res{o, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the hook did not wait for the confirm holding the link: %+v", r)
	case <-time.After(400 * time.Millisecond):
	}
	if _, err := tx.Exec(ctx, `UPDATE connections SET provider_user_id='own-2' WHERE user_id=$1`, u); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || len(r.out) != 1 || r.out[0] != HookUnknownUser {
			t.Fatalf("after the swap: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook never finished")
	}
}

// P2-17: an item the database refuses as data (a lone surrogate jsonb cannot hold) is bad_item alone; the rest of the
// push is stored and no error is returned.
func TestStoreInboxBadItemDoesNotFailThePush(t *testing.T) {
	st, pool := testStore(t)
	u := newAccount(t, st)
	connectAs(t, st, u, "bad-1")
	items := []HookItem{item("sleep", "ok1", "bad-1", 1, `{"a":1}`), item("sleep", "bad", "bad-1", 1, `{"a":"\ud800"}`), item("hrv", "ok2", "bad-1", 1, `{"a":2}`)}
	emitted := 0
	outs, err := st.StoreInbox(context.Background(), testProvider, policy, items, func(pgx.Tx, model.InboxReceivedEvent) error { emitted++; return nil })
	if err != nil {
		t.Fatalf("one bad item failed the push: %v", err)
	}
	if outs[0] != HookStored || outs[1] != HookBadItem || outs[2] != HookStored || emitted != 2 {
		t.Fatalf("%v emitted %d", outs, emitted)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE user_id=$1`, u); n != 2 {
		t.Fatalf("%d rows stored", n)
	}
}

// D259: the hook keeps an account whose current consent is to the cutoff version or a later one, and drops one below
// it, one withdrawn, and one whose older row was withdrawn while a newer one below the cutoff stands.
func TestStoreInboxConsentCutoff(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	mk := func(puid, consentVersion string) uuid.UUID {
		u := newAccount(t, st) // consented to 2026-10-26
		connectAs(t, st, u, puid)
		if _, err := pool.Exec(ctx, `DELETE FROM consents WHERE user_id=$1`, u); err != nil {
			t.Fatal(err)
		}
		if consentVersion != "" {
			if err := st.GrantConsent(ctx, u, consentVersion, nil); err != nil {
				t.Fatal(err)
			}
		}
		return u
	}
	mk("c-new", "2026-12-01")
	mk("c-at", "2026-10-26")
	mk("c-old", "2026-09-01")
	mk("c-none", "")
	withdrawn := mk("c-withdrawn", "2026-12-01")
	if err := st.RevokeConsent(ctx, withdrawn); err != nil {
		t.Fatal(err)
	}
	// An older consent row that was never revoked next to a newer one: the current consent is the latest.
	both := mk("c-both", "2026-09-01")
	if err := st.GrantConsent(ctx, both, "2026-10-26", nil); err != nil {
		t.Fatal(err)
	}
	var items []HookItem
	puids := []string{"c-new", "c-at", "c-old", "c-none", "c-withdrawn", "c-both"}
	for _, p := range puids {
		items = append(items, item("sleep", "r-"+p, p, 1, `{"a":1}`))
	}
	outs, err := st.StoreInbox(ctx, testProvider, "2026-10-26", items, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []HookOutcome{HookStored, HookStored, HookNoConsent, HookNoConsent, HookNoConsent, HookStored}
	for i := range want {
		if outs[i] != want[i] {
			t.Errorf("%s: %s, want %s", puids[i], outs[i], want[i])
		}
	}
}

// P2-27: the sweeps re-check the row after a lock wait: a row that was processed (expire) or re-pushed (purge) while
// the statement waited is left alone.
func TestSweepsRecheckTheRowAfterALockWait(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := newAccount(t, st)
	connectAs(t, st, u, "sw-1")
	store1(t, st, item("sleep", "old", "sw-1", 1, `{"a":1}`))
	id := inboxID(t, pool, "sleep", "old")
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET received_at = now() - interval '8 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	hold := func(sql string, args ...any) (release func(), tx pgx.Tx) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
		return func() {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}, tx
	}
	// Expire waits for the row while another transaction processes it.
	release, _ := hold(`UPDATE provider_inbox SET state='processed', payload=NULL, processed_at=now() WHERE id=$1`, id)
	done := make(chan int64, 1)
	go func() { n, _ := st.ExpireInbox(ctx, 10); done <- n }()
	time.Sleep(400 * time.Millisecond)
	release()
	if n := <-done; n != 0 {
		t.Fatalf("a row processed during the wait was expired (%d)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE id=$1 AND state='processed'`, id); n != 1 {
		t.Fatal("the row is no longer processed")
	}
	// Purge waits for the row while another transaction re-pushes it (pending again).
	if _, err := pool.Exec(ctx, `UPDATE provider_inbox SET processed_at = now() - interval '31 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	release, _ = hold(`UPDATE provider_inbox SET state='pending', processed_at=NULL, received_at=now() WHERE id=$1`, id)
	pdone := make(chan map[string]int64, 1)
	go func() { m, _ := st.PurgeInbox(ctx, 10); pdone <- m }()
	time.Sleep(400 * time.Millisecond)
	release()
	if m := <-pdone; m["processed"] != 0 {
		t.Fatalf("a row re-pushed during the wait was purged: %v", m)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM provider_inbox WHERE id=$1`, id); n != 1 {
		t.Fatal("the row is gone")
	}
}
