package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/connect/fake"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Direct connections on ingest (D257, design 7): the hook a provider pushes to, the inbox it fills, the consumer of
// the events that announce its rows, and the tick's upkeep. Ingest holds the push half of each provider only
// (connect.PushRegistry): no token is reachable from here, and processing a push needs none.

// connectStore is what the hook, the consumer and the tick need of the store (*store.Store; a fake in tests).
type connectStore interface {
	StoreInbox(ctx context.Context, provider, policyVersion string, items []store.HookItem, emit func(pgx.Tx, model.InboxReceivedEvent) error) ([]store.HookOutcome, error)
	InboxRow(ctx context.Context, id int64) (store.InboxItem, error)
	PendingDirect(ctx context.Context, user uuid.UUID, provider string, except int64, limit int) ([]store.InboxItem, error)
	BumpInboxAttempt(ctx context.Context, id int64, revision int) (int, error)
	FinishInbox(ctx context.Context, id int64, revision int, state, code string) error
	GetUser(ctx context.Context, id uuid.UUID) (model.User, *time.Location, error)
	ApplyDirect(ctx context.Context, r store.DirectRecord) (store.DirectResult, error)
	ApplyDeregistration(ctx context.Context, inboxID int64, revision int, user uuid.UUID, provider, minPolicy string, emit func(tx pgx.Tx, days []string) error) (store.DirectResult, error)
	ApplyPermissions(ctx context.Context, inboxID int64, revision int, user uuid.UUID, provider, minPolicy string, missing bool) (store.DirectOutcome, error)
	RepublishInbox(ctx context.Context, limit int, emit func(tx pgx.Tx, ev model.InboxReceivedEvent) error) (int, error)
	ExpireInbox(ctx context.Context, limit int) (int64, error)
	PurgeInbox(ctx context.Context, limit int) (map[string]int64, error)
	InboxAges(ctx context.Context) (store.InboxAges, error)
}

// The hook reads a body nobody has verified yet, up to connect.MaxPushBytes, so it is bounded twice (D260):
// at most maxHookInflight pushes are in flight per instance (ingest.yaml runs containerConcurrency 4: the rest of
// the slots stay for /v1/sync), and the body must arrive within hookReadTimeout.
const (
	maxHookInflight = 2
	hookReadTimeout = 10 * time.Second
	hookRetryAfter  = "5" // seconds, for the 503 of a full hook
)

// connectHook is the hook, the consumer and the tick step.
type connectHook struct {
	minPolicy   string        // the oldest privacy policy version whose consent the hook and the consumer accept (D259)
	slots       chan struct{} // the hook's in-flight pushes: a push takes a slot or is answered 503
	readTimeout time.Duration // the whole body's deadline
	st          connectStore
	reg         *connect.PushRegistry
	beta        config.Beta
	log         *slog.Logger
	insert      func(ctx context.Context, tx pgx.Tx, m events.Message) error // the outbox insert in the caller's transaction
	flush       func(ctx context.Context)                                    // the outbox's inline flush
	now         func() time.Time
}

// minPolicy is CONNECT_MIN_POLICY_VERSION (config.Connect.MinPolicyVersion); empty is the policy in force.
func newConnectHook(st connectStore, reg *connect.PushRegistry, beta config.Beta, minPolicy string, ob *outbox.Outbox, log *slog.Logger) *connectHook {
	if minPolicy == "" {
		minPolicy = beta.PrivacyPolicyVersion
	}
	return &connectHook{st: st, reg: reg, beta: beta, log: log, now: time.Now, minPolicy: minPolicy,
		slots: make(chan struct{}, maxHookInflight), readTimeout: hookReadTimeout,
		insert: func(ctx context.Context, tx pgx.Tx, m events.Message) error {
			return outbox.Insert(ctx, tx, "ingest", m)
		},
		flush: func(ctx context.Context) { ob.Flush(ctx) }}
}

// pushRegistry is the push halves of the providers cfg serves. Phase 1 has the fake only; a real provider's
// adapter arrives with phase 2, so naming one without an adapter stops the start.
func pushRegistry(cfg config.Connect) (*connect.PushRegistry, error) {
	var ps []connect.Provider
	for _, name := range cfg.Providers {
		switch name {
		case config.FakeName:
			ps = append(ps, fake.New(cfg.FakePushSecret))
		default:
			return nil, fmt.Errorf("CONNECT_PROVIDERS names %q, which has no adapter in this build", name)
		}
	}
	reg, err := connect.NewRegistry(ps...)
	if err != nil {
		return nil, err
	}
	return reg.PushSide(), nil
}

// itemOutcomes are the closed set of item outcomes the "connect push" line counts.
var itemOutcomes = []string{"stored", "updated", "duplicate", "stale", "unknown_user", "no_consent", "id_collision", "over_cap", "bad_item"}

// push is POST /connect/{provider}/push (design 7.2). It sits outside /v1/: no bearer, no App Attest, no invite
// gate. The provider's verification is all that stands between the internet and an account's data, so nothing is
// parsed before it passes, and nothing is processed inline: the answer follows the commit of the inbox rows and
// their events.
func (c *connectHook) push(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	ps, ok := c.reg.Get(name)
	if !ok {
		httpx.NotFound(w, r)
		return
	}
	ctx := r.Context()
	// kind is the push's record kind when all its items share one, "mixed" when they differ and "none" for a
	// request refused before its body was parsed (rejected, too_large, bad_body): the log-based counter
	// ryft_connect_pushes and the metric both label by it.
	outcome, kind, counts := "error", "none", map[string]int{}
	defer func() {
		metrics.ConnectPushes.WithLabelValues(name, kind, outcome).Inc()
		attrs := []any{"provider", name, "kind", kind, "outcome", outcome}
		for _, o := range itemOutcomes {
			if n := counts[o]; n > 0 {
				attrs = append(attrs, o, n)
			}
		}
		c.log.InfoContext(ctx, "connect push", attrs...)
	}()
	// Before anything is read: past the cap the push is answered 503 and nothing is read or stored; the provider
	// sends it again. Sync keeps the instance's other slots.
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		outcome = "busy"
		w.Header().Set("Retry-After", hookRetryAfter)
		httpx.WriteError(w, http.StatusServiceUnavailable, "busy, send the push again")
		return
	}
	limit := min(ps.MaxPushBytes(), connect.MaxPushBytes)
	if r.ContentLength > limit {
		outcome = "too_large"
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	// A short deadline for the whole body: an unverified sender that trickles it holds a slot for seconds, not for
	// the request limit. A writer that cannot set one (a test's recorder) just has none.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(c.readTimeout))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		outcome = "too_large"
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	case errors.Is(err, os.ErrDeadlineExceeded):
		outcome = "timeout"
		httpx.WriteError(w, http.StatusRequestTimeout, "body too slow")
		return
	case err != nil:
		outcome = "bad_body"
		httpx.WriteError(w, http.StatusBadRequest, "body unreadable")
		return
	}
	switch err := ps.VerifyPush(r.Header, body, c.now()); {
	case err == nil:
	case errors.Is(err, connect.ErrUnverified):
		outcome = "rejected"
		httpx.WriteError(w, http.StatusUnauthorized, "not verified")
		return
	default:
		outcome = "rejected"
		w.Header().Set("Retry-After", "30")
		httpx.WriteError(w, http.StatusServiceUnavailable, "cannot verify now")
		return
	}
	items, err := ps.SplitPush(body)
	if err != nil || len(items) > connect.MaxItems {
		outcome = "bad_body"
		httpx.WriteError(w, http.StatusBadRequest, "bad body")
		return
	}
	kind = pushKind(items)
	var hook []store.HookItem
	for _, it := range items {
		if !boundedItem(it) {
			counts["bad_item"]++
			metrics.ConnectPushItems.WithLabelValues(name, labelKind(it.Kind), "bad_item").Inc()
			continue
		}
		hook = append(hook, store.HookItem{Kind: it.Kind, RecordID: it.RecordID, ProviderUserID: it.ProviderUserID, Version: it.Version, Payload: it.Payload})
	}
	reqID := ""
	if f := httpx.Info(ctx); f != nil {
		reqID = f.RequestID
	}
	outs, err := c.st.StoreInbox(ctx, name, c.minPolicy, hook, func(tx pgx.Tx, ev model.InboxReceivedEvent) error {
		ev.RequestID = reqID
		m, err := events.InboxReceived(ev)
		if err != nil {
			return err
		}
		return c.insert(ctx, tx, m)
	})
	if err != nil {
		c.log.ErrorContext(ctx, "connect push not stored", "provider", name, "err", err)
		w.Header().Set("Retry-After", "30")
		httpx.WriteError(w, http.StatusServiceUnavailable, "temporarily unavailable, send the push again")
		return
	}
	fresh := false
	for i, o := range outs {
		counts[string(o)]++
		metrics.ConnectPushItems.WithLabelValues(name, labelKind(hook[i].Kind), string(o)).Inc()
		fresh = fresh || o == store.HookStored || o == store.HookUpdated
		if o == store.HookCollision {
			// The adapter's record ids are not unique across users: its bug. Provider and kind only.
			c.log.ErrorContext(ctx, "connect record id collision", "provider", name, "kind", hook[i].Kind)
		}
	}
	if fresh {
		c.flush(ctx) // before the answer: nothing runs after it (D219)
	}
	outcome = "accepted"
	w.WriteHeader(http.StatusOK)
}

// pushKind is the one kind all of a push's items share (an unknown kind as "unknown"), or "mixed".
func pushKind(items []connect.PushItem) string {
	kind := "none"
	for i, it := range items {
		k := labelKind(it.Kind)
		if i == 0 {
			kind = k
		} else if k != kind {
			return "mixed"
		}
	}
	return kind
}

// boundedItem says whether an item is one the inbox can hold: a known kind and bounded, well-formed text. NUL
// characters and invalid UTF-8 are refused here because Postgres refuses them, and a row that could never be
// stored would turn the provider's retry into a loop.
func boundedItem(it connect.PushItem) bool {
	text := func(s string, max int) bool {
		return len(s) >= 1 && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
	}
	return connect.KnownKind(it.Kind) && text(it.RecordID, connect.MaxRecordIDLen) && text(it.ProviderUserID, 128) && it.Version >= 0 &&
		len(it.Payload) > 0 && len(it.Payload) <= connect.MaxItemBytes && utf8.Valid(it.Payload) && !strings.Contains(string(it.Payload), `\u0000`)
}

// labelKind is a metric label: the item's kind when it is one of the four, else "unknown".
func labelKind(kind string) string {
	if connect.KnownKind(kind) {
		return kind
	}
	return "unknown"
}

// The window a direct record must fall in is a sync's (screen).
const (
	futureSlackDirect = futureSlack
	pastWindowDirect  = pastWindow
)

// consume is the handler of inbox.received (design 7.3). Every outcome but a transient error acks the message; a
// transient error (the database) returns it, the row stays pending, and Pub/Sub delivers again. It is idempotent
// (D100): a row no longer pending is acked untouched.
func (c *connectHook) consume(ctx context.Context, ev model.InboxReceivedEvent) error {
	row, err := c.st.InboxRow(ctx, ev.InboxID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && row.State != "pending") {
		return nil
	}
	if err != nil {
		return c.retry(ctx, ev.Provider, ev.Kind, err)
	}
	if ev.Revision != row.Revision || ev.UserID != row.UserID || ev.Provider != row.Provider || ev.Kind != row.Kind {
		return nil
	}
	n, err := c.st.BumpInboxAttempt(ctx, row.ID, row.Revision)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return c.retry(ctx, row.Provider, row.Kind, err)
	}
	if n >= store.MaxInboxAttempts {
		return c.finish(ctx, row, "failed", "attempts")
	}
	user, loc, err := c.st.GetUser(ctx, row.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return c.finish(ctx, row, "dropped", "no_connection")
	case err != nil:
		return c.retry(ctx, row.Provider, row.Kind, err)
	}
	// Consent, whatever CONSENT_ENFORCE says: health data from a source the user never agreed to is not stored
	// (D257, GDPR Art. 9). Consent to the cutoff version or a later one counts, a withdrawn one never does (D259).
	if !c.consented(user) {
		return c.finish(ctx, row, "dropped", "no_consent")
	}
	ps, ok := c.reg.Get(row.Provider)
	if !ok {
		return c.finish(ctx, row, "failed", "provider_off")
	}
	rec, err := ps.ParseRecord(row.Kind, row.Payload)
	if err != nil {
		return c.finish(ctx, row, "failed", "parse")
	}
	emit := func(reason string) func(tx pgx.Tx, days []string) error {
		return func(tx pgx.Tx, days []string) error {
			m, err := events.Ingested(model.IngestedEvent{UserID: row.UserID, Days: days, Reason: reason}, fmt.Sprintf("inbox:%d:%d", row.ID, row.Revision))
			if err != nil {
				return err
			}
			return c.insert(ctx, tx, m)
		}
	}
	switch row.Kind {
	case connect.KindSleep, connect.KindHRV:
		r, code := c.screenRecord(row, rec, loc)
		if code != "" {
			state := "failed"
			if code == "stale" {
				state = "dropped"
			}
			return c.finish(ctx, row, state, code)
		}
		r.Emit = emit(model.ReasonConnect)
		r.Also = c.alsoPending(ctx, row, ps, loc)
		res, err := c.st.ApplyDirect(ctx, r)
		if err != nil && len(r.Also) > 0 && ctx.Err() == nil {
			// A record among the others may be what failed: this row on its own, so it is not failed with it.
			c.log.WarnContext(ctx, "connect inbox batch failed, applying the row alone", "provider", row.Provider, "kind", row.Kind, "records", len(r.Also))
			r.Also = nil
			res, err = c.st.ApplyDirect(ctx, r)
		}
		if err != nil {
			return c.retry(ctx, row.Provider, row.Kind, err)
		}
		kinds := map[int64]string{}
		for _, x := range r.Also {
			kinds[x.InboxID] = x.Kind
		}
		for _, id := range res.Also {
			metrics.ConnectInboxProcessed.WithLabelValues(row.Provider, labelKind(kinds[id]), "processed").Inc()
		}
		if len(res.Also) > 0 {
			c.log.InfoContext(ctx, "connect inbox batch", "provider", row.Provider, "records", 1+len(res.Also))
		}
		return c.done(ctx, row, res.Outcome, res.Days)
	case connect.KindDeregistration:
		res, err := c.st.ApplyDeregistration(ctx, row.ID, row.Revision, row.UserID, row.Provider, c.minPolicy, emit(model.ReasonDisconnect))
		if err != nil {
			return c.retry(ctx, row.Provider, row.Kind, err)
		}
		return c.done(ctx, row, res.Outcome, res.Days)
	case connect.KindPermissions:
		missing := slices.ContainsFunc(ps.Required(), func(p string) bool { return !slices.Contains(rec.Permissions, p) })
		o, err := c.st.ApplyPermissions(ctx, row.ID, row.Revision, row.UserID, row.Provider, c.minPolicy, missing)
		if err != nil {
			return c.retry(ctx, row.Provider, row.Kind, err)
		}
		return c.done(ctx, row, o, nil)
	}
	return c.finish(ctx, row, "failed", "parse")
}

// alsoPending are the account's other pending sleep and hrv rows, parsed and screened, to store in the same
// transaction as row's (P2-3): one ingested event for the lot, not one per row in front of the account's next
// sync in scoring's per-user order. A row that does not parse or screen is left to its own delivery (it fails or
// drops there); a failed read of the list is no batch.
func (c *connectHook) alsoPending(ctx context.Context, row store.InboxItem, ps connect.PushSide, loc *time.Location) []store.DirectRecord {
	others, err := c.st.PendingDirect(ctx, row.UserID, row.Provider, row.ID, store.MaxBatchRecords-1)
	if err != nil {
		return nil
	}
	var out []store.DirectRecord
	rows := 0
	for _, o := range others {
		rec, err := ps.ParseRecord(o.Kind, o.Payload)
		if err != nil {
			continue
		}
		r, code := c.screenRecord(o, rec, loc)
		if code != "" || rows+len(r.Sleep)+len(r.Samples) > store.MaxBatchRows {
			continue
		}
		rows += len(r.Sleep) + len(r.Samples)
		out = append(out, r)
	}
	return out
}

// consented says whether the account's current consent (the user row's: the latest not withdrawn) is to the cutoff
// policy version or a later one. Versions are YYYY-MM-DD dates, so the text order is the date order.
func (c *connectHook) consented(u model.User) bool {
	return u.Consent != nil && u.Consent.PolicyVersion >= c.minPolicy
}

// screenRecord is a sync's screen for a parsed record: known stages and types, the window, and a bounded span and
// size. It returns the record to store, or a code: "stale" (nothing left inside the window: dropped) or "parse"
// (a record that cannot be one: failed). The rows' origin, vendor and source are the store's to stamp.
func (c *connectHook) screenRecord(row store.InboxItem, rec connect.Records, loc *time.Location) (store.DirectRecord, string) {
	now := c.now()
	r := store.DirectRecord{InboxID: row.ID, Revision: row.Revision, UserID: row.UserID, Provider: row.Provider, Kind: row.Kind, RecordID: row.RecordID, MinPolicy: c.minPolicy, Loc: loc,
		NightState: rec.NightState}
	outside := func(start, end time.Time) bool {
		return start.After(now.Add(futureSlackDirect)) || end.Before(now.Add(-pastWindowDirect))
	}
	if len(rec.Sleep)+len(rec.Samples) > connect.MaxRows {
		return r, "parse"
	}
	var lo, hi time.Time
	widen := func(start, end time.Time) {
		if lo.IsZero() || start.Before(lo) {
			lo = start
		}
		if end.After(hi) {
			hi = end
		}
	}
	for _, s := range rec.Sleep {
		switch {
		case !model.KnownSleepStages[s.Stage] || !s.End.After(s.Start):
			return r, "parse"
		case outside(s.Start, s.End):
			continue
		}
		widen(s.Start, s.End)
		r.Sleep = append(r.Sleep, s)
	}
	for _, s := range rec.Samples {
		switch {
		case !model.KnownSampleTypes[s.Type] || s.End.Before(s.Start):
			return r, "parse"
		case outside(s.Start, s.End):
			continue
		}
		widen(s.Start, s.End)
		r.Samples = append(r.Samples, s)
	}
	switch {
	case len(r.Sleep)+len(r.Samples) == 0:
		return r, "stale"
	case hi.Sub(lo) > connect.MaxSpan:
		return r, "parse"
	}
	return r, ""
}

// done records the outcome of an apply and flushes the event after successful processing.
func (c *connectHook) done(ctx context.Context, row store.InboxItem, o store.DirectOutcome, days []string) error {
	switch o {
	case store.DirectApplied:
		if len(days) > 0 {
			c.flush(ctx)
		}
		metrics.ConnectInboxProcessed.WithLabelValues(row.Provider, row.Kind, "processed").Inc()
		c.log.InfoContext(ctx, "connect inbox", "provider", row.Provider, "kind", row.Kind, "outcome", "processed", "days", len(days))
	case store.DirectNoConnection, store.DirectNoConsent:
		metrics.ConnectInboxProcessed.WithLabelValues(row.Provider, row.Kind, string(o)).Inc()
		c.log.InfoContext(ctx, "connect inbox", "provider", row.Provider, "kind", row.Kind, "outcome", string(o))
	}
	return nil // not pending any more: a repeat, nothing to do
}

// finish ends a row without applying it: dropped (its payload goes at once) or failed (kept 7 days), with its code.
func (c *connectHook) finish(ctx context.Context, row store.InboxItem, state, code string) error {
	err := c.st.FinishInbox(ctx, row.ID, row.Revision, state, code)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return c.retry(ctx, row.Provider, row.Kind, err)
	}
	outcome := code
	if state == "failed" {
		outcome = "failed"
	}
	metrics.ConnectInboxProcessed.WithLabelValues(row.Provider, row.Kind, labelOutcome(outcome)).Inc()
	c.log.InfoContext(ctx, "connect inbox", "provider", row.Provider, "kind", row.Kind, "outcome", labelOutcome(outcome), "code", code)
	return nil
}

// labelOutcome keeps ryft_connect_inbox_processed_total's closed outcomes: a dropped row's code is its outcome
// (no_consent, no_connection, stale), every failure is "failed".
func labelOutcome(o string) string {
	switch o {
	case "no_consent", "no_connection", "stale", "failed":
		return o
	}
	return "failed"
}

// retry counts a transient failure and returns it, so the push is nacked and delivered again.
func (c *connectHook) retry(ctx context.Context, provider, kind string, err error) error {
	metrics.ConnectInboxProcessed.WithLabelValues(provider, labelKind(kind), "retry").Inc()
	return fmt.Errorf("inbox: %w", err)
}

// ---- the tick ---------------------------------------------------------------------------------------------------

const (
	inboxBatch      = 500 // rows per statement
	republishBatch  = 100
	noticeHourUTC   = 6 // the tick that starts 06:00 to 06:14 UTC writes the day's notice
	noticeMinuteMax = 14
)

// tick is ingest's step "connect inbox" (design 7.4): the pending rows older than an hour get their event again, the
// rows pending for 7 days fail, the retained ones are deleted, and the gauges and the daily notice are set.
func (c *connectHook) tick(ctx context.Context) error {
	var errs []error
	{ // a provider turned off still has rows to keep, so this runs whatever is served
		emit := func(tx pgx.Tx, ev model.InboxReceivedEvent) error {
			m, err := events.InboxReceived(ev)
			if err != nil {
				return err
			}
			return c.insert(ctx, tx, m)
		}
		if n, err := c.st.RepublishInbox(ctx, republishBatch, emit); err != nil {
			errs = append(errs, fmt.Errorf("republish: %w", err))
		} else if n > 0 {
			c.log.InfoContext(ctx, "connect inbox republished", "rows", n)
		}
		if n, err := c.st.ExpireInbox(ctx, inboxBatch); err != nil {
			errs = append(errs, fmt.Errorf("expire: %w", err))
		} else if n > 0 {
			c.log.InfoContext(ctx, "connect inbox expired", "rows", n)
		}
		purged, err := c.st.PurgeInbox(ctx, inboxBatch)
		for state, n := range purged {
			if n > 0 {
				metrics.ConnectInboxPurged.WithLabelValues(state).Add(float64(n))
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("purge: %w", err))
		}
	}
	ages, err := c.st.InboxAges(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("ages: %w", err))
	} else {
		metrics.ConnectInboxOldest.WithLabelValues("pending").Set(ages.Pending)
		metrics.ConnectInboxOldest.WithLabelValues("failed").Set(ages.Failed)
		if ages.FailedRows > 0 && dailyNoticeSlot(c.now()) {
			// One of ryft_notices' kinds (Cloud Monitoring policy 6): at most once a day, while rows are failed.
			c.log.WarnContext(ctx, "connect inbox failed", "rows", ages.FailedRows)
		}
	}
	return errors.Join(errs...)
}

// dailyNoticeSlot is true for the tick that starts 06:00 to 06:14 UTC: the one tick of the day that writes the
// connection notices, so policy 6 notifies once a day, not every 15 minutes.
func dailyNoticeSlot(t time.Time) bool {
	t = t.UTC()
	return t.Hour() == noticeHourUTC && t.Minute() <= noticeMinuteMax
}

// register mounts the hook and the consumer's push route on mux.
func (c *connectHook) register(mux *http.ServeMux, tr *eventbus.Transport) error {
	mux.HandleFunc("POST /connect/{provider}/push", c.push)
	if tr == nil {
		return nil
	}
	p, err := tr.Push(events.ConsumerIngest, events.IngestHandler(c.consume), nil)
	if err != nil {
		return err
	}
	p.Register(mux)
	return nil
}
