package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/timkrebs/ryft/internal/metrics"
)

// Email-bound invites rest on one fact about the Supabase project: that it confirms an address before it
// marks it confirmed. With *Confirm email* off, Supabase sets email_confirmed_at at sign-up, so anyone who has
// a leaked link could sign up with the victim's address and redeem the code (D159). The api therefore reads the
// project's public auth setting mailer_autoconfirm, and while it is true, unknown (not read yet, unreadable, or
// too old) or the api cannot ask Supabase for an account at all, bound codes are neither created nor redeemed.
// Fail closed: unknown is treated like true.
//
// When it reads (D219): never at start (an instance starts on a request, which must not wait on Supabase), on
// every tick that reaches this instance ("invite settings"), and from the first request that needs the answer
// (an email-bound create or redeem) when this instance has not tried for lazyReadAfter. A tick reaches one
// instance only, so the others rely on that lazy read.

// Why bound invites work or do not (the "state" of the log line; never a label with an id).
const (
	bindOK          = "ok"
	bindAutoconfirm = "autoconfirm"     // Confirm email is off: an address is "confirmed" without a mailbox
	bindUnknown     = "unknown"         // the settings were not read, could not be read, or the last read is too old
	bindNoAdmin     = "no_admin_client" // apikey mode, or no SUPABASE_SERVICE_ROLE_KEY: the api cannot look an account up
)

// bindingGate says whether bound invites work now (one of the bind* states). Prime reads the setting first
// when this instance has no recent try; a handler calls it before State.
type bindingGate interface {
	Prime(ctx context.Context)
	State() string
}

// autoconfirmSource reads mailer_autoconfirm (auth.Admin).
type autoconfirmSource interface {
	EmailAutoconfirm(ctx context.Context) (bool, error)
}

const (
	// bindingMaxAge is how long a good read counts: an older one means neither the tick nor a request has read
	// it for hours, and the answer is unknown.
	bindingMaxAge = 3 * time.Hour
	// lazyReadAfter is how long a try (good or failed) spares the next request a read: the tick's interval, so
	// a busy instance asks Supabase about as often as the tick does, and a failed read is not repeated by every
	// request in a row.
	lazyReadAfter = 15 * time.Minute
	// settingsTimeout bounds one read.
	settingsTimeout = 10 * time.Second
)

// confirmGuard is the cached reading of the project's settings. src nil: the api has no Supabase admin client.
type confirmGuard struct {
	src autoconfirmSource
	log *slog.Logger
	now func() time.Time

	reading sync.Mutex // one lazy read at a time: the requests that arrive meanwhile wait for its answer

	mu     sync.Mutex
	known  bool      // the last read worked
	auto   bool      // ... and said mailer_autoconfirm
	at     time.Time // of that read
	tried  time.Time // the last read, good or failed; zero: never
	logged string    // the state last written to the log
}

// newConfirmGuard reads nothing: the first tick or the first request that needs the answer does.
func newConfirmGuard(src autoconfirmSource, log *slog.Logger) *confirmGuard {
	return &confirmGuard{src: src, log: log, now: time.Now}
}

// State is why bound invites work or do not, now.
func (g *confirmGuard) State() string {
	if g.src == nil {
		return bindNoAdmin
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case !g.known || g.now().Sub(g.at) > bindingMaxAge:
		return bindUnknown
	case g.auto:
		return bindAutoconfirm
	}
	return bindOK
}

// Prime reads the setting when this instance has not tried within lazyReadAfter, on a context the request's
// cancel does not reach: a client that hangs up must not leave the instance unknown until the next try.
func (g *confirmGuard) Prime(ctx context.Context) {
	if g.src == nil {
		return
	}
	g.reading.Lock()
	defer g.reading.Unlock()
	g.mu.Lock()
	due := g.tried.IsZero() || g.now().Sub(g.tried) >= lazyReadAfter
	g.mu.Unlock()
	if due {
		g.Refresh(context.WithoutCancel(ctx))
	}
}

// Refresh reads the setting once: the tick's "invite settings" step, and Prime. A failed read makes the state
// unknown at once (it does not keep the last good answer: the setting may have changed while Supabase could not
// be reached). A read cut short because ctx ended (the tick's budget) records nothing, as db.Ping does: it says
// nothing about Supabase, and recording it would also spare this instance's next request the read it needs.
func (g *confirmGuard) Refresh(ctx context.Context) {
	if g.src != nil {
		rctx, cancel := context.WithTimeout(ctx, settingsTimeout)
		auto, err := g.src.EmailAutoconfirm(rctx)
		cancel()
		if err != nil && ctx.Err() != nil {
			return
		}
		g.mu.Lock()
		g.tried = g.now()
		switch {
		case err != nil:
			g.known = false
			metrics.InviteSettingsChecks.WithLabelValues("error").Inc()
			// The error holds a status and GoTrue's error_code, or the transport's cause; no address, no id.
			g.log.WarnContext(ctx, "invite binding: auth settings unreadable", "err", err)
		case auto:
			g.known, g.auto, g.at = true, true, g.now()
			metrics.InviteSettingsChecks.WithLabelValues("autoconfirm").Inc()
		default:
			g.known, g.auto, g.at = true, false, g.now()
			metrics.InviteSettingsChecks.WithLabelValues("ok").Inc()
		}
		g.mu.Unlock()
	}
	state := g.State()
	if state == bindOK {
		metrics.InviteBindingAvailable.Set(1)
	} else {
		metrics.InviteBindingAvailable.Set(0)
	}
	g.mu.Lock()
	changed := g.logged != state
	g.logged = state
	g.mu.Unlock()
	switch {
	case changed && state == bindOK:
		g.log.InfoContext(ctx, "invite binding", "state", state)
	case changed:
		g.log.WarnContext(ctx, "invite binding", "state", state)
	}
}

// bindingState is the handler's view, read first if this instance has no recent try; a handler without a gate
// (a test) cannot bind: fail closed.
func (h *handler) bindingState(ctx context.Context) string {
	if h.binding == nil {
		return bindNoAdmin
	}
	h.binding.Prime(ctx)
	return h.binding.State()
}

// bindingMessage is the 422 text of a create with an email while bound invites do not work.
func bindingMessage(state string) string {
	switch state {
	case bindAutoconfirm:
		return "email-bound invites are not available: the Supabase project confirms no emails (Confirm email is off)"
	case bindNoAdmin:
		return "email-bound invites are not available: the api has no Supabase admin key"
	}
	return "email-bound invites are not available right now: the Supabase sign-up settings could not be read"
}
