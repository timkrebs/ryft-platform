package store

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
)

func TestSleepBaselineSources(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	u, err := st.EnsureUser(ctx, Identity{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	check := func(step string, u model.User, base int, source string, learned *int) {
		t.Helper()
		if u.SleepBaseMin != base || u.SleepBaseSource != source ||
			(learned == nil) != (u.SleepBaseLearnedMin == nil) || (learned != nil && *learned != *u.SleepBaseLearnedMin) {
			t.Fatalf("%s: base %d source %s learned %v", step, u.SleepBaseMin, u.SleepBaseSource, u.SleepBaseLearnedMin)
		}
	}
	reload := func() model.User {
		t.Helper()
		u, _, err := st.GetUser(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	check("new", u, 460, model.BaseDefault, nil)

	if changed, err := st.SetLearnedBase(ctx, id, 480); err != nil || !changed {
		t.Fatalf("first estimate: changed=%v err=%v", changed, err)
	}
	check("learned", reload(), 480, model.BaseLearned, ptr(480))
	if changed, _ := st.SetLearnedBase(ctx, id, 480); changed {
		t.Fatal("the same estimate changes nothing")
	}

	u, err = st.UpdateUser(ctx, id, model.UserPatch{SleepBaseMin: ptr(500)})
	if err != nil {
		t.Fatal(err)
	}
	check("set by hand", u, 500, model.BaseManual, ptr(480))
	// Manual wins: a new estimate is stored, but not used.
	if changed, err := st.SetLearnedBase(ctx, id, 490); err != nil || changed {
		t.Fatalf("estimate while manual: changed=%v err=%v", changed, err)
	}
	check("manual wins", reload(), 500, model.BaseManual, ptr(490))
	u, _ = st.UpdateUser(ctx, id, model.UserPatch{MaxHR: ptr(185)})
	check("another setting keeps the source", u, 500, model.BaseManual, ptr(490))

	u, _ = st.UpdateUser(ctx, id, model.UserPatch{SleepBaseManual: ptr(false)})
	check("back to learned", u, 490, model.BaseLearned, ptr(490))
	u, _ = st.UpdateUser(ctx, id, model.UserPatch{SleepBaseManual: ptr(true)})
	check("freeze the current value", u, 490, model.BaseManual, ptr(490))
	if changed, _ := st.SetLearnedBase(ctx, id, 470); changed {
		t.Fatal("frozen: the estimate is not used")
	}

	// Without an estimate, leaving manual goes back to the default.
	other := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: other}); err != nil {
		t.Fatal(err)
	}
	u, _ = st.UpdateUser(ctx, other, model.UserPatch{SleepBaseMin: ptr(420)})
	check("other manual", u, 420, model.BaseManual, nil)
	u, _ = st.UpdateUser(ctx, other, model.UserPatch{SleepBaseManual: ptr(false)})
	check("other default", u, 460, model.BaseDefault, nil)

	if _, err := st.SetLearnedBase(ctx, uuid.New(), 480); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

// Scoring writes the weekly estimate with its own role: the column grants of
// migration 0013 must cover SetLearnedBase's statement (FOR UPDATE included).
func TestSetLearnedBaseAsScoringRole(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	const pw = "pw-ryft_scoring-0123456789"
	if err := db.SetRolePassword(ctx, pool, "ryft_scoring", pw); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("ryft_scoring", pw)
	sp, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sp.Close)
	if changed, err := New(sp).SetLearnedBase(ctx, id, 480); err != nil || !changed {
		t.Fatalf("as ryft_scoring: changed=%v err=%v", changed, err)
	}
	if _, err := New(sp).UpdateUser(ctx, id, model.UserPatch{SleepBaseManual: ptr(false)}); err == nil {
		t.Fatal("scoring must not change the user's settings")
	}
}
