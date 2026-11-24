package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/connect/fake"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/sealed"
	"github.com/timkrebs/ryft/internal/store"
)

// Direct connections on the api (D257, design 4 and 6): the OAuth flow (start, callback, confirm), the sealed
// tokens, the cap, disconnecting, and the tick's refresh and deregistration. The api alone holds
// DATA_ENCRYPTION_KEY; ingest's half is cmd/ingest/connect.go and never sees a token.

// connectStore is what the routes and the tick need of the store (*store.Store; a fake in tests).
type connectStore interface {
	Flags(ctx context.Context) ([]model.FeatureFlag, error)
	Connections(ctx context.Context, user uuid.UUID) ([]store.ConnectionRow, error)
	Accepting(ctx context.Context, user uuid.UUID, provider string, cap int) (bool, error)
	StartConnect(ctx context.Context, user uuid.UUID, provider string, cap int, st store.NewState) error
	ConsumeState(ctx context.Context, hash [32]byte, provider string) (store.FlowState, error)
	PutPendingCapped(ctx context.Context, p store.Pending, cap int) error
	QueueDeregistration(ctx context.Context, user uuid.UUID, provider, puid, reason string, accessEnc, refreshEnc []byte) (int64, error)
	ConfirmConnection(ctx context.Context, in store.ConfirmIn) (store.ConfirmOut, error)
	DropPending(ctx context.Context, user uuid.UUID, provider, reason string) ([]int64, error)
	Disconnect(ctx context.Context, user uuid.UUID, provider string, emit func(tx pgx.Tx, user uuid.UUID, days []string) error) (store.DisconnectOut, error)
	GetUser(ctx context.Context, id uuid.UUID) (model.User, *time.Location, error)

	Deregistration(ctx context.Context, id int64) (store.Dereg, error)
	UserDeregistrations(ctx context.Context, user uuid.UUID) ([]store.Dereg, error)
	ClaimDeregistrations(ctx context.Context, limit int) ([]store.Dereg, error)
	HoldsLiveGrant(ctx context.Context, provider, puid string) (bool, error)
	FinishDeregistration(ctx context.Context, id int64) error
	RetryDeregistration(ctx context.Context, id int64, code string, next time.Time) error
	GiveUpDeregistrations(ctx context.Context) ([]store.ExpiredDeregistration, error)
	OldestDeregistration(ctx context.Context) (time.Duration, error)

	ClaimRefreshes(ctx context.Context, limit int) ([]store.RefreshDue, error)
	SaveRefresh(ctx context.Context, user uuid.UUID, provider string, claimedRefresh, accessEnc, refreshEnc []byte, accessExp, due time.Time) (bool, error)
	RefreshFailed(ctx context.Context, user uuid.UUID, provider, code string, attempts int, next time.Time) error
	NeedsReauth(ctx context.Context, user uuid.UUID, provider, code string) error

	PurgeConnect(ctx context.Context) (store.Purged, error)
	ConnectionCounts(ctx context.Context) (map[string]map[string]int, error)
	StaleConnections(ctx context.Context, provider string) (int, error)
	ProviderLoad(ctx context.Context, provider string, except uuid.UUID) (int, error)
}

// Times and sizes of the flow.
const (
	attemptTimeout   = 5 * time.Second  // one deregistration attempt inline (disconnect, DELETE /v1/me)
	providerTimeout  = 15 * time.Second // one call to the provider (exchange, refresh)
	maxCodeLen       = 2048             // the provider's code in the callback's query
	refreshBatch     = 20
	deregisterBatch  = 20
	backoffBase      = 15 * time.Minute
	backoffMax       = 24 * time.Hour
	capacityNoticeAt = 0.8
	stuckAfter       = 24 * time.Hour

	// The tick's steps each have a deadline of their own inside the tick's 45 s (P2-4), so one slow provider ends
	// its own step and leaves the rest of the tick: the database-only steps (purge, the gauges and the daily notices)
	// get dbStepBudget, the two that call a provider (refresh, deregister) providerStepBudget.
	dbStepBudget       = 8 * time.Second
	providerStepBudget = 25 * time.Second // room for a refresh call (providerTimeout) and its save (persistBudget) at least once
	// persistBudget is what saving a refreshed grant gets, on a context that outlives the step: the provider has
	// already rotated its tokens, and a refresh token is single use.
	persistBudget = 5 * time.Second
)

// connectAPI serves the Connections routes and the tick's steps.
type connectAPI struct {
	st   connectStore
	reg  *connect.Registry
	cfg  config.Connect
	key  *sealed.Key // nil without DATA_ENCRYPTION_KEY: tokens cannot be opened
	log  *slog.Logger
	beta config.Beta

	// insert writes an event in the caller's transaction (the outbox, publisher api); flush sends what it wrote.
	insert func(ctx context.Context, tx pgx.Tx, m events.Message) error
	flush  func(ctx context.Context)
	now    func() time.Time

	// The deadlines of the tick's steps (the constants above; tests shorten them).
	dbBudget, providerBudget, persist, callBudget time.Duration
}

func newConnectAPI(st connectStore, reg *connect.Registry, cfg config.Connect, key *sealed.Key, beta config.Beta, ob *outbox.Outbox, log *slog.Logger) *connectAPI {
	c := &connectAPI{st: st, reg: reg, cfg: cfg, key: key, beta: beta, log: log, now: time.Now,
		dbBudget: dbStepBudget, providerBudget: providerStepBudget, persist: persistBudget, callBudget: providerTimeout}
	c.insert = func(ctx context.Context, tx pgx.Tx, m events.Message) error { return outbox.Insert(ctx, tx, "api", m) }
	c.flush = func(ctx context.Context) {
		if ob != nil {
			ob.Flush(ctx)
		}
	}
	return c
}

// connectRegistry is the providers cfg serves: the fake in compose; a real provider needs its adapter, which is
// phase 2's, so naming one without it stops the start.
func connectRegistry(cfg config.Connect) (*connect.Registry, error) {
	var ps []connect.Provider
	for _, name := range cfg.Providers {
		switch name {
		case config.FakeName:
			ps = append(ps, fake.New(cfg.FakePushSecret))
		default:
			return nil, fmt.Errorf("CONNECT_PROVIDERS names %q, which has no adapter in this build", name)
		}
	}
	return connect.NewRegistry(ps...)
}

// ---- sealing ---------------------------------------------------------------------------------------------------

// The associated data of each sealed value (design 5.1): a ciphertext copied to another row does not open.
func adAccess(p string, u uuid.UUID) []byte { return []byte("connect:access:" + p + ":" + u.String()) }
func adRefresh(p string, u uuid.UUID) []byte {
	return []byte("connect:refresh:" + p + ":" + u.String())
}
func adPendingAccess(p string, u uuid.UUID) []byte {
	return []byte("connect:pending-access:" + p + ":" + u.String())
}
func adPendingRefresh(p string, u uuid.UUID) []byte {
	return []byte("connect:pending-refresh:" + p + ":" + u.String())
}
func adState(hash [32]byte) []byte { return []byte("connect:state:" + hex.EncodeToString(hash[:])) }

// seal seals plain under ad; nil for an empty value (a grant without a refresh token).
func (c *connectAPI) seal(plain string, ad []byte) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	if c.key == nil {
		return nil, errors.New("no DATA_ENCRYPTION_KEY")
	}
	return c.key.Seal([]byte(plain), ad)
}

// open opens enc under the first of ads that fits: a queue row's tokens carry the ADs of the table they came from.
func (c *connectAPI) open(enc []byte, ads ...[]byte) (string, error) {
	if len(enc) == 0 {
		return "", nil
	}
	if c.key == nil {
		return "", sealed.ErrOpen
	}
	for _, ad := range ads {
		if b, err := c.key.Open(enc, ad); err == nil {
			return string(b), nil
		}
	}
	return "", sealed.ErrOpen
}

// ---- who may connect what ---------------------------------------------------------------------------------------

// served is the provider when this server serves it and its flag connect.<provider> is on for the account; the fake
// has no flag (CONNECT_FAKE=on is its gate). It answers false for an id that is not served, without saying why.
func (c *connectAPI) served(ctx context.Context, provider string, user uuid.UUID) (connect.Provider, bool, error) {
	p, ok := c.reg.Get(provider)
	if !ok {
		return nil, false, nil
	}
	if provider == config.FakeName {
		return p, true, nil
	}
	all, err := c.st.Flags(ctx)
	if err != nil {
		return nil, false, err
	}
	return p, flags.Enabled(all, "connect."+provider, user), nil
}

func (c *connectAPI) redirectURI(provider string) string {
	return c.cfg.CallbackBase + "/v1/connect/" + provider + "/callback"
}

// consentOK is the consent gate of start and confirm: CONSENT_ENFORCE warn acts as on (D141); off asks nothing.
func (c *connectAPI) consentOK(w http.ResponseWriter, r *http.Request) bool {
	if c.beta.Consent == config.ModeOff || gate.Consented(c.beta, userOf(r)) {
		return true
	}
	metrics.GateRefusals.WithLabelValues(model.CodeConsentRequired, string(config.ModeOn), "api").Inc()
	httpx.WriteCodedError(w, r, http.StatusForbidden, model.CodeConsentRequired,
		"consent to the privacy policy "+c.beta.PrivacyPolicyVersion+" is required before connecting a source")
	return false
}

// providerParam is the path's provider id, or "" when it is not a well-formed id (or the reserved one).
func providerParam(r *http.Request) string {
	p := r.PathValue("provider")
	if !connect.ValidProvider(p) || p == connect.ReservedProvider {
		return ""
	}
	return p
}

// isDisconnect says whether r is DELETE /v1/me/connections/{provider}: exempt from App Attest and the invite gate,
// because a connection can always be ended (the lead's decision of 2026-11-23, like DELETE /v1/me/consent).
func isDisconnect(r *http.Request) bool {
	if r.Method != http.MethodDelete {
		return false
	}
	p, ok := strings.CutPrefix(r.URL.Path, "/v1/me/connections/")
	return ok && p != "" && !strings.Contains(p, "/")
}

func (c *connectAPI) unknownProvider(w http.ResponseWriter, r *http.Request, name, metric string) {
	if metric != "" {
		metrics.ConnectStarts.WithLabelValues(c.label(name), metric).Inc()
	}
	httpx.WriteCodedError(w, r, http.StatusNotFound, model.CodeUnknownProvider, "this provider is not available")
}

// label is a metric label for a provider id from a path: a served one, else "unknown".
func (c *connectAPI) label(name string) string {
	if _, ok := c.reg.Get(name); ok {
		return name
	}
	return connect.ReservedProvider
}

// ---- GET /v1/me/connections --------------------------------------------------------------------------------------

func (c *connectAPI) toModel(row store.ConnectionRow) model.Connection {
	provides := []string{}
	if p, ok := c.reg.Get(row.Provider); ok {
		provides = slices.Clone(p.Provides())
	}
	var last *time.Time
	if row.LastDataAt != nil {
		t := row.LastDataAt.UTC()
		last = &t
	}
	return model.Connection{Provider: row.Provider, Status: row.Status, ConnectedAt: row.ConnectedAt.UTC(), LastDataAt: last,
		Provides: provides, History: model.ConnectionHistoryNone, ProviderUserID: row.ProviderUserID, Permissions: nonNilStrings(row.Permissions)}
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (c *connectAPI) list(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), userOf(r)
	rows, err := c.st.Connections(ctx, u.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := model.ConnectionsResponse{Connections: []model.Connection{}, Available: []model.AvailableConnection{}}
	for _, row := range rows {
		out.Connections = append(out.Connections, c.toModel(row))
	}
	for _, name := range c.reg.Names() {
		p, ok, err := c.served(ctx, name, u.ID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !ok {
			continue
		}
		accepting, err := c.st.Accepting(ctx, u.ID, name, c.cfg.Caps[name])
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		out.Available = append(out.Available, model.AvailableConnection{Provider: name, Provides: slices.Clone(p.Provides()),
			History: model.ConnectionHistoryNone, Accepting: accepting})
	}
	out.Providers = providerCatalog(c.reg.Names(), out.Available)
	httpx.WriteJSON(w, http.StatusOK, out)
}

func providerCatalog(configured []string, offered []model.AvailableConnection) []model.ConnectionProvider {
	out := make([]model.ConnectionProvider, 0, 2)
	for _, provider := range []string{"garmin", "suunto"} {
		state := "partner_onboarding"
		if slices.Contains(configured, provider) {
			state = "unavailable"
		}
		for _, option := range offered {
			if option.Provider != provider {
				continue
			}
			state = "full"
			if option.Accepting {
				state = "available"
			}
			break
		}
		out = append(out, model.ConnectionProvider{Provider: provider, Availability: state})
	}
	return out
}

// ---- POST /v1/me/connections/{provider}/start ---------------------------------------------------------------------

func (c *connectAPI) start(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), userOf(r)
	name := providerParam(r)
	if !c.consentOK(w, r) {
		metrics.ConnectStarts.WithLabelValues(c.label(name), "consent_required").Inc()
		return
	}
	prov, ok, err := c.served(ctx, name, u.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if name == "" || !ok {
		c.unknownProvider(w, r, name, "unknown_provider")
		return
	}
	state, verifier := connect.NewState(), connect.NewVerifier()
	hash := connect.Hash(state)
	enc, err := c.seal(verifier, adState(hash))
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("seal verifier: %w", err))
		return
	}
	expires := c.now().Add(store.StateTTL).UTC().Truncate(time.Second)
	err = c.st.StartConnect(ctx, u.ID, name, c.cfg.Caps[name], store.NewState{Hash: hash, VerifierEnc: enc, PolicyVersion: c.beta.PrivacyPolicyVersion, Expires: expires})
	switch {
	case errors.Is(err, store.ErrCapFull):
		metrics.ConnectStarts.WithLabelValues(name, "connection_limit").Inc()
		httpx.WriteCodedError(w, r, http.StatusConflict, model.CodeConnectionLimit, "this provider has no room for another connection right now")
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	}
	authorize, err := prov.AuthorizeURL(state, connect.Challenge(verifier), c.redirectURI(name))
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("authorize url: %w", err))
		return
	}
	metrics.ConnectStarts.WithLabelValues(name, "ok").Inc()
	c.log.InfoContext(ctx, "connect start", "provider", name)
	httpx.WriteJSON(w, http.StatusOK, model.ConnectStart{AuthorizeURL: authorize, ExpiresAt: expires})
}

// ---- GET /v1/connect/{provider}/callback ---------------------------------------------------------------------------

// callbackReasons are the closed reasons of the "connect callback" line.
const (
	reasonOK, reasonDenied, reasonMissingPerm = "ok", "access_denied", "missing_permission"
	reasonStateUnknown, reasonStateExpired    = "state_unknown", "state_expired"
	reasonExchange, reasonNoAccount           = "exchange_failed", "no_account"
	reasonNoConsent, reasonCap, reasonUnknown = "no_consent", "cap", "provider_unknown"
)

// callbackOut redirects the browser to the app: ryft://connect/<provider>?status=<s>[&ticket=<t>]. No body, no
// token, code, provider user id or health value in the URL; the provider is the path's when it is well-formed.
func callbackOut(w http.ResponseWriter, r *http.Request, provider, status, ticket string) {
	if !connect.ValidProvider(provider) {
		provider = connect.ReservedProvider
	}
	q := url.Values{"status": {status}}
	if ticket != "" {
		q.Set("ticket", ticket)
	}
	h := w.Header()
	h.Set("Location", "ryft://connect/"+provider+"?"+q.Encode())
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusFound)
}

// callback is the provider's redirect target. It takes no bearer and no app token (the browser, not the app,
// calls it), so it never answers 401 or 403, and it always answers 302. Single-use state, PKCE, then the exchange;
// the grant waits in connection_pending until the app confirms with the bearer of the account that started the
// flow. No state, code or ticket is logged, and the access line carries no query.
func (c *connectAPI) callback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("provider")
	status, reason, ticket := model.CallbackError, reasonUnknown, ""
	defer func() {
		metrics.ConnectCallbacks.WithLabelValues(c.label(name), status).Inc()
		c.log.InfoContext(ctx, "connect callback", "provider", c.label(name), "status", status, "reason", reason)
		callbackOut(w, r, name, status, ticket)
	}()
	prov, ok := c.reg.Get(name)
	q := r.URL.Query()
	state := q.Get("state")
	if !ok || !connect.ValidState(state) {
		if ok {
			status, reason = model.CallbackExpired, reasonStateUnknown
		}
		return
	}
	hash := connect.Hash(state)
	flow, err := c.st.ConsumeState(ctx, hash, name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		status, reason = model.CallbackExpired, reasonStateUnknown
		return
	case err != nil:
		reason = reasonExchange
		c.log.ErrorContext(ctx, "connect callback state", "err", err)
		return
	case !c.now().Before(flow.Expires):
		status, reason = model.CallbackExpired, reasonStateExpired
		return
	}
	if f := httpx.Info(ctx); f != nil {
		f.UserID = flow.UserID.String()
	}
	if q.Get("error") != "" { // the user declined at the provider
		if q.Get("error") == "access_denied" {
			status, reason = model.CallbackDenied, reasonDenied
		}
		return
	}
	user, _, err := c.st.GetUser(ctx, flow.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		reason = reasonNoAccount
		return
	case err != nil:
		reason = reasonExchange
		c.log.ErrorContext(ctx, "connect callback user", "err", err)
		return
	case c.beta.Consent != config.ModeOff && !gate.Consented(c.beta, user):
		reason = reasonNoConsent
		return
	}
	if _, enabled, err := c.served(ctx, name, flow.UserID); err != nil || !enabled {
		return // reasonUnknown
	}
	code := q.Get("code")
	if code == "" || len(code) > maxCodeLen {
		reason = reasonExchange
		return
	}
	verifier, err := c.open(flow.VerifierEnc, adState(hash))
	if err != nil {
		reason = reasonExchange
		return
	}
	xctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	g, err := prov.Exchange(xctx, code, verifier, c.redirectURI(name))
	switch {
	case errors.Is(err, connect.ErrDenied):
		status, reason = model.CallbackDenied, reasonDenied
		return
	case err != nil || g.ProviderUserID == "" || len(g.ProviderUserID) > 128:
		reason = reasonExchange
		return
	}
	access, err1 := c.seal(g.Access, adPendingAccess(name, flow.UserID))
	refresh, err2 := c.seal(g.Refresh, adPendingRefresh(name, flow.UserID))
	if err1 != nil || err2 != nil || access == nil {
		reason = reasonExchange
		return
	}
	perms := g.Permissions
	if len(perms) > model.MaxProviderPermissions {
		perms = perms[:model.MaxProviderPermissions]
	}
	if missing := slices.ContainsFunc(prov.Required(), func(p string) bool { return !slices.Contains(g.Permissions, p) }); missing {
		// The grant lacks what RYFT needs: handed back (B2's rule decides at the attempt whether the provider is called).
		c.handBackAs(ctx, flow.UserID, name, g.ProviderUserID, "missing_permission", access, refresh)
		status, reason = model.CallbackDenied, reasonMissingPerm
		return
	}
	next := connect.NewTicket()
	err = c.st.PutPendingCapped(ctx, store.Pending{UserID: flow.UserID, Provider: name, ProviderUserID: g.ProviderUserID, Permissions: perms,
		AccessEnc: access, RefreshEnc: refresh, AccessExpires: g.AccessExpires, RefreshDue: g.RefreshDue,
		ConfirmHash: connect.Hash(next), Expires: c.now().Add(store.StateTTL)}, c.cfg.Caps[name])
	switch {
	case errors.Is(err, store.ErrCapFull):
		reason = reasonCap
		c.handBackAs(ctx, flow.UserID, name, g.ProviderUserID, "cap", access, refresh)
		return
	case errors.Is(err, store.ErrConnectNoAccount):
		reason = reasonNoAccount
		c.handBack(ctx, flow.UserID, name, g.ProviderUserID, access, refresh)
		return
	case err != nil:
		reason = reasonExchange
		c.log.ErrorContext(ctx, "connect callback store", "err", err)
		c.handBack(ctx, flow.UserID, name, g.ProviderUserID, access, refresh)
		return
	}
	status, reason, ticket = model.CallbackOK, reasonOK, next
}

// handBack queues a grant the exchange made but nothing holds (the pending row was not stored: the account is gone or
// the database failed), so the provider is asked to end it (P2-15). The live-grant rule decides at the attempt.
func (c *connectAPI) handBack(ctx context.Context, user uuid.UUID, provider, puid string, access, refresh []byte) {
	c.handBackAs(ctx, user, provider, puid, "confirm_expired", access, refresh)
}

// handBackAs is handBack under a reason, on a context of its own: the browser leaving does not lose the grant (P2-29).
func (c *connectAPI) handBackAs(ctx context.Context, user uuid.UUID, provider, puid, reason string, access, refresh []byte) {
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.persist)
	defer cancel()
	if _, err := c.st.QueueDeregistration(qctx, user, provider, puid, reason, access, refresh); err != nil {
		c.log.ErrorContext(ctx, "connect callback queue", "provider", provider, "err", err)
	}
}

// ---- POST /v1/me/connections/{provider}/confirm ---------------------------------------------------------------------

func (c *connectAPI) confirm(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), userOf(r)
	name := providerParam(r)
	var body model.ConnectConfirmPost
	if err := httpx.ReadJSON(w, r, &body, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if body.Ticket == "" {
		httpx.WriteError(w, http.StatusBadRequest, "ticket is required")
		return
	}
	if !c.consentOK(w, r) {
		return
	}
	prov, ok, err := c.served(ctx, name, u.ID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if name == "" || !ok {
		// Not served, or the flag is off for the account: the grant waiting for this confirm is handled like an
		// expired one (queued; deregistered unless a live connection holds that provider account, B2).
		if name != "" {
			if ids, derr := c.st.DropPending(ctx, u.ID, name, "confirm_expired"); derr != nil {
				c.log.ErrorContext(ctx, "connect drop pending", "err", derr)
			} else {
				c.attempt(ctx, ids)
			}
		}
		c.unknownProvider(w, r, name, "")
		return
	}
	if !connect.ValidTicket(body.Ticket) {
		metrics.ConnectConfirms.WithLabelValues(name, "invalid").Inc()
		httpx.WriteCodedError(w, r, http.StatusConflict, model.CodeConnectTicketInvalid, "the ticket is not valid for this account")
		return
	}
	_ = prov
	out, err := c.st.ConfirmConnection(ctx, store.ConfirmIn{UserID: u.ID, Provider: name, TicketHash: connect.Hash(body.Ticket),
		Cap: c.cfg.Caps[name], PolicyVersion: c.beta.PrivacyPolicyVersion,
		Reseal: func(accessEnc, refreshEnc []byte) ([]byte, []byte, error) {
			a, err := c.open(accessEnc, adPendingAccess(name, u.ID))
			if err != nil {
				return nil, nil, err
			}
			rf, err := c.open(refreshEnc, adPendingRefresh(name, u.ID))
			if err != nil {
				return nil, nil, err
			}
			sa, err := c.seal(a, adAccess(name, u.ID))
			if err != nil {
				return nil, nil, err
			}
			sr, err := c.seal(rf, adRefresh(name, u.ID))
			return sa, sr, err
		},
		Emit: c.emitDisconnect(ctx)})
	switch {
	case errors.Is(err, store.ErrTicketInvalid):
		metrics.ConnectConfirms.WithLabelValues(name, "invalid").Inc()
		httpx.WriteCodedError(w, r, http.StatusConflict, model.CodeConnectTicketInvalid, "the ticket is not valid for this account")
		return
	case errors.Is(err, store.ErrConfirmExpired):
		metrics.ConnectConfirms.WithLabelValues(name, "expired").Inc()
		c.attemptUser(ctx, u.ID, name)
		httpx.WriteCodedError(w, r, http.StatusGone, model.CodeConnectExpired, "the confirm came too late: start again")
		return
	case errors.Is(err, store.ErrCapFull):
		metrics.ConnectConfirms.WithLabelValues(name, "connection_limit").Inc()
		httpx.WriteCodedError(w, r, http.StatusConflict, model.CodeConnectionLimit, "this provider has no room for another connection right now")
		return
	case err != nil:
		httpx.Fail(w, r, err)
		return
	}
	c.flush(ctx) // the event of a connection the swap ended, before the answer (D219)
	c.attempt(ctx, out.Queued)
	metrics.ConnectConfirms.WithLabelValues(name, out.Outcome).Inc()
	c.log.InfoContext(ctx, "connect confirm", "provider", name, "outcome", out.Outcome)
	httpx.WriteJSON(w, http.StatusOK, c.toModel(out.Connection))
}

// emitDisconnect writes the ingested event (reason disconnect) of a connection's deleted data, in the transaction
// that deleted it.
func (c *connectAPI) emitDisconnect(ctx context.Context) func(tx pgx.Tx, user uuid.UUID, days []string) error {
	return func(tx pgx.Tx, user uuid.UUID, days []string) error {
		m, err := events.Ingested(model.IngestedEvent{UserID: user, Days: days, Reason: model.ReasonDisconnect, RequestID: reqIDOf(ctx)}, "")
		if err != nil {
			return err
		}
		return c.insert(ctx, tx, m)
	}
}

func reqIDOf(ctx context.Context) string {
	if f := httpx.Info(ctx); f != nil {
		return f.RequestID
	}
	return ""
}

// ---- DELETE /v1/me/connections/{provider} -------------------------------------------------------------------------

// disconnect ends the connection and deletes what it delivered, then asks the provider once (5 s at most). It works
// for any well-formed provider id, served or not, enabled or not: a connection can always be ended.
func (c *connectAPI) disconnect(w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), userOf(r)
	name := providerParam(r)
	if name == "" {
		httpx.NotFound(w, r)
		return
	}
	out, err := c.st.Disconnect(ctx, u.ID, name, c.emitDisconnect(ctx))
	if err != nil {
		w.Header().Set("Retry-After", "5")
		httpx.Fail(w, r, err)
		return
	}
	c.flush(ctx)
	res := model.ConnectionDeleted{Status: "disconnected", DataDeleted: true}
	if len(out.Queued) > 0 {
		done := c.attempt(ctx, out.Queued)
		res.Deregistered = &done
	}
	c.log.InfoContext(ctx, "connect disconnect", "provider", c.label(name), "existed", out.Existed)
	httpx.WriteJSON(w, http.StatusOK, res)
}

// attempt makes one bounded try (attemptTimeout in all) at each queue row and says whether every grant is ended: the
// provider ended it or no longer knew it. A grant not called because another RYFT account holds that provider account
// live is not ended, so it is false (API.md; P2-13). A row that fails stays in the queue for the tick.
func (c *connectAPI) attempt(ctx context.Context, ids []int64) bool {
	if len(ids) == 0 {
		return true
	}
	ctx, cancel := httpx.Detach(ctx, attemptTimeout)
	defer cancel()
	all := true
	for _, id := range ids {
		d, err := c.st.Deregistration(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			all = false
			continue
		}
		if !ended(c.deregisterOne(ctx, d)) {
			all = false
		}
	}
	return all
}

// attemptUser is attempt over the account's rows for one provider (the rows a late confirm just queued).
func (c *connectAPI) attemptUser(ctx context.Context, user uuid.UUID, provider string) {
	rows, err := c.st.UserDeregistrations(ctx, user)
	if err != nil {
		return
	}
	var ids []int64
	for _, d := range rows {
		if d.Provider == provider {
			ids = append(ids, d.ID)
		}
	}
	c.attempt(ctx, ids)
}

// endAccount is DELETE /v1/me's one bounded attempt over the account's queue rows (5 s in all): nil when the account
// had no connection to end, else whether every grant is ended.
func (c *connectAPI) endAccount(ctx context.Context, user uuid.UUID) *bool {
	rows, err := c.st.UserDeregistrations(ctx, user)
	if err != nil {
		c.log.WarnContext(ctx, "account deletion: connection queue", "err", err)
		f := false
		return &f
	}
	if len(rows) == 0 {
		return nil
	}
	ids := make([]int64, len(rows))
	for i, d := range rows {
		ids[i] = d.ID
	}
	ok := c.attempt(ctx, ids)
	return &ok
}

// ---- the deregistration, one row -----------------------------------------------------------------------------------

// The outcomes of deregisterOne (ryft_connect_deregistrations_total).
const (
	depOK, depGone, depRetry, depGivenUp, depSkipped = "ok", "gone", "retry", "given_up", "skipped_live"
)

// ended says whether an outcome means the provider's grant is ended: ended now, or already gone. A grant skipped
// because another account holds the provider account live is not (the queue row is gone, the grant is not).
func ended(outcome string) bool { return outcome == depOK || outcome == depGone }

// backoff is the wait after the nth failed attempt: 15 minutes doubled each time, at most a day.
func backoff(attempts int) time.Duration {
	if attempts >= 7 { // 15 min * 2^7 is already past a day
		return backoffMax
	}
	return min(backoffMax, backoffBase<<attempts)
}

// deregisterOne ends one queued grant at the provider. Every deregistration, inline or in the tick, goes through
// it (review B2): before the provider is called it asks, under the provider's cap lock (the confirm's), whether any
// account holds (provider, provider user id) as connected or needs_reauth, or has a grant for it waiting for its
// confirm (P2-6). If one does, nothing is called: the row and its tokens are
// deleted. That covers an attacker's own authorize URL completed by a connected victim and left to expire
// (reason confirm_expired), the cap and missing_permission reasons, the user's own unconfirmed reconnect and a link
// that went live again between queueing and the attempt.
func (c *connectAPI) deregisterOne(ctx context.Context, d store.Dereg) string {
	count := func(outcome string) string {
		metrics.ConnectDeregistrations.WithLabelValues(c.label(d.Provider), d.Reason, outcome).Inc()
		return outcome
	}
	fail := func(code string) string {
		if err := c.st.RetryDeregistration(ctx, d.ID, code, c.now().Add(backoff(d.Attempts))); err != nil {
			c.log.WarnContext(ctx, "connect deregistration", "provider", c.label(d.Provider), "err", err)
		}
		return count(depRetry)
	}
	held, err := c.st.HoldsLiveGrant(ctx, d.Provider, d.ProviderUserID)
	if err != nil {
		return fail("database")
	}
	if held {
		if err := c.st.FinishDeregistration(ctx, d.ID); err != nil {
			return fail("database")
		}
		return count(depSkipped)
	}
	prov, ok := c.reg.Get(d.Provider)
	if !ok {
		return fail("provider_off")
	}
	access, err1 := c.open(d.AccessEnc, adAccess(d.Provider, d.UserID), adPendingAccess(d.Provider, d.UserID))
	refresh, err2 := c.open(d.RefreshEnc, adRefresh(d.Provider, d.UserID), adPendingRefresh(d.Provider, d.UserID))
	if err1 != nil || err2 != nil {
		c.log.ErrorContext(ctx, "connect deregistration: tokens do not open (wrong DATA_ENCRYPTION_KEY?)", "provider", c.label(d.Provider), "reason", d.Reason)
		return fail("undecryptable")
	}
	pctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	g := connect.Grant{ProviderUserID: d.ProviderUserID, Access: access, Refresh: refresh}
	err = prov.Deregister(pctx, g)
	if errors.Is(err, connect.ErrInvalidGrant) && refresh != "" {
		// The access token has expired: refresh in memory (the new tokens are not kept: the grant ends now). The
		// refresh may rotate the token, and a call cut after that would read the next attempt's invalid_grant as "gone"
		// while the grant lives. Where the step has the time (the tick), the refresh and the deregistration after it run
		// past the step's cancel, on a context of their own (P2-24). The inline attempt (5 s in all, API.md) keeps its
		// bound: its calls take a second or so, and a cut there is the same risk the first call already had.
		rctx, rcancel := pctx, func() {}
		if c.roomForRefresh(ctx) {
			rctx, rcancel = context.WithTimeout(context.WithoutCancel(ctx), providerTimeout)
		}
		defer rcancel()
		var ng connect.Grant
		if ng, err = prov.Refresh(rctx, refresh); err == nil {
			ng.ProviderUserID = d.ProviderUserID
			err = prov.Deregister(rctx, ng)
		}
	}
	// Once the provider has answered, the row is finished on a context of its own: a step cancelled meanwhile does
	// not leave an ended grant in the queue.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), c.persist)
	defer fcancel()
	switch {
	case err == nil:
		if ferr := c.st.FinishDeregistration(fctx, d.ID); ferr != nil {
			return fail("database")
		}
		return count(depOK)
	case errors.Is(err, connect.ErrGone), errors.Is(err, connect.ErrInvalidGrant):
		if ferr := c.st.FinishDeregistration(fctx, d.ID); ferr != nil {
			return fail("database")
		}
		return count(depGone)
	}
	return fail("unreachable")
}

// ---- the tick ---------------------------------------------------------------------------------------------------

// ticks are the api's steps for connections, in order (design 6.3). The steps that only use the database come first:
// the daily notices (gauges) and the purge must not sit behind a provider that does not answer. Each runs under a
// deadline of its own (P2-4); the tick's own budget still bounds them all.
func (c *connectAPI) ticks() []eventbus.Tick {
	return []eventbus.Tick{
		{Name: "connect purge", Run: c.within(&c.dbBudget, c.purgeStep)},
		{Name: "connect gauges", Run: c.within(&c.dbBudget, c.gaugesStep)},
		{Name: "connect refresh", Run: c.within(&c.providerBudget, c.refreshStep)},
		{Name: "connect deregister", Run: c.within(&c.providerBudget, c.deregisterStep)},
	}
}

// within runs step under its own deadline. A step that ran out of it says so; the next step starts on the tick's
// remaining time.
func (c *connectAPI) within(budget *time.Duration, step func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		sctx, cancel := context.WithTimeout(ctx, *budget)
		defer cancel()
		err := step(sctx)
		if err != nil && sctx.Err() != nil && ctx.Err() == nil {
			return fmt.Errorf("the step ran out of its %s, the next tick goes on: %w", *budget, sctx.Err())
		}
		return err
	}
}

// refreshStep refreshes up to 20 due connections, oldest first. A refusal of the grant makes the link needs_reauth
// (tokens kept); a transient failure backs off. Processing a push never waits for this.
func (c *connectAPI) refreshStep(ctx context.Context) error {
	due, err := c.st.ClaimRefreshes(ctx, refreshBatch)
	if err != nil {
		return err
	}
	for _, d := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !c.roomForRefresh(ctx) {
			// The rest were claimed ahead (ten minutes) and come again; a refresh the step cannot finish is not begun.
			return fmt.Errorf("less than a refresh call and its save left in the step: %w", context.DeadlineExceeded)
		}
		c.refreshOne(ctx, d)
	}
	return nil
}

// roomForRefresh says whether ctx has at least one provider call and one save left (no deadline: yes). The refresh
// call runs past the step's cancel, so it must be started only where it ends inside the step.
func (c *connectAPI) roomForRefresh(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) >= c.callBudget+c.persist
}

func (c *connectAPI) refreshOne(ctx context.Context, d store.RefreshDue) {
	prov, ok := c.reg.Get(d.Provider)
	if !ok {
		// Not served now: look again tomorrow.
		_ = c.st.RefreshFailed(ctx, d.UserID, d.Provider, "provider_off", d.Attempts, c.now().Add(backoffMax))
		return
	}
	refresh, err := c.open(d.RefreshEnc, adRefresh(d.Provider, d.UserID))
	if err != nil {
		c.log.ErrorContext(ctx, "connect refresh: tokens do not open (wrong DATA_ENCRYPTION_KEY?)", "provider", c.label(d.Provider))
		_ = c.st.RefreshFailed(ctx, d.UserID, d.Provider, "undecryptable", d.Attempts+1, c.now().Add(backoff(d.Attempts+1)))
		metrics.ConnectRefreshes.WithLabelValues(d.Provider, "error").Inc()
		return
	}
	// The call runs past the step's deadline and cancel: the provider may rotate the token while it is answering, and
	// a call cut then leaves the new token nowhere (P2-24). roomForRefresh made sure it fits the step anyway.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.callBudget)
	defer cancel()
	g, err := prov.Refresh(pctx, refresh)
	// What the answer decides is written on a context of its own too: a refusal of the grant is final.
	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), c.persist)
	defer scancel()
	switch {
	case errors.Is(err, connect.ErrInvalidGrant):
		if err := c.st.NeedsReauth(sctx, d.UserID, d.Provider, "invalid_grant"); err != nil {
			c.log.WarnContext(ctx, "connect refresh", "provider", c.label(d.Provider), "err", err)
		}
		metrics.ConnectRefreshes.WithLabelValues(d.Provider, "needs_reauth").Inc()
		return
	case err != nil:
		_ = c.st.RefreshFailed(sctx, d.UserID, d.Provider, "unreachable", d.Attempts+1, c.now().Add(backoff(d.Attempts+1)))
		metrics.ConnectRefreshes.WithLabelValues(d.Provider, "error").Inc()
		return
	}
	access, err1 := c.seal(g.Access, adAccess(d.Provider, d.UserID))
	newRefresh, err2 := c.seal(g.Refresh, adRefresh(d.Provider, d.UserID)) // nil keeps the stored one
	if err1 != nil || err2 != nil || access == nil {
		_ = c.st.RefreshFailed(sctx, d.UserID, d.Provider, "seal", d.Attempts+1, c.now().Add(backoff(d.Attempts+1)))
		metrics.ConnectRefreshes.WithLabelValues(d.Provider, "error").Inc()
		return
	}
	// The provider has rotated the tokens: save them even when the step's deadline or the tick's has passed. Only if the
	// row still holds the refresh token this refresh started from (P2-25).
	saved, err := c.st.SaveRefresh(sctx, d.UserID, d.Provider, d.RefreshEnc, access, newRefresh, g.AccessExpires, g.RefreshDue)
	if err != nil {
		c.log.ErrorContext(ctx, "connect refresh: the refreshed grant was not saved", "provider", c.label(d.Provider), "err", err)
		metrics.ConnectRefreshes.WithLabelValues(d.Provider, "error").Inc()
		return
	}
	if !saved {
		// A disconnect, a replace or a relink ran while the provider answered: the account's tokens are gone or are
		// another grant's, and these are the ones the provider holds now (the old ones may have been rotated away).
		// Queue them, so the grant is ended (P2-19, P2-25); the refresh token the provider kept is the one this refresh
		// started from when it sent none.
		if newRefresh == nil {
			newRefresh = d.RefreshEnc
		}
		if d.ProviderUserID != "" {
			if _, err := c.st.QueueDeregistration(sctx, d.UserID, d.Provider, d.ProviderUserID, "disconnect", access, newRefresh); err != nil {
				c.log.ErrorContext(ctx, "connect refresh: the grant of a disconnected link was not queued", "provider", c.label(d.Provider), "err", err)
			}
		}
		metrics.ConnectRefreshes.WithLabelValues(d.Provider, "error").Inc()
		return
	}
	metrics.ConnectRefreshes.WithLabelValues(d.Provider, "ok").Inc()
}

// deregisterStep gives up on the queue rows older than 30 days (the database, so first), then works up to 20 due
// rows.
func (c *connectAPI) deregisterStep(ctx context.Context) error {
	gone, giveUpErr := c.st.GiveUpDeregistrations(ctx)
	for _, g := range gone {
		metrics.ConnectDeregistrations.WithLabelValues(c.label(g.Provider), g.Reason, depGivenUp).Inc()
	}
	if len(gone) > 0 {
		c.log.WarnContext(ctx, "connect deregistrations given up", "rows", len(gone))
	}
	rows, err := c.st.ClaimDeregistrations(ctx, deregisterBatch)
	if err != nil {
		return errors.Join(giveUpErr, err)
	}
	for _, d := range rows {
		if ctx.Err() != nil {
			return errors.Join(giveUpErr, ctx.Err())
		}
		c.deregisterOne(ctx, d)
	}
	return giveUpErr
}

// purgeStep deletes what expired: states, pending grants (an unconfirmed one's tokens queued first), the tokens of
// revoked connections and revoked entries after 90 days.
func (c *connectAPI) purgeStep(ctx context.Context) error {
	p, err := c.st.PurgeConnect(ctx)
	if err != nil {
		return err
	}
	if p.Pending+p.Tokens+p.Revoked > 0 {
		c.log.InfoContext(ctx, "connect purged", "pending", p.Pending, "tokens", p.Tokens, "revoked", p.Revoked)
	}
	return nil
}

// noticeSlot is true for the tick that starts 06:00 to 06:14 UTC: the day's one tick that writes the notices
// (Cloud Monitoring policy 6 then notifies once a day per kind, not every 15 minutes).
func noticeSlot(t time.Time) bool {
	t = t.UTC()
	return t.Hour() == 6 && t.Minute() <= 14
}

// gaugesStep sets the connection gauges and, once a day, writes the notices whose condition holds: a provider at
// 80 % of its cap, connected links with no data for 36 hours, a deregistration stuck for a day.
func (c *connectAPI) gaugesStep(ctx context.Context) error {
	counts, err := c.st.ConnectionCounts(ctx)
	if err != nil {
		return err
	}
	notice := noticeSlot(c.now())
	names := c.reg.Names()
	for p := range counts {
		if !slices.Contains(names, p) {
			names = append(names, p)
		}
	}
	var errs []error
	for _, p := range names {
		for _, s := range []string{model.ConnectionConnected, model.ConnectionNeedsReauth, model.ConnectionRevoked} {
			metrics.ConnectConnections.WithLabelValues(p, s).Set(float64(counts[p][s]))
		}
		stale, err := c.st.StaleConnections(ctx, p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		metrics.ConnectStale.WithLabelValues(p).Set(float64(stale))
		if notice && stale > 0 {
			c.log.WarnContext(ctx, "connect no data", "provider", p, "connections", stale)
		}
		if _, ok := c.reg.Get(p); !ok {
			continue
		}
		load, err := c.st.ProviderLoad(ctx, p, uuid.Nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		cap := c.cfg.Caps[p]
		ratio := 1.0
		if cap > 0 {
			ratio = float64(load) / float64(cap)
		}
		metrics.ConnectCapacity.WithLabelValues(p).Set(ratio)
		if notice && ratio >= capacityNoticeAt {
			c.log.WarnContext(ctx, "connect capacity", "provider", p, "counted", load, "cap", cap)
		}
	}
	age, err := c.st.OldestDeregistration(ctx)
	if err != nil {
		errs = append(errs, err)
	} else if notice && age > stuckAfter {
		c.log.WarnContext(ctx, "connect deregistration stuck", "hours", int(age/time.Hour))
	}
	return errors.Join(errs...)
}
