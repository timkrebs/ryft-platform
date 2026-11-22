package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

func claimUsers(t *testing.T, st *Store, n int) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, n)
	for k := range ids {
		ids[k] = uuid.New()
		if _, err := st.EnsureUser(context.Background(), Identity{ID: ids[k]}); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

func pushDevicesOf(t *testing.T, st *Store, id uuid.UUID) []uuid.UUID {
	t.Helper()
	u, _, err := st.GetUser(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u.PushDevices
}

func tokenCount(t *testing.T, pool *pgxpool.Pool, user, device uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM push_tokens WHERE user_id=$1 AND device_id=$2`, user, device).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func claimReg(c string) model.PushRegistration {
	return model.PushRegistration{Token: strings.Repeat(c, 64), Env: "sandbox", Locale: "en"}
}

func TestClaimDevice(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := claimUsers(t, st, 3)
	a, b, c := u[0], u[1], u[2]
	d, d2, d3 := uuid.New(), uuid.New(), uuid.New()

	if got := pushDevicesOf(t, st, a); len(got) != 0 {
		t.Fatalf("a new account has no installs: %v", got)
	}
	// A holds D and D2; C holds D3.
	for _, x := range []struct {
		user, device uuid.UUID
		tok          string
	}{{a, d, "a"}, {a, d2, "b"}, {c, d3, "c"}} {
		if err := st.RegisterPushToken(ctx, x.user, x.device, claimReg(x.tok)); err != nil {
			t.Fatal(err)
		}
	}
	if got := pushDevicesOf(t, st, a); !slices.Equal(got, []uuid.UUID{d, d2}) {
		t.Fatalf("a's list after registering: %v", got)
	}

	// B claims D: A's token for D and A's entry go; A's D2 and C's D3 stay.
	dropped, err := st.ClaimDevice(ctx, b, d)
	if err != nil || dropped != 1 {
		t.Fatalf("claim: dropped %d, %v", dropped, err)
	}
	if got := pushDevicesOf(t, st, b); !slices.Equal(got, []uuid.UUID{d}) {
		t.Fatalf("b's list: %v", got)
	}
	if got := pushDevicesOf(t, st, a); !slices.Equal(got, []uuid.UUID{d2}) {
		t.Fatalf("a's list: %v", got)
	}
	if tokenCount(t, pool, a, d) != 0 || tokenCount(t, pool, a, d2) != 1 || tokenCount(t, pool, c, d3) != 1 {
		t.Fatal("the claim touched a token that is not another account's on the install")
	}
	if got := pushDevicesOf(t, st, c); !slices.Equal(got, []uuid.UUID{d3}) {
		t.Fatalf("c's list: %v", got)
	}

	// Again: nothing changes and nothing is dropped.
	if dropped, err = st.ClaimDevice(ctx, b, d); err != nil || dropped != 0 {
		t.Fatalf("repeat: dropped %d, %v", dropped, err)
	}
	if got := pushDevicesOf(t, st, b); !slices.Equal(got, []uuid.UUID{d}) {
		t.Fatalf("b's list after a repeat: %v", got)
	}
	if tokenCount(t, pool, a, d2) != 1 || !slices.Equal(pushDevicesOf(t, st, a), []uuid.UUID{d2}) {
		t.Fatal("a repeat changed another account")
	}

	// A claim for an account's own install keeps its token and moves the install to the end.
	if err := st.RegisterPushToken(ctx, b, d2, claimReg("d")); err != nil { // takes D2 from A
		t.Fatal(err)
	}
	if _, err := st.ClaimDevice(ctx, b, d); err != nil {
		t.Fatal(err)
	}
	if got := pushDevicesOf(t, st, b); !slices.Equal(got, []uuid.UUID{d2, d}) {
		t.Fatalf("b's list, D newest: %v", got)
	}
	if tokenCount(t, pool, b, d2) != 1 || tokenCount(t, pool, a, d2) != 0 {
		t.Fatal("registering for D2 takes it from A and keeps B's token")
	}
}

// The list keeps the newest MaxPushDevices; the oldest drop off it, never a token.
func TestClaimDeviceKeepsTheNewest(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := claimUsers(t, st, 1)[0]
	ids := make([]uuid.UUID, MaxPushDevices+2)
	for k := range ids {
		ids[k] = uuid.New()
	}
	if err := st.RegisterPushToken(ctx, a, ids[0], claimReg("a")); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids[1 : MaxPushDevices+1] { // 9 installs in all
		if _, err := st.ClaimDevice(ctx, a, id); err != nil {
			t.Fatal(err)
		}
	}
	want := slices.Clone(ids[1 : MaxPushDevices+1])
	if got := pushDevicesOf(t, st, a); !slices.Equal(got, want) {
		t.Fatalf("the ninth claim drops the oldest: %v, want %v", got, want)
	}
	if tokenCount(t, pool, a, ids[0]) != 1 {
		t.Fatal("dropping an install from the list dropped its token")
	}

	// An install already in the list moves to the end; the list stays at 8.
	if _, err := st.ClaimDevice(ctx, a, ids[3]); err != nil {
		t.Fatal(err)
	}
	want = append(slices.Delete(slices.Clone(want), 2, 3), ids[3])
	if got := pushDevicesOf(t, st, a); !slices.Equal(got, want) {
		t.Fatalf("re-claiming moves it to the end: %v, want %v", got, want)
	}

	// A new one drops the oldest again.
	if _, err := st.ClaimDevice(ctx, a, ids[MaxPushDevices+1]); err != nil {
		t.Fatal(err)
	}
	want = append(want[1:], ids[MaxPushDevices+1])
	if got := pushDevicesOf(t, st, a); len(got) != MaxPushDevices || !slices.Equal(got, want) {
		t.Fatalf("a tenth: %v, want %v", got, want)
	}
}

// RegisterPushToken claims its path id too, so a client without the header gets the same result,
// and a second registration for the same install replaces the token.
func TestRegisterPushTokenClaimsAndReplaces(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	u := claimUsers(t, st, 2)
	a, b := u[0], u[1]
	d := uuid.New()

	if err := st.RegisterPushToken(ctx, a, d, claimReg("a")); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterPushToken(ctx, b, d, claimReg("b")); err != nil {
		t.Fatal(err)
	}
	if tokenCount(t, pool, a, d) != 0 || tokenCount(t, pool, b, d) != 1 {
		t.Fatal("B's registration for the install takes it from A")
	}
	if !slices.Equal(pushDevicesOf(t, st, b), []uuid.UUID{d}) || len(pushDevicesOf(t, st, a)) != 0 {
		t.Fatalf("lists: a %v, b %v", pushDevicesOf(t, st, a), pushDevicesOf(t, st, b))
	}

	if err := st.RegisterPushToken(ctx, b, d, claimReg("c")); err != nil {
		t.Fatal(err)
	}
	var token string
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*), min(token) FROM push_tokens WHERE user_id=$1 AND device_id=$2`, b, d).Scan(&n, &token); err != nil {
		t.Fatal(err)
	}
	if n != 1 || token != strings.Repeat("c", 64) {
		t.Fatalf("one row with the second token: %d rows, token %q", n, token)
	}
	if !slices.Equal(pushDevicesOf(t, st, b), []uuid.UUID{d}) {
		t.Fatal("registering twice listed the install twice")
	}
}
