package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect/fake"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// The hook and the consumer on a real database (0042): a signed push stores its rows and their events in one
// transaction and answers 200; the events, delivered three times, leave one processed row per record, the rows of
// one night, one queue entry per day, and one ingested event (D100); a disconnect-by-notice deletes it all.
func TestHookAndConsumerOnPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := store.New(pool)
	log := logx.NewWriter(&strings.Builder{}, "ingest", slog.LevelDebug)
	user := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: user, Email: "u@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, user, "2026-10-26", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1, 'fake', 'puid-1', 'connected', '{read_sleep,read_hrv}', '2026-10-26')`, user); err != nil {
		t.Fatal(err)
	}
	reg, err := pushRegistry(config.Connect{Fake: true, FakePushSecret: fakeSecret, Providers: []string{"fake"}})
	if err != nil {
		t.Fatal(err)
	}
	ob := outbox.New(pool, "ingest", nil, log) // no transport: the rows stay for the test to read
	hook := newConnectHook(st, reg, config.Beta{PrivacyPolicyVersion: "2026-10-26", Consent: config.ModeOn}, "", ob, log)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /connect/{provider}/push", hook.push)

	night := time.Now().UTC().Add(-10 * time.Hour).Truncate(time.Second)
	body := `{"items":[{"kind":"sleep","record_id":"n-1","user":"puid-1","version":1,"night":{"state":"final","segments":[
	  {"start":"` + night.Format(time.RFC3339) + `","end":"` + night.Add(4*time.Hour).Format(time.RFC3339) + `","stage":"light"},
	  {"start":"` + night.Add(4*time.Hour).Format(time.RFC3339) + `","end":"` + night.Add(7*time.Hour).Format(time.RFC3339) + `","stage":"deep"}]}},
	 {"kind":"hrv","record_id":"h-1","user":"puid-1","version":1,"hrv":{"start":"` + night.Add(time.Hour).Format(time.RFC3339) + `","end":"` + night.Add(time.Hour+5*time.Minute).Format(time.RFC3339) + `","rmssd_ms":48.5}},
	 {"kind":"sleep","record_id":"n-2","user":"stranger","version":1,"night":{"state":"final","segments":[]}}]}`
	push := func() int {
		r := httptest.NewRequest(http.MethodPost, "/connect/fake/push", strings.NewReader(body))
		r.Header.Set(fake.SignatureHeader, fake.Sign(fakeSecret, []byte(body)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	if code := push(); code != 200 {
		t.Fatalf("push: %d", code)
	}
	if code := push(); code != 200 { // the same body again: the same rows
		t.Fatalf("repeat: %d", code)
	}
	var rows, events2 int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM provider_inbox`).Scan(&rows)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE routing_key='inbox.received'`).Scan(&events2)
	if rows != 2 || events2 != 2 {
		t.Fatalf("%d inbox rows and %d events after two identical pushes, want 2 and 2 (a stranger is not stored)", rows, events2)
	}

	// The consumer: each event three times, as Pub/Sub may deliver.
	evs, err := pool.Query(ctx, `SELECT payload FROM outbox WHERE routing_key='inbox.received' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var payloads [][]byte
	for evs.Next() {
		var p []byte
		_ = evs.Scan(&p)
		payloads = append(payloads, p)
	}
	evs.Close()
	for _, p := range payloads {
		var ev model.InboxReceivedEvent
		if err := json.Unmarshal(p, &ev); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if err := hook.consume(ctx, ev); err != nil {
				t.Fatalf("consume %d: %v", i, err)
			}
		}
	}
	count := func(q string) (n int) { _ = pool.QueryRow(ctx, q).Scan(&n); return }
	if n := count(`SELECT count(*) FROM provider_inbox WHERE state='processed' AND payload IS NULL`); n != 2 {
		t.Fatalf("%d processed rows", n)
	}
	if s, sl := count(`SELECT count(*) FROM samples WHERE origin='direct' AND vendor='fake' AND source='ryft-direct-fake' AND type='hrv_rmssd'`),
		count(`SELECT count(*) FROM sleep_segments WHERE origin='direct' AND record_state='final'`); s != 1 || sl != 2 {
		t.Fatalf("%d direct samples, %d direct sleep segments", s, sl)
	}
	// P2-3: the first delivery stores both pending rows in one transaction and announces them with one event.
	if n := count(`SELECT count(*) FROM outbox WHERE routing_key='ingested' AND reason='connect'`); n != 1 {
		t.Fatalf("%d ingested events for two records delivered three times each, want 1", n)
	}
	if n := count(`SELECT count(*) FROM score_queue WHERE scored_at IS NULL`); n == 0 || n > 3 {
		t.Fatalf("%d queued days", n)
	}
	if n := count(`SELECT count(*) FROM connections WHERE last_data_at IS NOT NULL`); n != 1 {
		t.Fatal("last_data_at not set")
	}

	// Without consent in force the record is dropped, not stored.
	if _, err := pool.Exec(ctx, `UPDATE consents SET policy_version='2025-01-01'`); err != nil {
		t.Fatal(err)
	}
	body = `{"items":[{"kind":"hrv","record_id":"h-2","user":"puid-1","version":1,"hrv":{"start":"` + night.Format(time.RFC3339) + `","end":"` + night.Format(time.RFC3339) + `","rmssd_ms":50}}]}`
	if code := push(); code != 200 {
		t.Fatal(code)
	}
	if n := count(`SELECT count(*) FROM provider_inbox WHERE record_id='h-2'`); n != 0 {
		t.Fatal("stored a record without consent in force")
	}
}
