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
// project's public auth setting mailer_autoconfirm at start and every hour, and while it is true, unknown (not
// read yet, unreadable, or too old) or the api cannot ask Supabase for an account at all, bound codes are
// neither created nor redeemed. Fail closed: unknown is treated like true.

// Why bound invites work or do not (the "state" of the log line; never a label with an id).
const (
	bindOK          = "ok"
	bindAutoconfirm = "autoconfirm"     // Confirm email is off: an address is "confirmed" without a mailbox
	bindUnknown     = "unknown"         // the settings were not read, could not be read, or the last read is too old
	bindNoAdmin     = "no_admin_client" // apikey mode, or no SUPABASE_SERVICE_ROLE_KEY: the api cannot look an account up
)

// bindingGate says whether bound invites work now (one of the bind* states).
type bindingGate interface {
	State() string
}

// autoconfirmSource reads mailer_autoconfirm (auth.Admin).
type autoconfirmSource interface {
	EmailAutoconfirm(ctx context.Context) (bool, error)
}

// bindingMaxAge is how long a good read counts: the loop reads every hour, so an older one means the loop is
// stuck, and the answer is unknown.
const bindingMaxAge = 3 * time.Hour

// confirmGuard is the cached reading of the project's settings. src nil: the api has no Supabase admin client.
type confirmGuard struct {
	src autoconfirmSource
	log *slog.Logger
	now func() time.Time

	mu     sync.Mutex
	known  bool      // the last read worked
	auto   bool      // ... and said mailer_autoconfirm
	at     time.Time // of that read
	logged string    // the state last written to the log
}

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

// Refresh reads the setting once. A failed read makes the state unknown at once (it does not keep the last
// good answer: the setting may have changed while Supabase could not be reached).
func (g *confirmGuard) Refresh(ctx context.Context) {
	if g.src != nil {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		auto, err := g.src.EmailAutoconfirm(ctx)
		cancel()
		g.mu.Lock()
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

// Run reads the setting every interval until ctx ends (the caller made the first read with Refresh).
func (g *confirmGuard) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.Refresh(ctx)
		}
	}
}

// bindingState is the handler's view; a handler without a gate (a test) cannot bind: fail closed.
func (h *handler) bindingState() string {
	if h.binding == nil {
		return bindNoAdmin
	}
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
