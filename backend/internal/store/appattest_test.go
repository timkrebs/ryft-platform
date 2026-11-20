package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Byte values are derived at run time from a label (no literal that a secret scanner reads as a key).

// attestKeyID is a 32-byte key id for label.
func attestKeyID(label string) [32]byte { return sha256.Sum256([]byte("key-id/" + label)) }

// attestHash is a challenge's hash for label.
func attestHash(label string) [32]byte { return sha256.Sum256([]byte("challenge/" + label)) }

// attestPublicKey is a 65-byte value in the uncompressed P-256 form (0x04 || X || Y). Not a point on the
// curve: the store does not check that, internal/appattest does.
func attestPublicKey(label string) []byte {
	x, y := sha256.Sum256([]byte("x/"+label)), sha256.Sum256([]byte("y/"+label))
	return append(append([]byte{4}, x[:]...), y[:]...)
}

// seedAttest gives the account an attested key and an open challenge.
func seedAttest(t *testing.T, st *Store, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	label := id.String()
	if err := st.PutAttestKey(ctx, AppAttestKey{UserID: id, KeyID: attestKeyID(label), PublicKey: attestPublicKey(label),
		Environment: "production"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAttestChallenge(ctx, id, attestHash(label), time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

// attestRows counts the account's rows in both App Attest tables.
func attestRows(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM app_attest_keys WHERE user_id = $1)
		+ (SELECT count(*) FROM app_attest_challenges WHERE user_id = $1)`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// attestUsers creates n accounts.
func attestUsers(t *testing.T, st *Store, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for i := range ids {
		ids[i] = uuid.New()
		if _, err := st.EnsureUser(context.Background(), Identity{ID: ids[i]}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func putKey(t *testing.T, st *Store, user uuid.UUID, label string, at time.Time) [32]byte {
	t.Helper()
	id := attestKeyID(label)
	if err := st.PutAttestKey(context.Background(), AppAttestKey{UserID: user, KeyID: id, PublicKey: attestPublicKey(label),
		Environment: "production", AttestedAt: at}); err != nil {
		t.Fatalf("put %s: %v", label, err)
	}
	return id
}

func consume(t *testing.T, st *Store, user uuid.UUID, hash [32]byte, now time.Time) bool {
	t.Helper()
	ok, err := st.ConsumeAttestChallenge(context.Background(), user, hash, now)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestAttestChallengeIsSingleUse(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := attestUsers(t, st, 1)[0]
	now := time.Now()
	h := attestHash("once")
	if err := st.CreateAttestChallenge(ctx, a, h, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !consume(t, st, a, h, now) {
		t.Fatal("an open challenge is accepted once")
	}
	if consume(t, st, a, h, now) {
		t.Fatal("a used challenge is refused")
	}
	if consume(t, st, a, attestHash("never issued"), now) {
		t.Fatal("an unknown challenge is refused")
	}
	if n := attestRows(t, pool, a); n != 0 {
		t.Fatalf("rows left: %d", n)
	}
}

func TestAttestChallengeExpires(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := attestUsers(t, st, 1)[0]
	now := time.Now().Truncate(time.Microsecond) // the column's precision
	expires := now.Add(5 * time.Minute)
	late, onTime := attestHash("late"), attestHash("on time")
	for _, h := range [][32]byte{late, onTime} {
		if err := st.CreateAttestChallenge(ctx, a, h, expires); err != nil {
			t.Fatal(err)
		}
	}
	if consume(t, st, a, late, expires) {
		t.Fatal("a challenge is refused at its expiry")
	}
	if n := attestRows(t, pool, a); n != 1 {
		t.Fatalf("the expired challenge is deleted by the attempt: %d rows, want 1", n)
	}
	if consume(t, st, a, late, now) {
		t.Fatal("an expired challenge stays used up")
	}
	if !consume(t, st, a, onTime, expires.Add(-time.Microsecond)) {
		t.Fatal("a challenge is accepted until its expiry")
	}
	// A challenge already expired by the database's clock is dropped when the next one is created.
	if err := st.CreateAttestChallenge(ctx, a, attestHash("stale"), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAttestChallenge(ctx, a, attestHash("fresh"), now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := attestRows(t, pool, a); n != 1 {
		t.Fatalf("creating a challenge drops the expired ones: %d rows, want 1", n)
	}
}

func TestAttestChallengeOfAnotherAccount(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	ids := attestUsers(t, st, 2)
	a, b := ids[0], ids[1]
	now := time.Now()
	h := attestHash("a's")
	if err := st.CreateAttestChallenge(ctx, a, h, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if consume(t, st, b, h, now) {
		t.Fatal("another account's challenge is refused")
	}
	if !consume(t, st, a, h, now) {
		t.Fatal("another account naming it does not use it up")
	}
}

func TestAttestChallengeSixthCancelsTheOldest(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	ids := attestUsers(t, st, 2)
	a, b := ids[0], ids[1]
	now := time.Now()
	if err := st.CreateAttestChallenge(ctx, b, attestHash("b's"), now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	hashes := make([][32]byte, MaxOpenAttestChallenges+1)
	for i := range hashes {
		hashes[i] = attestHash(fmt.Sprintf("open-%d", i))
		if err := st.CreateAttestChallenge(ctx, a, hashes[i], now.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if n := attestRows(t, pool, a); n != MaxOpenAttestChallenges {
		t.Fatalf("open challenges: %d, want %d", n, MaxOpenAttestChallenges)
	}
	if consume(t, st, a, hashes[0], now) {
		t.Fatal("the sixth challenge cancels the oldest")
	}
	for i, h := range hashes[1:] {
		if !consume(t, st, a, h, now) {
			t.Errorf("challenge %d must still be open", i+1)
		}
	}
	if n := attestRows(t, pool, b); n != 1 {
		t.Fatalf("another account's challenge is not counted or cancelled: %d", n)
	}
}

func TestPutAttestKeyReplacesAndResetsTheCounter(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := attestUsers(t, st, 1)[0]
	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	id := putKey(t, st, a, "phone", first)
	k, err := st.AttestKey(ctx, a, id)
	if err != nil {
		t.Fatal(err)
	}
	if k.UserID != a || k.KeyID != id || string(k.PublicKey) != string(attestPublicKey("phone")) || k.Environment != "production" ||
		k.Counter != 0 || !k.AttestedAt.Equal(first) || !k.LastUsedAt.Equal(first) {
		t.Fatalf("stored key: %+v", k)
	}
	if ok, err := st.AdvanceAttestCounter(ctx, a, id, 7, first.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("advance: %v %v", ok, err)
	}
	again := first.Add(30 * time.Minute)
	if err := st.PutAttestKey(ctx, AppAttestKey{UserID: a, KeyID: id, PublicKey: attestPublicKey("phone"),
		Environment: "production", Counter: 99, AttestedAt: again, LastUsedAt: again.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	k, err = st.AttestKey(ctx, a, id)
	if err != nil {
		t.Fatal(err)
	}
	if k.Counter != 0 || !k.AttestedAt.Equal(again) || !k.LastUsedAt.Equal(again) {
		t.Fatalf("a re-attested key starts again at 0, attested and used now: %+v", k)
	}
	if n := attestRows(t, pool, a); n != 1 {
		t.Fatalf("an upsert, not a second row: %d", n)
	}
	if _, err := st.AttestKey(ctx, a, attestKeyID("tablet")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown key: %v", err)
	}
	other := attestUsers(t, st, 1)[0]
	if _, err := st.AttestKey(ctx, other, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another account's key: %v", err)
	}
	// The database's CHECKs are the backstop for what internal/appattest refuses first.
	for name, k := range map[string]AppAttestKey{
		"development": {UserID: a, KeyID: attestKeyID("dev"), PublicKey: attestPublicKey("dev"), Environment: "development"},
		"short key":   {UserID: a, KeyID: attestKeyID("short"), PublicKey: attestPublicKey("short")[:33], Environment: "production"},
		"compressed":  {UserID: a, KeyID: attestKeyID("02"), PublicKey: append([]byte{2}, attestPublicKey("02")[1:]...), Environment: "production"},
	} {
		if err := st.PutAttestKey(ctx, k); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestPutAttestKeyEvictsTheLeastRecentlyUsed(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	ids := attestUsers(t, st, 2)
	a, b := ids[0], ids[1]
	base := time.Now().UTC().Add(-48 * time.Hour)
	putKey(t, st, b, "b's", base)
	keys := make([][32]byte, MaxAttestKeys)
	for i := range keys {
		keys[i] = putKey(t, st, a, fmt.Sprintf("install-%d", i), base.Add(time.Duration(i)*time.Minute))
	}
	// The first attested is the most recently used; install-1 is now the least recently used.
	if ok, err := st.AdvanceAttestCounter(ctx, a, keys[0], 1, base.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("advance: %v %v", ok, err)
	}
	eleventh := putKey(t, st, a, "install-10", base.Add(2*time.Hour))
	if n := attestRows(t, pool, a); n != MaxAttestKeys {
		t.Fatalf("keys: %d, want %d", n, MaxAttestKeys)
	}
	if _, err := st.AttestKey(ctx, a, keys[1]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the least recently used key is evicted: %v", err)
	}
	for _, id := range [][32]byte{keys[0], keys[2], keys[MaxAttestKeys-1], eleventh} {
		if _, err := st.AttestKey(ctx, a, id); err != nil {
			t.Errorf("kept: %v", err)
		}
	}
	// A new key is kept even when it is the oldest by the clock it was given.
	old := putKey(t, st, a, "install-old", base.Add(-24*time.Hour))
	if _, err := st.AttestKey(ctx, a, old); err != nil {
		t.Fatalf("the key just attested is never the one evicted: %v", err)
	}
	if n := attestRows(t, pool, a); n != MaxAttestKeys {
		t.Fatalf("keys: %d, want %d", n, MaxAttestKeys)
	}
	if n := attestRows(t, pool, b); n != 1 {
		t.Fatalf("another account's key is not counted or evicted: %d", n)
	}
}

func TestAdvanceAttestCounterIsACompareAndSet(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	a := attestUsers(t, st, 1)[0]
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	id := putKey(t, st, a, "phone", at)
	step := func(counter uint32, now time.Time, want bool) {
		t.Helper()
		ok, err := st.AdvanceAttestCounter(ctx, a, id, counter, now)
		if err != nil || ok != want {
			t.Fatalf("counter %d: %v %v, want %v", counter, ok, err, want)
		}
	}
	used := at.Add(10 * time.Minute)
	step(0, at.Add(time.Minute), false) // equal to the attestation's 0
	step(5, used, true)
	step(5, at.Add(20*time.Minute), false) // a replay
	step(4, at.Add(20*time.Minute), false) // lower
	k, err := st.AttestKey(ctx, a, id)
	if err != nil || k.Counter != 5 || !k.LastUsedAt.Equal(used) {
		t.Fatalf("a refused step changes nothing: %+v %v", k, err)
	}
	step(1<<32-1, used.Add(time.Minute), true) // the largest uint32 fits the column
	if k, err = st.AttestKey(ctx, a, id); err != nil || k.Counter != 1<<32-1 {
		t.Fatalf("max counter: %+v %v", k, err)
	}
	if ok, err := st.AdvanceAttestCounter(ctx, a, attestKeyID("unknown"), 9, used); err != nil || ok {
		t.Fatalf("an unknown key is not advanced: %v %v", ok, err)
	}
}

func TestPurgeAppAttest(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	ids := attestUsers(t, st, 2)
	a, b := ids[0], ids[1]
	now := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond) // the tick's clock, ahead of the database's
	// Challenges: one expired at now, one open; on two accounts.
	if err := st.CreateAttestChallenge(ctx, a, attestHash("expired"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAttestChallenge(ctx, b, attestHash("open"), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Keys: idle 90 days and a microsecond, idle exactly 90 days, used 89 days ago.
	idle := putKey(t, st, a, "idle", now.Add(-AttestKeyIdle-time.Microsecond))
	edge := putKey(t, st, b, "edge", now.Add(-AttestKeyIdle))
	recent := putKey(t, st, b, "recent", now.Add(-AttestKeyIdle-time.Hour))
	if ok, err := st.AdvanceAttestCounter(ctx, b, recent, 1, now.Add(-89*24*time.Hour)); err != nil || !ok {
		t.Fatalf("advance: %v %v", ok, err)
	}
	challenges, keys, err := st.PurgeAppAttest(ctx, now)
	if err != nil || challenges != 1 || keys != 1 {
		t.Fatalf("purge: %d challenges, %d keys, %v; want 1 and 1", challenges, keys, err)
	}
	if _, err := st.AttestKey(ctx, a, idle); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the idle key is gone: %v", err)
	}
	for _, id := range [][32]byte{edge, recent} {
		if _, err := st.AttestKey(ctx, b, id); err != nil {
			t.Errorf("a key used within 90 days stays: %v", err)
		}
	}
	if n := attestRows(t, pool, b); n != 3 {
		t.Fatalf("b keeps its open challenge and two keys: %d", n)
	}
	if challenges, keys, err = st.PurgeAppAttest(ctx, now); err != nil || challenges != 0 || keys != 0 {
		t.Fatalf("a second purge finds nothing: %d %d %v", challenges, keys, err)
	}
}

func TestAttestationsSince(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	ids := attestUsers(t, st, 2)
	a, b := ids[0], ids[1]
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-24 * time.Hour)
	if n, oldest, err := st.AttestationsSince(ctx, a, since); err != nil || n != 0 || !oldest.IsZero() {
		t.Fatalf("none yet: %d %v %v", n, oldest, err)
	}
	putKey(t, st, a, "30h", now.Add(-30*time.Hour))
	putKey(t, st, a, "24h", since) // at since counts
	putKey(t, st, a, "1h", now.Add(-time.Hour))
	putKey(t, st, b, "b's", now.Add(-2*time.Hour))
	n, oldest, err := st.AttestationsSince(ctx, a, since)
	if err != nil || n != 2 || !oldest.Equal(since) {
		t.Fatalf("since: %d, oldest %v, %v; want 2 and %v", n, oldest, err, since)
	}
	// A re-attestation moves the key's attested_at: it counts once, at its new time.
	putKey(t, st, a, "30h", now.Add(-time.Minute))
	if n, oldest, err = st.AttestationsSince(ctx, a, since); err != nil || n != 3 || !oldest.Equal(since) {
		t.Fatalf("after a re-attestation: %d %v %v", n, oldest, err)
	}
}

// The account's deletion takes both tables' rows and nobody else's; TestDeleteUserErasesEverythingAndStaysDeleted
// covers it through owned as well. The foreign keys cascade for an image from before 0037, whose DeleteUser
// does not name the tables.
func TestAppAttestGoesWithTheAccount(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	ids := attestUsers(t, st, 3)
	a, b, c := ids[0], ids[1], ids[2]
	for _, id := range ids {
		seedAttest(t, st, id)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := attestRows(t, pool, a); n != 0 {
		t.Fatalf("DeleteUser leaves %d App Attest rows", n)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if n := attestRows(t, pool, b); n != 0 {
		t.Fatalf("the cascade leaves %d App Attest rows", n)
	}
	if n := attestRows(t, pool, c); n != 2 {
		t.Fatalf("another account keeps its rows: %d", n)
	}
	if err := st.PutAttestKey(ctx, AppAttestKey{UserID: a, KeyID: attestKeyID("after"), PublicKey: attestPublicKey("after"),
		Environment: "production"}); err == nil {
		t.Fatal("a deleted account cannot store a key")
	}
	if err := st.CreateAttestChallenge(ctx, a, attestHash("after"), time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a deleted account cannot open a challenge")
	}
}
