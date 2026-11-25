package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/periodization"
	"github.com/timkrebs/ryft/internal/wearables"
)

const relayPolicyFixture = "2026-10-26"

type leaseFixture struct {
	st           *Store
	pool         *pgxpool.Pool
	ctx          context.Context
	owner, other uuid.UUID
	now          time.Time
	goal         model.GoalProfile
	delivery     wearables.Delivery
}

func newLeaseFixture(t *testing.T) leaseFixture {
	t.Helper()
	st, pool, owner, other := nutritionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	f := leaseFixture{st: st, pool: pool, ctx: ctx, owner: owner, other: other, now: now,
		goal:     model.GoalProfile{Goal: periodization.CyclingRace, HeightCM: 178},
		delivery: wearables.Delivery{User: "fixture-lease-owner", Weights: []wearables.WeighIn{{At: now.Add(-time.Hour), Mass: 70}}}}
	for _, account := range []uuid.UUID{owner, other} {
		f.exec(t, "UPDATE users SET birth_year=1990,tz='UTC' WHERE id=$1", account)
		f.check(t, st.GrantConsent(ctx, account, relayPolicyFixture, nil))
		_, err := st.PutN2RProfile(ctx, account, model.N2RProfile{Sex: "male", BodyMassKG: 70, ExpectedBedtime: "23:00"})
		f.check(t, err)
		_, err = st.PutCoachingGoal(ctx, account, f.goal, now)
		f.check(t, err)
	}
	f.link(t, owner)
	return f
}

func (f leaseFixture) check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f leaseFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	_, err := f.pool.Exec(f.ctx, query, args...)
	f.check(t, err)
}

func (f leaseFixture) link(t *testing.T, owner uuid.UUID) {
	t.Helper()
	f.exec(t, `INSERT INTO nutrition.wearable_links(provider,provider_user,user_id) VALUES('garmin',$1,$2)`, f.delivery.User, owner)
}

func (f leaseFixture) enqueue(t *testing.T) {
	t.Helper()
	f.check(t, f.st.EnqueueWearable(f.ctx, "garmin", relayPolicyFixture, []wearables.Delivery{f.delivery}))
	f.exec(t, `UPDATE nutrition.wearable_inbox SET due_at=$1 WHERE completed_at IS NULL`, f.now)
}

func (f leaseFixture) claim(t *testing.T, at time.Time) WearableJob {
	t.Helper()
	job, err := f.st.ClaimWearable(f.ctx, at, relayPolicyFixture, []string{"garmin"})
	f.check(t, err)
	if job == nil {
		t.Fatal("expected synthetic queue lease")
	}
	return *job
}

func (f leaseFixture) count(t *testing.T, query string, want int, args ...any) {
	t.Helper()
	if got := countRows(t, f.pool, query, args...); got != want {
		t.Fatalf("synthetic fixture count: got %d, want %d", got, want)
	}
}

func TestWearableLeaseControls(t *testing.T) {
	const allWeights = "SELECT count(*) FROM nutrition.wearable_weights"
	const retained = "SELECT count(*) FROM nutrition.wearable_inbox WHERE completed_at IS NULL AND body<>'{}'::jsonb"
	const scrubbed = "SELECT count(*) FROM nutrition.wearable_inbox WHERE completed_at IS NOT NULL AND body='{}'::jsonb"
	t.Run("consent and account", func(t *testing.T) {
		f := newLeaseFixture(t)
		unlinked := f.delivery
		unlinked.User = "fixture-unlinked"
		f.check(t, f.st.EnqueueWearable(f.ctx, "garmin", relayPolicyFixture, []wearables.Delivery{unlinked}))
		f.count(t, retained, 0)
		f.enqueue(t)
		job := f.claim(t, f.now)

		mismatch := f.delivery
		mismatch.User = unlinked.User
		if err := f.st.ApplyWearable(f.ctx, job, mismatch, relayPolicyFixture, f.now); err == nil {
			t.Fatal("accepted mismatched connector response")
		}
		forged := job
		forged.User = f.other
		f.check(t, f.st.ApplyWearable(f.ctx, forged, f.delivery, relayPolicyFixture, f.now))
		f.count(t, allWeights, 0)
		f.count(t, retained, 1)

		f.check(t, f.st.RevokeConsent(f.ctx, f.owner))
		f.count(t, retained, 0)
		f.count(t, scrubbed, 1)
		f.check(t, f.st.ApplyWearable(f.ctx, job, f.delivery, relayPolicyFixture, f.now))
		different := f.delivery
		different.Weights = []wearables.WeighIn{{At: f.now.Add(-30 * time.Minute), Mass: 71}}
		f.check(t, f.st.EnqueueWearable(f.ctx, "garmin", relayPolicyFixture, []wearables.Delivery{different}))
		f.count(t, allWeights, 0)
		f.count(t, retained, 0)
		f.count(t, scrubbed, 1)

		f.check(t, f.st.GrantConsent(f.ctx, f.owner, relayPolicyFixture, nil))
		f.check(t, f.st.ApplyWearable(f.ctx, job, f.delivery, relayPolicyFixture, f.now))
		f.check(t, f.st.EnqueueWearable(f.ctx, "garmin", relayPolicyFixture, []wearables.Delivery{f.delivery}))
		f.count(t, allWeights, 0)
		f.count(t, retained, 0)
		f.count(t, scrubbed, 1)
		f.delivery = different
		f.enqueue(t)
		fresh := f.claim(t, f.now)
		f.check(t, f.st.ApplyWearable(f.ctx, fresh, f.delivery, relayPolicyFixture, f.now))
		f.count(t, allWeights, 1)
		f.count(t, scrubbed, 2)
	})
	t.Run("opt-out and relink", func(t *testing.T) {
		f := newLeaseFixture(t)
		f.enqueue(t)
		old := f.claim(t, f.now)
		f.check(t, f.st.DeleteCoachingGoal(f.ctx, f.owner))
		_, err := f.st.PutCoachingGoal(f.ctx, f.owner, f.goal, f.now)
		f.check(t, err)
		f.link(t, f.owner)
		f.enqueue(t)
		current := f.claim(t, f.now)
		if current.ID == old.ID {
			t.Fatal("new link kept the old delivery")
		}
		f.check(t, f.st.ApplyWearable(f.ctx, old, f.delivery, relayPolicyFixture, f.now))
		f.count(t, allWeights, 0)
		f.count(t, retained, 1)
		f.check(t, f.st.ApplyWearable(f.ctx, current, f.delivery, relayPolicyFixture, f.now))
		f.count(t, allWeights, 1)
		f.count(t, scrubbed, 1)
	})
	t.Run("concurrent duplicate and stale attempt", func(t *testing.T) {
		f := newLeaseFixture(t)
		batch := []wearables.Delivery{f.delivery}
		start := make(chan struct{})
		errs := make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				errs <- f.st.EnqueueWearable(f.ctx, "garmin", relayPolicyFixture, batch)
			}()
		}
		close(start)
		for range 2 {
			f.check(t, <-errs)
		}
		f.count(t, retained, 1)
		f.exec(t, `UPDATE nutrition.wearable_inbox SET due_at=$1`, f.now)
		old := f.claim(t, f.now)
		retryAt := f.now.Add(16 * time.Minute)
		current := f.claim(t, retryAt)
		if current.ID != old.ID || current.Attempt != old.Attempt+1 {
			t.Fatal("expected the same delivery on a new attempt")
		}
		f.check(t, f.st.ApplyWearable(f.ctx, old, f.delivery, relayPolicyFixture, retryAt))
		f.count(t, allWeights, 0)

		// Different synthetic timestamps make a second domain effect observable.
		for _, at := range []time.Time{f.now.Add(-time.Hour), f.now.Add(-30 * time.Minute)} {
			data := f.delivery
			data.Weights = []wearables.WeighIn{{At: at, Mass: 70}}
			go func() { errs <- f.st.ApplyWearable(f.ctx, current, data, relayPolicyFixture, retryAt) }()
		}
		for range 2 {
			f.check(t, <-errs)
		}
		f.count(t, allWeights, 1)
		f.count(t, scrubbed, 1)
		f.check(t, f.st.EnqueueWearable(f.ctx, "garmin", relayPolicyFixture, batch))
		f.count(t, retained, 0)
		f.count(t, scrubbed, 1)
	})
	t.Run("downstream rollback and retry", func(t *testing.T) {
		f := newLeaseFixture(t)
		f.delivery.Weights = append(f.delivery.Weights, wearables.WeighIn{At: f.now.Add(-30 * time.Minute), Mass: 71})
		f.enqueue(t)
		job := f.claim(t, f.now)
		f.exec(t, `ALTER TABLE nutrition.wearable_weights ADD CONSTRAINT fixture_failure CHECK (mass_kg<>71)`)
		if err := f.st.ApplyWearable(f.ctx, job, f.delivery, relayPolicyFixture, f.now); err == nil {
			t.Fatal("expected downstream database failure")
		}
		f.count(t, allWeights, 0)
		f.count(t, retained, 1)
		f.count(t, scrubbed, 0)
		f.check(t, f.st.FinishWearable(f.ctx, job, f.now, false))
		if pending, err := f.st.ClaimWearable(f.ctx, f.now, relayPolicyFixture, []string{"garmin"}); err != nil || pending != nil {
			t.Fatalf("work retried before the lease was due: %v", err)
		}
		f.exec(t, `ALTER TABLE nutrition.wearable_weights DROP CONSTRAINT fixture_failure`)
		retryAt := f.now.Add(16 * time.Minute)
		retry := f.claim(t, retryAt)
		f.check(t, f.st.ApplyWearable(f.ctx, retry, f.delivery, relayPolicyFixture, retryAt))
		f.count(t, allWeights, 2)
		f.count(t, scrubbed, 1)
	})
}
