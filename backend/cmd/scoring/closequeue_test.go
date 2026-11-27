package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Withdrawn consent (D78, D298, the user's answer 2): scoring does not score an account without a current consent,
// and it closes that account's queue rows with a reason, so the sync status does not report scores pending for
// days that will never be scored. A failure to close is logged and never fails the event.

// fakeCloser is the queue's closing, recording what it was asked.
type fakeCloser struct {
	err   error
	users []uuid.UUID
	why   []string
}

func (f *fakeCloser) CloseQueue(_ context.Context, user uuid.UUID, reason string) (int64, error) {
	f.users, f.why = append(f.users, user), append(f.why, reason)
	return 1, f.err
}

// logLines parses a JSON log.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("%v: %s", err, line)
		}
		out = append(out, m)
	}
	return out
}

func TestCloseNoConsent(t *testing.T) {
	user := uuid.New()

	t.Run("closes the user's queue with the no_consent reason, logs nothing", func(t *testing.T) {
		var buf bytes.Buffer
		f := &fakeCloser{}
		closeNoConsent(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)), f, user)
		if len(f.users) != 1 || f.users[0] != user || f.why[0] != "no_consent" {
			t.Fatalf("closed %v for %v", f.users, f.why)
		}
		if buf.Len() != 0 {
			t.Fatalf("a quiet success logged %s", buf.String())
		}
	})

	t.Run("a failure is logged as an error, once, and not returned", func(t *testing.T) {
		var buf bytes.Buffer
		closeNoConsent(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)), &fakeCloser{err: errors.New("connection reset")}, user)
		lines := logLines(t, &buf)
		if len(lines) != 1 || lines[0]["level"] != "ERROR" || !strings.Contains(lines[0]["msg"].(string), "close the score queue") ||
			!strings.Contains(lines[0]["err"].(string), "connection reset") {
			t.Fatalf("log %s", buf.String())
		}
		// No health data, no identity beyond the request's own fields in the line.
		if strings.Contains(buf.String(), user.String()) {
			t.Fatalf("the log line names the account: %s", buf.String())
		}
	})

	t.Run("a cancelled pass is not an error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var buf bytes.Buffer
		closeNoConsent(ctx, slog.New(slog.NewJSONHandler(&buf, nil)), &fakeCloser{err: context.Canceled}, user)
		if buf.Len() != 0 {
			t.Fatalf("logged %s", buf.String())
		}
	})
}

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

func wantClosed(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, days ...string) {
	t.Helper()
	for _, d := range days {
		if r := queueRowOf(t, pool, user, d); r.scored == nil || r.reason == nil || *r.reason != "no_consent" {
			t.Fatalf("%s: scored_at %v, closed_reason %v, want it closed with no_consent", d, r.scored, r.reason)
		}
	}
}

func wantOpen(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, days ...string) {
	t.Helper()
	for _, d := range days {
		if r := queueRowOf(t, pool, user, d); r.scored != nil || r.reason != nil {
			t.Fatalf("%s: scored_at %v, closed_reason %v, want it untouched", d, r.scored, r.reason)
		}
	}
}

func newConsentingUser(t *testing.T, st *store.Store) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, id, "test", nil); err != nil {
		t.Fatal(err)
	}
	return id
}

// The event of an account that has withdrawn consent (the engine's consent mode on): nothing is scored, the event is
// acked (nil), and the account's pending queue rows are closed with no_consent; the same event again, or redelivered,
// changes nothing; another account's rows stay pending.
func TestHandleEventClosesTheQueueWithoutConsent(t *testing.T) {
	st, engine, user, pool := scoringStoreAndPool(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	other := newConsentingUser(t, st)
	d1, d2, d3 := "2026-11-24", "2026-11-25", "2026-11-26"
	if err := st.QueueDays(ctx, user, []string{d1, d2}); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueDays(ctx, other, []string{d1}); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeConsent(ctx, user); err != nil {
		t.Fatal(err)
	}

	ev := model.IngestedEvent{UserID: user, Days: []string{d1, d2}, Reason: model.ReasonSync, RequestID: "rid"}
	if err := handleEvent(ctx, quiet, engine, st, ev); err != nil {
		t.Fatalf("a withdrawn consent is acked, not retried: %v", err)
	}
	wantClosed(t, pool, user, d1, d2)
	wantOpen(t, pool, other, d1)
	if p, err := st.PendingDays(ctx, user); err != nil || len(p) != 0 {
		t.Fatalf("pending after the close: %v %v", p, err)
	}
	if rows, err := st.DailyScores(ctx, user, "2026-11-01", "2026-11-30"); err != nil || len(rows) != 0 {
		t.Fatalf("nothing is scored without consent: %+v %v", rows, err)
	}

	// Closing twice is a no-op: the same event, and a redelivery of it.
	before := queueRowOf(t, pool, user, d1)
	if err := handleEvent(ctx, quiet, engine, st, ev); err != nil {
		t.Fatal(err)
	}
	if err := handleEvent(events.WithRedelivery(ctx), quiet, engine, st, ev); err != nil {
		t.Fatal(err)
	}
	if after := queueRowOf(t, pool, user, d1); !after.scored.Equal(*before.scored) || *after.reason != "no_consent" {
		t.Fatalf("the second close moved a closed row: %+v, was %+v", after, before)
	}

	// A range event (recompute) takes the same path through ComputeRange.
	if err := st.QueueDays(ctx, user, []string{d3}); err != nil {
		t.Fatal(err)
	}
	rc := model.IngestedEvent{UserID: user, Days: []string{d1, d3}, Reason: "recompute", RequestID: "rid2"}
	if err := handleEvent(ctx, quiet, engine, st, rc); err != nil {
		t.Fatal(err)
	}
	wantClosed(t, pool, user, d3)
	wantOpen(t, pool, other, d1)
}

// Consent not enforced (off, warn): the account is scored as any other and nothing is closed for a reason.
func TestHandleEventWithoutConsentEnforcementClosesNothing(t *testing.T) {
	st, engine, user, pool := scoringStoreAndPool(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct {
		mode config.Mode
		day  string
	}{{config.ModeWarn, "2026-11-20"}, {config.ModeOff, "2026-11-21"}} {
		mode, day := c.mode, c.day
		t.Run(string(mode), func(t *testing.T) {
			engine.SetConsentMode(mode)
			if err := st.QueueDays(ctx, user, []string{day}); err != nil {
				t.Fatal(err)
			}
			if err := st.RevokeConsent(ctx, user); err != nil {
				t.Fatal(err)
			}
			if err := handleEvent(ctx, quiet, engine, st, model.IngestedEvent{UserID: user, Days: []string{day}, Reason: model.ReasonSync}); err != nil {
				t.Fatal(err)
			}
			var closed int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM score_queue WHERE closed_reason IS NOT NULL`).Scan(&closed); err != nil || closed != 0 {
				t.Fatalf("%d rows closed for a reason while consent is %s: %v", closed, mode, err)
			}
			// The day was scored for real, not closed: its row is done and a score exists.
			if r := queueRowOf(t, pool, user, day); r.scored == nil {
				t.Fatalf("%s: the day was not scored while consent is %s", day, mode)
			}
			if rows, err := st.DailyScores(ctx, user, day, day); err != nil || len(rows) != 1 {
				t.Fatalf("%s: scores %+v %v while consent is %s", day, rows, err, mode)
			}
		})
	}
}

// A queue that cannot be closed does not fail the event: it is acked (nil), the error is logged, and the rows stay
// pending for the next event or the timer pass to close.
func TestHandleEventAcksWhenTheCloseFails(t *testing.T) {
	st, engine, user, pool := scoringStoreAndPool(t)
	ctx := context.Background()
	day := "2026-11-26"
	if err := st.QueueDays(ctx, user, []string{day}); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeConsent(ctx, user); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	f := &fakeCloser{err: errors.New("queue unavailable")}
	err := handleEvent(ctx, slog.New(slog.NewJSONHandler(&buf, nil)), engine, f, model.IngestedEvent{UserID: user, Days: []string{day}, Reason: model.ReasonSync})
	if err != nil {
		t.Fatalf("a failed close must not fail (and so retry) the event: %v", err)
	}
	if len(f.users) != 1 || f.users[0] != user || f.why[0] != store.QueueClosedNoConsent {
		t.Fatalf("asked to close %v / %v", f.users, f.why)
	}
	if lines := logLines(t, &buf); len(lines) != 1 || lines[0]["level"] != "ERROR" {
		t.Fatalf("log %s", buf.String())
	}
	wantOpen(t, pool, user, day)
}

// The timer pass closes the rows of an active account that is not admitted, without scoring it: no day is computed
// and no baseline is learned.
func TestRefreshActiveClosesTheQueueOfAnAccountWithoutConsent(t *testing.T) {
	st, engine, user, pool := scoringStoreAndPool(t)
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	today, yesterday := time.Now().UTC().Format(time.DateOnly), time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	if err := st.TouchDevice(ctx, user, uuid.New(), "iPhone", nil, nil); err != nil { // a device that synced: active
		t.Fatal(err)
	}
	if err := st.QueueDays(ctx, user, []string{yesterday, today}); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeConsent(ctx, user); err != nil {
		t.Fatal(err)
	}

	refreshActive(ctx, quiet, st, engine, &learner{tried: map[uuid.UUID]string{}}, &catchUp{failed: map[uuid.UUID]string{}})

	wantClosed(t, pool, user, yesterday, today)
	if rows, err := st.DailyScores(ctx, user, yesterday, today); err != nil || len(rows) != 0 {
		t.Fatalf("nothing is scored without consent: %+v %v", rows, err)
	}
}
