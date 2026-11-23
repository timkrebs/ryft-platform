package eventbus

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/pubsub"
)

// Tick is one step of a service's tick (pubsub.Tick), so a service builds its ticks
// without importing the transport.
type Tick = pubsub.Tick

// Transport is a service's Pub/Sub, built from its configuration in one call. It is
// the events.Bus the outbox relays into (publish by key), and it builds the push routes,
// the tick handler and notify's wait sender, all checked by the one push verifier.
type Transport struct {
	*pubsub.Bus
	verifier auth.PushVerifier
	service  string
	log      *slog.Logger
}

// Open builds service's transport from cfg (config.Load has validated it): the REST
// client (the metadata server's token, or the emulator without one) and the push
// verifier (Google OIDC for cfg.Push, or auth.InsecurePush under ALLOW_INSECURE_PUSH,
// which warns). The verifier's JWKS refresher runs until ctx ends, so ctx is the
// service's life, not a start-up timeout. Its one network call is the first JWKS fetch,
// which Open waits for (at most 10 s); its failure does not fail Open (pushes answer 503
// until keys arrive). Pub/Sub's client dials nothing until it publishes.
func Open(ctx context.Context, cfg config.Config, service string, log *slog.Logger) (*Transport, error) {
	client, err := pubsub.NewClient(pubsub.Options{
		Project: cfg.PubSub.Project, Endpoint: cfg.PubSub.Endpoint, EmulatorHost: cfg.PubSub.EmulatorHost,
	})
	if err != nil {
		return nil, err
	}
	var verifier auth.PushVerifier
	pushAuth := "google_oidc"
	if cfg.Push.AllowInsecure {
		verifier, pushAuth = auth.InsecurePush(log), "insecure"
	} else if verifier, err = auth.NewGoogleOIDC(ctx, auth.GoogleOIDCOptions{
		Audience: cfg.Push.Audience, Email: cfg.Push.SAEmail, Subject: cfg.Push.SAID,
	}); err != nil {
		return nil, err
	}
	endpoint := cfg.PubSub.Endpoint
	if cfg.PubSub.EmulatorHost != "" {
		endpoint = "http://" + cfg.PubSub.EmulatorHost + " (emulator)"
	}
	log.Info("events", "transport", "pubsub", "project", cfg.PubSub.Project, "endpoint", endpoint, "push_auth", pushAuth)
	return &Transport{Bus: pubsub.NewBus(client), verifier: verifier, service: service, log: log}, nil
}

// Verifier is the check every /push/* and /internal/* route runs.
func (t *Transport) Verifier() auth.PushVerifier { return t.verifier }

// Push is consumer's push routes with fn as its handler (events.ScoringHandler,
// events.NotifyHandler): register them with Register(mux). Bad events are dead-lettered
// with this transport's client. deferTo stores notify's deferrals (notify's outbox,
// whose SendAt writes the not-before row); nil makes a Defer a nack.
func (t *Transport) Push(consumer string, fn events.Handler, deferTo pubsub.Deferrer) (*pubsub.Push, error) {
	return pubsub.NewPush(pubsub.PushOptions{
		Consumer: consumer, Verifier: t.verifier, DLQ: t.Client(), Deferrer: deferTo, Log: t.log,
	}, fn)
}

// Tick is the service's POST /internal/tick handler (pubsub.PathTick): ticks in order
// within budget, then finally (the metrics push; nil for none) once the tick is counted.
func (t *Transport) Tick(budget time.Duration, finally func(context.Context) error, ticks ...Tick) (http.Handler, error) {
	return pubsub.NewTick(pubsub.TickOptions{
		Service: t.service, Verifier: t.verifier, Budget: budget, Log: t.log, Finally: finally,
	}, ticks...)
}

// WaitSender is notify's outbox transport: it publishes every row to ryft-notify-wait
// with its own key as ryft-key, and /push/wait hands it back to notify under that key.
func (t *Transport) WaitSender() (events.Sender, error) {
	s, err := t.Client().To(pubsub.TopicNotifyWait)
	if err != nil {
		return nil, fmt.Errorf("eventbus: %w", err)
	}
	return s, nil
}

// EdgePushPaths are the push routes api and ingest serve besides the tick (ingest's /push/inbox.received, D257): the
// edge's per-address limit never counts them, as it never counts the tick (Google's push servers share a few
// addresses, and the push token is checked behind it).
func EdgePushPaths() []string {
	return append(pubsub.PushPaths(pubsub.ServiceAPI), pubsub.PushPaths(pubsub.ServiceIngest)...)
}

// PathTick is the tick's route, for the mux.
const PathTick = pubsub.PathTick
