package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// asNotify is the store as notify uses it: role ryft_notify, so a query its
// grants (0015) do not cover fails here.
func asNotify(t *testing.T, pool *pgxpool.Pool) *Store {
	t.Helper()
	ctx := context.Background()
	const pw = "pw-ryft_notify-0123456789"
	if err := db.SetRolePassword(ctx, pool, "ryft_notify", pw); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("ryft_notify", pw)
	np, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(np.Close)
	return New(np)
}

func TestPushTokensMoveWithTheInstall(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b, phone := uuid.New(), uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{a, b} {
		if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	tok := hexToken(a)
	reg := model.PushRegistration{Token: tok, Env: "sandbox", Locale: "de"}
	if err := st.RegisterPushToken(ctx, a, phone, reg); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterPushToken(ctx, a, phone, reg); err != nil {
		t.Fatal("registering again is an update")
	}
	// b signs in on the same phone: the token is b's now.
	if err := st.RegisterPushToken(ctx, b, phone, reg); err != nil {
		t.Fatal(err)
	}
	ns := asNotify(t, pool)
	ra, _ := ns.Recipient(ctx, a)
	rb, err := ns.Recipient(ctx, b)
	if err != nil || len(ra.Tokens) != 0 || len(rb.Tokens) != 1 || rb.Tokens[0].Locale != "de" || rb.Tokens[0].DeviceID != phone {
		t.Fatalf("a %+v, b %+v (%v)", ra.Tokens, rb.Tokens, err)
	}
	if err := ns.DropPushToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if rb, _ = ns.Recipient(ctx, b); len(rb.Tokens) != 0 {
		t.Fatal("notify drops a dead token")
	}
	if err := st.DeletePushToken(ctx, b, phone); err != nil {
		t.Fatal("deleting a missing token is fine")
	}
	if _, err := ns.Recipient(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestNotificationPrefs(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	u, _ := st.EnsureUser(ctx, Identity{ID: id})
	if len(u.Notifications.Categories) != 4 || !u.Notifications.Categories[model.NotifyPR] || u.Notifications.QuietHours != nil {
		t.Fatalf("defaults: %+v", u.Notifications)
	}
	var p model.NotificationPatch
	_ = json.Unmarshal([]byte(`{"categories": {"PR": false}, "quiet_hours": {"from": "22:30", "to": "06:45"}}`), &p)
	u, err := st.UpdateNotificationPrefs(ctx, id, p)
	if err != nil || u.Notifications.Categories[model.NotifyPR] || !u.Notifications.Categories[model.NotifySyncStale] ||
		*u.Notifications.QuietHours != (model.QuietHours{From: "22:30", To: "06:45"}) {
		t.Fatalf("set: %+v %v", u.Notifications, err)
	}
	p = model.NotificationPatch{}
	_ = json.Unmarshal([]byte(`{"categories": {"SYNC_STALE": false}}`), &p)
	if u, _ = st.UpdateNotificationPrefs(ctx, id, p); u.Notifications.Categories[model.NotifyPR] || u.Notifications.QuietHours == nil {
		t.Fatalf("categories merge, quiet hours stay: %+v", u.Notifications)
	}
	p = model.NotificationPatch{}
	_ = json.Unmarshal([]byte(`{"quiet_hours": null}`), &p)
	if u, _ = st.UpdateNotificationPrefs(ctx, id, p); u.Notifications.QuietHours != nil || u.Notifications.Categories[model.NotifySyncStale] {
		t.Fatalf("null clears the quiet hours only: %+v", u.Notifications)
	}
	if g, _, _ := st.GetUser(ctx, id); g.Notifications.Categories[model.NotifyPR] {
		t.Fatal("GET /v1/me shows them")
	}
}

func TestNotifyReadsAndClaims(t *testing.T) {
	st, pool, a, _ := trainStore(t)
	ctx := context.Background()
	ns := asNotify(t, pool)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := st.UpsertSamples(ctx, a, []model.Sample{sample(now.Add(-21 * time.Hour))}); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterPushToken(ctx, a, uuid.New(), model.PushRegistration{Token: hexToken(a), Env: "production", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	cands, err := ns.StaleCandidates(ctx, now)
	if err != nil || len(cands) != 1 || cands[0].LastSample == nil || !cands[0].LastSample.Equal(now.Add(-21*time.Hour)) || len(cands[0].Tokens) != 1 {
		t.Fatalf("stale candidates %+v %v", cands, err)
	}

	if err := ns.PushDelivered(ctx, hexToken(a)); err != nil {
		t.Fatalf("notify stamps a delivered push: %v", err)
	}
	var stamped bool
	if err := pool.QueryRow(ctx, `SELECT last_success_at IS NOT NULL FROM push_tokens WHERE token=$1`, hexToken(a)).Scan(&stamped); err != nil || !stamped {
		t.Fatalf("last_success_at: %v %v", stamped, err)
	}

	day := now.Format("2006-01-02")
	if ok, err := ns.ClaimNotification(ctx, a, day, model.NotifyMorningScore); !ok || err != nil {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := ns.ClaimNotification(ctx, a, day, model.NotifyMorningScore); ok {
		t.Fatal("the second claim of the day loses")
	}
	if err := ns.NotificationSent(ctx, a, day, model.NotifyMorningScore, 2); err != nil {
		t.Fatal(err)
	}
	if err := ns.ReleaseNotification(ctx, a, day, model.NotifyMorningScore); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ns.ClaimNotification(ctx, a, day, model.NotifyMorningScore); !ok {
		t.Fatal("a released claim can be taken again")
	}

	if _, err := ns.MorningScore(ctx, a, day); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no score yet: %v", err)
	}
	rec := 81
	if _, err := st.UpsertDailyScore(ctx, a, ScoreRow{DailyScore: model.DailyScore{Day: day, Recovery: &rec, State: model.StateBalanced}}); err != nil {
		t.Fatal(err)
	}
	if m, err := ns.MorningScore(ctx, a, day); err != nil || *m.Recovery != 81 || m.State != model.StateBalanced {
		t.Fatalf("morning %+v %v", m, err)
	}

	// A completed session with a record, read as notify.
	s := newSession(squat, lift(0, 5, 100, t0), lift(1, 5, 100, t0.Add(3*time.Minute)))
	if _, err := st.SaveSession(ctx, a, s); err != nil {
		t.Fatal(err)
	}
	end := t0.Add(time.Hour)
	if err := st.CompleteSession(ctx, a, s.ID, Completion{EndedAt: end, MuscularTRIMP: 30, Strain: 7.4, VolumeKG: 1000,
		WorkoutID: train.SessionWorkoutID(a, s.ID)}); err != nil {
		t.Fatal(err)
	}
	set := s.Exercises[0].Sets[1]
	if err := st.ReplacePRs(ctx, a, squat, []model.PR{{ExerciseID: squat, Kind: model.PRE1RM, Value: 116.7, SetID: set.ID,
		SessionID: s.ID, AchievedAt: *set.CompletedAt}}); err != nil {
		t.Fatal(err)
	}
	n, err := ns.SessionNotice(ctx, a, s.ID)
	if err != nil || n.Status != model.SessionCompleted || n.Strain == nil || *n.Strain < 7.3 || len(n.PRs) == 0 ||
		n.PRs[0].Kind != model.PRE1RM || n.PRs[0].ExerciseDE == "" {
		t.Fatalf("session notice %+v %v", n, err)
	}
}
