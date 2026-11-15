package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// A bad setting ends the process before anything connects, under the message it
// always had: the database is a closed port, so a check behind db.Open would be
// outlasted by the start-up retries. The child gets only this environment: compose's
// local Pub/Sub settings (an emulator that is not there, as nothing is published).
func TestMainRefusesBadSettingsBeforeConnecting(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainRefusesBadSettingsBeforeConnecting$")
	cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://scoring@127.0.0.1:1/ryft", "CONSENT_ENFORCE=maybe",
		"PUBSUB_EMULATOR_HOST=127.0.0.1:1", "ALLOW_INSECURE_PUSH=true"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"msg":"beta config"`) ||
		!strings.Contains(string(out), `"service":"scoring"`) {
		t.Fatalf("want exit 1 with the beta config error, got %v:\n%s", err, out)
	}
}

// transport is a real transport with compose's local settings: it dials nothing, and
// its push routes check no token.
func transport(t *testing.T, log *slog.Logger) *eventbus.Transport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tr, err := eventbus.Open(ctx, config.Config{
		PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: "127.0.0.1:1"},
		Push:   config.Push{AllowInsecure: true},
	}, "scoring", log)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// build hands app scoring's push routes (its two keys, nothing else) and one tick
// step, the scoring pass; no /v1 routes and no checks (app adds the db's).
func TestBuildPartsArePushRoutesAndTheTickStep(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	parts, err := build(app.Deps{Log: log, Outbox: outbox.New(nil, "scoring", nil, log), Bus: transport(t, log)},
		config.Beta{Consent: config.ModeOn})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts.Push) != 1 || len(parts.Ticks) != 1 || parts.Ticks[0].Name != "scoring pass" {
		t.Fatalf("push %d, ticks %+v: want one push handling and the scoring pass", len(parts.Push), parts.Ticks)
	}
	if parts.Routes != nil || parts.Checks != nil {
		t.Fatal("scoring serves no /v1, and app adds the db check")
	}
	mux := http.NewServeMux()
	parts.Push[0].Register(mux)
	for path, want := range map[string]int{
		"/push/ingested": http.StatusBadRequest, "/push/session.completed": http.StatusBadRequest, // routed: not an envelope
		"/push/scored": http.StatusNotFound, "/push/wait": http.StatusNotFound, "/internal/tick": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("not json")))
		if rec.Code != want {
			t.Errorf("POST %s = %d, want %d", path, rec.Code, want)
		}
	}
	if !strings.Contains(logs.String(), `"msg":"consuming ingest events"`) || !strings.Contains(logs.String(), `"/push/session.completed"`) {
		t.Fatalf("runbooks grep for the consuming line: %s", logs.String())
	}
}

// The Spec decides how app wires the service: the events' outbox (swept by the tick) and
// the 240 s budget the pass needs (D219).
func TestSpecFlags(t *testing.T) {
	s := spec()
	if s.Name != "scoring" || s.Outbox != app.EventOutbox || s.TickBudget != 240*time.Second || s.Edge {
		t.Fatalf("Name %q, Outbox %v, TickBudget %v, Edge %v: want scoring, the event outbox, 240 s, false", s.Name, s.Outbox, s.TickBudget, s.Edge)
	}
	if s.Load == nil || s.Build == nil {
		t.Fatal("scoring needs its Load and its Build")
	}
}

// Load reads the consent mode (no policy version is needed, D78) and logs it under
// the line OPERATIONS greps for; a bad value is a ConfigError under "beta config".
func TestSpecLoadReadsTheConsentMode(t *testing.T) {
	t.Setenv("CONSENT_ENFORCE", "warn")
	var logs bytes.Buffer
	if err := spec().Load(slog.New(slog.NewJSONHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"beta"`) || !strings.Contains(logs.String(), `"consent":"warn"`) {
		t.Fatalf("beta line: %s", logs.String())
	}
	t.Setenv("CONSENT_ENFORCE", "maybe")
	var ce app.ConfigError
	if err := spec().Load(slog.New(slog.NewJSONHandler(&logs, nil))); !errors.As(err, &ce) || ce.Msg != "beta config" {
		t.Fatalf("Load = %v, want a ConfigError under \"beta config\"", err)
	}
}

// A Deps without the outbox would panic on the first publish (every push and the pass's
// OnScored), one without the transport has no push routes: build refuses both.
func TestBuildNeedsTheOutboxAndTheTransport(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	for want, d := range map[string]app.Deps{
		"outbox":    {Log: log, Bus: transport(t, log)},
		"transport": {Log: log, Outbox: outbox.New(nil, "scoring", nil, log)},
	} {
		if _, err := build(d, config.Beta{Consent: config.ModeOn}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("build without the %s = %v", want, err)
		}
	}
}

// sender records what the handler publishes; with hold set, every Send waits for it to
// close, as an inline flush waits for Pub/Sub.
type sender struct {
	mu   sync.Mutex
	sent []string
	hold chan struct{}
}

func (s *sender) Send(_ context.Context, m events.Message) error {
	if s.hold != nil {
		<-s.hold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m.Key)
	return nil
}

func (s *sender) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// push posts one envelope of key with ev as its data and returns the answer's channel.
func push(t *testing.T, srv *httptest.Server, key string, ev any) <-chan int {
	t.Helper()
	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"message":{"data":%q,"attributes":{"ryft-key":%q},"messageId":"17"},"subscription":"projects/ryft-local/subscriptions/scoring-%s","deliveryAttempt":1}`,
		base64.StdEncoding.EncodeToString(data), key, key)
	answer := make(chan int, 1)
	go func() {
		resp, err := srv.Client().Post(srv.URL+"/push/"+key, "application/json", strings.NewReader(body))
		if err != nil {
			answer <- 0
			return
		}
		resp.Body.Close()
		answer <- resp.StatusCode
	}()
	return answer
}

// A push answers only after what the event tells notify has been sent: Send is the
// outbox's (insert, then the inline flush, D219), so a push that answered first would
// leave its events to run with no CPU after the answer. The answer is the handler's.
func TestPushAnswersAfterItsPublishes(t *testing.T) {
	user, session := uuid.New(), uuid.New()
	for _, tc := range []struct {
		name  string
		key   string
		ev    any
		score error
		code  int
		sent  []string
	}{
		{"a sync: refresh", events.KeyIngested,
			model.IngestedEvent{UserID: user, Days: []string{"2026-11-14", "2026-11-15"}, Reason: model.ReasonSync}, nil,
			http.StatusNoContent, []string{events.KeyRefresh}},
		{"a completed session: session.scored", events.KeySessionCompleted,
			model.SessionCompletedEvent{UserID: user, SessionID: session, Day: "2026-11-15"}, nil,
			http.StatusNoContent, []string{events.KeySessionScored}},
		{"scoring failed: nothing told, Pub/Sub delivers it again", events.KeyIngested,
			model.IngestedEvent{UserID: user, Days: []string{"2026-11-15"}, Reason: model.ReasonSync}, errors.New("pooler unreachable"),
			http.StatusInternalServerError, nil},
		{"the user is gone: dropped", events.KeyIngested,
			model.IngestedEvent{UserID: user, Days: []string{"2026-11-15"}, Reason: model.ReasonSync}, store.ErrNotFound,
			http.StatusNoContent, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			out := &sender{hold: make(chan struct{})}
			handle := ingestHandler(log, func(context.Context, model.IngestedEvent) error { return tc.score }, events.Publisher{Sender: out})
			p, err := transport(t, log).Push(events.ConsumerScoring, events.ScoringHandler(handle), nil)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			p.Register(mux)
			srv := httptest.NewServer(mux)
			defer srv.Close()

			answer := push(t, srv, tc.key, tc.ev)
			if tc.sent != nil {
				select {
				case code := <-answer:
					t.Fatalf("the push answered %d while its publish was still being sent", code)
				case <-time.After(100 * time.Millisecond):
				}
			}
			close(out.hold)
			select {
			case code := <-answer:
				if code != tc.code {
					t.Errorf("answer %d, want %d", code, tc.code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no answer")
			}
			if got := out.keys(); !slices.Equal(got, tc.sent) {
				t.Errorf("sent %v, want %v", got, tc.sent)
			}
		})
	}
}

// OnScored's publish is the morning push's: it carries the request id of the event
// being scored, and a failed write is logged, never the push's failure.
func TestOnScoredPublishesWithTheRequestID(t *testing.T) {
	out := &sender{}
	var logs bytes.Buffer
	fn := onScored(slog.New(slog.NewJSONHandler(&logs, nil)), events.Publisher{Sender: out})
	ctx := context.Background()
	fn(ctx, model.ScoredEvent{UserID: uuid.New(), Day: "2026-11-15"})
	if got := out.keys(); !slices.Equal(got, []string{events.KeyScored}) {
		t.Fatalf("sent %v", got)
	}
	failing := onScored(slog.New(slog.NewJSONHandler(&logs, nil)), events.Publisher{Sender: failSender{}})
	failing(ctx, model.ScoredEvent{UserID: uuid.New(), Day: "2026-11-15"})
	if !strings.Contains(logs.String(), `"msg":"publish scored"`) {
		t.Errorf("a failed publish is logged: %s", logs.String())
	}
}

type failSender struct{}

func (failSender) Send(context.Context, events.Message) error { return errors.New("insert failed") }

// The pass as a tick step fails only when the budget ended it: a user's error is the
// pass's to log, so a pass that ran through is ok whatever it logged.
func TestPassStepFailsOnlyOutOfBudget(t *testing.T) {
	ran := 0
	step := passStep(func(context.Context) { ran++ })
	if err := step(context.Background()); err != nil || ran != 1 {
		t.Fatalf("a pass that ran through = %v (ran %d)", err, ran)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if err := step(ctx); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("a pass the budget ended = %v", err)
	}
}

// The pass keeps one learner and one catch-up for all its ticks (the Sunday learner
// tries a user once a day, the catch-up rotates), refreshes first and does its
// housekeeping only while the budget lasts.
func TestTimerPassKeepsItsMemoryBetweenPasses(t *testing.T) {
	learn, behind := &learner{tried: map[uuid.UUID]string{}}, &catchUp{failed: map[uuid.UUID]string{}}
	var order []string
	var learns []*learner
	var behinds []*catchUp
	pass := timerPass(learn, behind, func(_ context.Context, l *learner, b *catchUp) {
		order = append(order, "refresh")
		learns, behinds = append(learns, l), append(behinds, b)
	}, func(context.Context) { order = append(order, "after") })

	pass(context.Background())
	pass(context.Background())
	if len(learns) != 2 || learns[0] != learn || learns[1] != learn || behinds[0] != behind || behinds[1] != behind {
		t.Fatal("every pass is handed the pass's own learner and catch-up")
	}
	if want := []string{"refresh", "after", "refresh", "after"}; !slices.Equal(order, want) {
		t.Fatalf("order %v, want %v", order, want)
	}

	order = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pass(ctx)
	if want := []string{"refresh"}; !slices.Equal(order, want) {
		t.Fatalf("out of budget: %v, want only the refresh (it stops itself)", order)
	}
}

// The pass over the active users stops between two users when the budget ends,
// instead of logging one cancelled query per user left.
func TestEachUserStopsWhenTheContextEnds(t *testing.T) {
	users := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var seen []int
	eachUser(ctx, users, func(_ context.Context, i int, id uuid.UUID) {
		if id != users[i] {
			t.Errorf("user %d is %v", i, id)
		}
		seen = append(seen, i)
		if i == 1 {
			cancel()
		}
	})
	if len(seen) != 2 {
		t.Fatalf("users %v, want 0 and 1 then stop", seen)
	}
	eachUser(ctx, users, func(context.Context, int, uuid.UUID) { t.Fatal("no user once the context has ended") })

	var all int
	eachUser(context.Background(), users, func(context.Context, int, uuid.UUID) { all++ })
	if all != len(users) {
		t.Fatalf("a live context visits every user, got %d", all)
	}
}
