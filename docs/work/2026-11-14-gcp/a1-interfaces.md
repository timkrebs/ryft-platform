# A1-2 handover: the Pub/Sub transport's API (2026-11-15)

For A1-4 (wiring `internal/app` and `cmd/{api,ingest,scoring,notify}`), A1-6 (emulator tests) and A1-7 (docs). Built to
`plan-a1.md` → Interfaces. Everything below compiles today and has tests; the services do not build until A1-4 wires
it (see "What breaks" at the end).

## `internal/events` (transport-neutral)

Unchanged: `Sender`, `Message{Key, ID, Body, Reason}`, `Publisher`, the encoders (`Ingested`, `SessionCompleted`, `Scored`,
`SessionScored`, `Refresh`), `DecodeIngested`, `DecodeNotify`, `NotifyMessage`, `CountPublished`, `Permanent`/`IsPermanent`,
`Defer`/`Deferred`, `Guarded`, `WithRedelivery`/`Redelivered`, the `Key*`, `Consumer*`, `ScoringKeys`, `NotifyKeys` constants.

New or changed:

```go
// Bus has no Ready and no Close any more: REST publish holds no connection.
type Bus interface {
	Sender
	PublishIngested(ctx context.Context, ev model.IngestedEvent, dedupID string) error
	PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, dedupID string) error
	PublishScored(ctx context.Context, ev model.ScoredEvent) error
	PublishSessionScored(ctx context.Context, ev model.SessionScoredEvent) error
	PublishRefresh(ctx context.Context, ev model.RefreshEvent) error
}

type Handler func(ctx context.Context, key string, data []byte) error // one pushed event
func ScoringHandler(fn func(context.Context, model.IngestedEvent) error) Handler // decode failure → Bad
func NotifyHandler(fn func(context.Context, NotifyMessage) error) Handler       // decode failure → Bad

func Bad(err error) error   // the event can never be handled: the consumer dead-letters it (not a retry)
func IsBad(err error) bool

const ReasonUnknown = "unknown"
func WithReason(ctx context.Context) (context.Context, func() string) // the transport's reason slot
func SetReason(ctx context.Context, reason string)                    // the typed handlers set it after decoding
```

Removed: `HeaderSubject`, `HeaderConsumer`, `HeaderDeliveries`, `HeaderError`, and the `Consume*` methods.

What a handler returns decides the answer: `nil` → 204 (ok); `Permanent(err)` → 204 (dropped); `Bad(err)` → dead letter on
the consumer's own DLQ topic, then 204; `Defer(d)` → the deferral sink, then 204 (no sink, or its write fails → 500); any other
error or a panic → 500 (Pub/Sub redelivers; on attempt 20 it is counted `failed` and Pub/Sub dead-letters it).

## `internal/events/pubsub` (stdlib only; imported by `eventbus` and `cmd/pubsub-topology` only, arch rule R5)

```go
// Names and attributes
const TopicIngested, TopicSessionCompleted, TopicScored, TopicSessionScored, TopicRefresh,
	TopicNotifyWait, TopicTick, TopicDLQScoring, TopicDLQNotify string // "ryft-…"
const ServiceAPI, ServiceIngest, ServiceScoring, ServiceNotify string
const PathTick = "/internal/tick"; const PathWait = "/push/wait"; const KeyTick = "tick"
const AttrKey = "ryft-key"; AttrMessageID = "ryft-message-id"; AttrReason = "ryft-reason"
const AttrReplayed = "ryft-replayed"; AttrError = "ryft-error"; const MaxErrorBytes = 512
const RetryMinimumBackoff = 10 * time.Second; RetryMaximumBackoff = 600 * time.Second
const MaxDeliveryAttempts = 20; HoldAckDeadline = 60 * time.Second
const MaxPushBody = 1 << 20
const MetadataTokenURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
const TFVarsFile = "infra/gcp/pubsub.auto.tfvars.json"; EmulatorInitFile = "deploy/pubsub-emulator/init.sh"
var TickServices []string
var ErrNoTopic error

// Topology (the generator's input)
type Topic struct{ Name string; Publishers []string; Key string; Ordered bool }
type PushSubscription struct{ Name, Topic, Service, Path string; Ordered bool; AckDeadline, Retention time.Duration; DeadLetterTopic string }
type HoldSubscription struct{ Name, Topic string; Retention time.Duration }
type Topology struct{ Topics []Topic; Push []PushSubscription; Hold []HoldSubscription }
func Spec() Topology
func (t Topology) Validate() error
func TFVars(t Topology) ([]byte, error)
func EmulatorInit(t Topology) []byte
func TopicFor(key string) (string, bool)
func PushPath(key string) string           // "/push/" + key
func KeyOfPath(path string) (string, bool)
func PushPaths(service string) []string
func Ordered(topic string) bool
func DLQTopic(consumer string) (string, bool)

// Publishing
type Options struct{ Project, Endpoint, EmulatorHost, TokenURL string; HTTPClient *http.Client; Timeout time.Duration }
type Client struct{ /* unexported */ }
func NewClient(o Options) (*Client, error)
type Message struct{ Data []byte; Attributes map[string]string; OrderingKey string }
func (c *Client) Publish(ctx context.Context, topic string, m Message) (string, error) // a 401 refetches the token once
func (c *Client) To(topic string) (events.Sender, error)  // every message to one topic, ryft-key = its own key
type Bus struct{ events.Publisher /* + unexported */ }    // events.Bus
func NewBus(c *Client) *Bus
func (b *Bus) Send(ctx context.Context, m events.Message) error // by key; an unknown key → ErrNoTopic (counted)
func (b *Bus) Client() *Client

// Push
type Deferrer interface{ SendAt(ctx context.Context, m events.Message, at time.Time) error }
type Publisher interface{ Publish(ctx context.Context, topic string, m Message) (string, error) }
type PushOptions struct{ Consumer string; Verifier auth.PushVerifier; DLQ Publisher; Deferrer Deferrer; Log *slog.Logger; Now func() time.Time }
type Push struct{ /* unexported */ }
func NewPush(o PushOptions, fn events.Handler) (*Push, error)
func (p *Push) Paths() []string
func (p *Push) Register(mux *http.ServeMux) // "POST <path>" for every path
func (p *Push) Handler(path string) http.Handler

// Tick
type Tick struct{ Name string; Run func(ctx context.Context) error }
type TickOptions struct{ Service string; Verifier auth.PushVerifier; Budget time.Duration; Log *slog.Logger; Finally func(ctx context.Context) error; FinallyBudget time.Duration }
func NewTick(o TickOptions, ticks ...Tick) (http.Handler, error)
```

## `internal/auth` (the Google part)

```go
const GoogleJWKSURL = "https://www.googleapis.com/oauth2/v3/certs"
type PushVerifier interface{ VerifyPush(r *http.Request) error } // *auth.Error: 401, or 503 (no keys yet)
type GoogleOIDCOptions struct {
	Audience, Email, Subject, JWKSURL string // PUSH_AUDIENCE, PUSH_SA_EMAIL, PUSH_SA_ID; JWKSURL default GoogleJWKSURL
	RefreshInterval, UnknownKIDEvery, Leeway time.Duration // 1 h, 1 min, 30 s
	Client *http.Client
}
type GoogleOIDC struct{ /* unexported */ }
func NewGoogleOIDC(ctx context.Context, o GoogleOIDCOptions) (*GoogleOIDC, error) // the JWKS refresher lives until ctx ends
func (g *GoogleOIDC) VerifyPush(r *http.Request) error
func (g *GoogleOIDC) Verify(ctx context.Context, token string) error
func (g *GoogleOIDC) Ready(ctx context.Context) error
func InsecurePush(log *slog.Logger) PushVerifier // ALLOW_INSECURE_PUSH only; warns once when built
```

Failures count in `ryft_auth_failures_total` with the reasons the Supabase verifier already uses, no new ones:
`missing_token`, `malformed`, `invalid`, `signature`, `expired`, `unknown_key`, `key_mismatch`, `issuer`, `audience`,
`subject` (401) and `jwks_unavailable`, `jwks_refresh_limited` (503). If there are no keys the answer is 503
`jwks_unavailable`, so `AuthUnavailable` also fires for Google's JWKS.

## `internal/config` (the Pub/Sub part)

```go
const DefaultPubSubEndpoint = "https://europe-west1-pubsub.googleapis.com"
const EmulatorProject = "ryft-local" // GCP_PROJECT's default with the emulator (init.sh uses the same)
type PubSub struct{ Project, Endpoint, EmulatorHost string } // GCP_PROJECT, PUBSUB_ENDPOINT, PUBSUB_EMULATOR_HOST
type Push struct{ Audience, SAEmail, SAID string; AllowInsecure bool } // PUSH_AUDIENCE, PUSH_SA_EMAIL, PUSH_SA_ID, ALLOW_INSECURE_PUSH
// Config gains PubSub PubSub and Push Push; AMQP and the EVENTS_* switches are gone.
```

Load refuses: `ALLOW_INSECURE_PUSH=true` or `PUBSUB_EMULATOR_HOST` with `K_SERVICE` or `CLOUD_RUN_JOB` set; without the
insecure flag a missing or malformed `PUSH_AUDIENCE` (https origin, no path), `PUSH_SA_EMAIL` or `PUSH_SA_ID`; no `GCP_PROJECT`
without the emulator; an emulator host that is not host:port. **Every `config.Load` caller needs this env now**, including the
subcommands (`scoring rescore`, `ingest fold-workouts`); locally `PUBSUB_EMULATOR_HOST=…` and `ALLOW_INSECURE_PUSH=true`.

## `internal/eventbus`

```go
type Tick = pubsub.Tick
const PathTick = pubsub.PathTick
type Transport struct{ *pubsub.Bus /* + verifier, service, log */ } // an events.Bus
func Open(ctx context.Context, cfg config.Config, service string, log *slog.Logger) (*Transport, error)
func (t *Transport) Verifier() auth.PushVerifier
func (t *Transport) Push(consumer string, fn events.Handler, deferTo pubsub.Deferrer) (*pubsub.Push, error)
func (t *Transport) Tick(budget time.Duration, finally func(context.Context) error, ticks ...Tick) (http.Handler, error)
func (t *Transport) WaitSender() (events.Sender, error) // ryft-notify-wait
```

`Open`'s `ctx` is the service's life (the JWKS refresher), not a start-up timeout. It dials nothing; a JWKS that cannot be
fetched does not fail `Open` (pushes and ticks answer 503 until keys arrive).

## Wiring (A1-4)

Every service:

```go
tr, err := eventbus.Open(ctx, cfg, svc, log)
ob := outbox.New(pool, svc, tr, log) // relays by key into Pub/Sub; tr is the events.Sender
tick, err := tr.Tick(budget, nil /* A2: the metrics push */,
	eventbus.Tick{Name: "db ping", Run: ping},      // sets ryft_db_up / ryft_db_ping_failures_total
	eventbus.Tick{Name: "outbox sweep", Run: ob.Sweep},
	/* the service's own steps */)
mux.Handle("POST "+eventbus.PathTick, tick)
```

Budgets: api, ingest, notify 45 s; scoring 240 s. A step must return when its ctx ends (scoring's pass stops between users).
The tick counts `ryft_ticks_total{service,result}` before `Finally` runs, and answers 204 last.

scoring:

```go
push, err := tr.Push(events.ConsumerScoring, events.ScoringHandler(handle), nil) // no deferrals: a Defer is a nack
push.Register(mux) // POST /push/ingested, POST /push/session.completed
```

notify:

```go
wait, err := tr.WaitSender()
waitBox := outbox.New(pool, events.ConsumerNotify, wait, log) // publisher "notify": its rows go to ryft-notify-wait only
push, err := tr.Push(events.ConsumerNotify, events.NotifyHandler(handle), waitBox) // *outbox.Outbox is a pubsub.Deferrer
push.Register(mux) // /push/scored, /push/session.scored, /push/refresh, /push/wait
// tick: db ping, waitBox.Sweep (the due wait rows), the stale check
```

A `Defer(d)` becomes `waitBox.SendAt(ctx, events.Message{Key, ID: ryft-message-id, Body, Reason}, now+d)`; the notify tick's
`Sweep` publishes the due rows to `ryft-notify-wait` with `ryft-key` = the original key, and `/push/wait` hands them to
`handle` again under that key. A message deferred twice carries the same `ID`; the outbox's insert must accept that.

api and ingest: no push routes, only the tick and the outbox.

The mux: `/push/*` and `/internal/tick` sit outside the `/v1` auth middleware and the per-user limit (the push verifier is
their auth; the handlers bound the body at 1 MiB and log neither `Authorization` nor the body). The "broker" readiness check
and the bus close at shutdown go: there is nothing to check or close.

## What breaks until A1-4 (and the lead's files)

- `internal/app`: `Spec`'s bus opener is typed `func(...) (events.Bus, error)` (now `*eventbus.Transport`), and it calls
  `bus.Ready` and `bus.Close`.
- `cmd/api`, `cmd/ingest`: `bus.Close`; `h.outbox.Kick` (A1-3 replaced it with `Flush`).
- `cmd/scoring`, `cmd/notify`: `d.Bus.ConsumeIngested` / `ConsumeNotify` (and they import `internal/app`).
- Not owned by A1-2, still naming the old transport: `Makefile:349` (`k8s-validate` runs `go run ./cmd/rabbitmq-topology
  -check`; it should run `./cmd/pubsub-topology -check`), `.github/workflows/ci.yml` (paths `amqpbus/**`,
  `cmd/rabbitmq-topology/**`), `scripts/pkgmap.sh:118`, `backend/internal/metrics/doc.go:14` (`UseAMQP`), `internal/app/doc.go`,
  `deploy/rabbitmq/topology/topology.yaml` (the deleted generator's output).
- `deploy/pubsub-emulator/init.sh` sends `messageRetentionDuration` and `retryPolicy`; whether the emulator accepts both is
  for A1-6's emulator test to show.
