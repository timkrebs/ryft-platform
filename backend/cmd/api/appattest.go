package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/appattest"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
	"github.com/timkrebs/ryft/internal/store"
)

// App Attest, the server (D245, D247; docs/API.md → Conventions → App Attest): the three
// /v1/app-attest routes, the routes the token check exempts, and the tick's purge. The token check
// itself is gate.AppAttest, around /v1/ and the import.

// attestExemptRoutes are the /v1/ chain's routes that never need an app token (decision 12; API.md
// → Conventions → App Attest → Exempt), by method and path. DELETE /v1/me and /v1/admin/* sit on
// muxes of their own that App Attest never wraps; DELETE /v1/devices/{id}/push is matched in
// attestExempt.
var attestExemptRoutes = map[string]bool{
	"POST /v1/app-attest/challenge": true, "POST /v1/app-attest/keys": true, "POST /v1/app-attest/token": true,
	"POST /v1/me/apple-authorization": true, "DELETE /v1/me/consent": true, "POST /v1/feedback": true,
}

// attestExempt says whether r is one of the exempt routes of the /v1/ chain.
func attestExempt(r *http.Request) bool {
	if attestExemptRoutes[r.Method+" "+r.URL.Path] {
		return true
	}
	if r.Method != http.MethodDelete {
		return false
	}
	if isDisconnect(r) { // a connection can always be ended (D257)
		return true
	}
	// DELETE /v1/devices/{id}/push: one non-empty segment between the two, as the mux matches it.
	id, ok := strings.CutPrefix(r.URL.Path, "/v1/devices/")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, "/push")
	return ok && id != "" && !strings.Contains(id, "/")
}

// appAttestGate is gate.AppAttest for the api's /v1/ chain and the import: the exempt routes above
// and APP_ATTEST_EXEMPT_USERS.
func appAttestGate(cfg config.AppAttest, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return gate.AppAttest(gate.AppAttestOptions{Config: cfg, Service: "api", Exempt: attestExempt, Log: log}, next)
	}
}

// attestStore is what the routes keep in the database (*store.Store; a fake in tests).
type attestStore interface {
	CreateAttestChallenge(ctx context.Context, user uuid.UUID, hash [32]byte, expires time.Time) error
	ConsumeAttestChallenge(ctx context.Context, user uuid.UUID, hash [32]byte, now time.Time) (bool, error)
	AttestKey(ctx context.Context, user uuid.UUID, keyID [32]byte) (store.AppAttestKey, error)
	AttestationsSince(ctx context.Context, user uuid.UUID, since time.Time) (int, time.Time, error)
	PutAttestKey(ctx context.Context, k store.AppAttestKey) error
	AdvanceAttestCounter(ctx context.Context, user uuid.UUID, keyID [32]byte, counter uint32, now time.Time) (bool, error)
}

// attestVerifier checks attestations and assertions (*appattest.Verifier; one on a test root in tests).
type attestVerifier interface {
	Attestation(att []byte, keyID, clientDataHash [32]byte) (appattest.Attested, error)
	Assertion(assertion, publicKey []byte, clientDataHash [32]byte, lastCounter uint32) (uint32, error)
}

// The routes' limits (API.md → Answers of the three routes): challenge and token per account, bursts
// of 10, then one a minute, each its own budget, in memory per instance; keys 10 per account in 24
// hours, counted in the database.
const (
	attestBurst      = 10
	attestPerSecond  = 1.0 / 60
	attestKeysWindow = 24 * time.Hour
	attestKeysMax    = 10
	// The longest attestation and assertion, in characters of base64 (API.md: 32 KiB and 1 KiB of text).
	maxAttestationLen = 32 << 10
	maxAssertionLen   = 1 << 10
)

// The answers' fixed texts (AA4): none holds a value of the request.
const (
	msgAttestNotSetUp       = "App Attest is not set up on this server"
	msgAttestBody           = "the body must be the route's JSON object with its fields only"
	msgAttestChallengeField = "challenge is required"
	msgAttestKeyIDField     = "key_id must be 44 characters of standard base64 for 32 bytes"
	msgAttestationField     = "attestation must be standard base64 of at most 32768 characters"
	msgAssertionField       = "assertion must be standard base64 of at most 1024 characters"
	msgAttestChallenge      = "the challenge is unknown, used up or expired: get a new one"
	msgAttestInvalid        = "the attestation or assertion did not pass the checks"
	msgAttestKeyUnknown     = "key_id is not one of this account's keys"
	msgAttestTooMany        = "too many requests to this route for this account"
)

// appAttest serves the three routes. Without a ring (APP_ATTEST_TOKEN_KEYS unset) it is nil and every
// route answers 501.
type appAttest struct {
	st                 attestStore
	verifier           attestVerifier
	ring               *appattest.Ring
	log                *slog.Logger
	now                func() time.Time
	challenges, tokens *ratelimit.Limiter
}

// newAppAttest is the routes on st, v and ring at the clock now (nil: time.Now).
func newAppAttest(st attestStore, v attestVerifier, ring *appattest.Ring, log *slog.Logger, now func() time.Time) *appAttest {
	if now == nil {
		now = time.Now
	}
	return &appAttest{st: st, verifier: v, ring: ring, log: log, now: now,
		challenges: ratelimit.New(attestPerSecond, attestBurst, ratelimit.Clock(now)),
		tokens:     ratelimit.New(attestPerSecond, attestBurst, ratelimit.Clock(now))}
}

// served answers 501 and returns false when the server has no ring (D247: until the warn PR pins it).
func (a *appAttest) served(w http.ResponseWriter) bool {
	if a == nil || a.ring == nil || a.st == nil || a.verifier == nil {
		httpx.WriteError(w, http.StatusNotImplemented, msgAttestNotSetUp)
		return false
	}
	return true
}

// allow takes a token from the account's bucket of l, or answers 429 with Retry-After.
func (a *appAttest) allow(w http.ResponseWriter, l *ratelimit.Limiter, user uuid.UUID) bool {
	if ok, wait := l.Allow(user.String()); !ok {
		tooMany(w, wait)
		return false
	}
	return true
}

// tooMany is the routes' 429: Retry-After in whole seconds, at least 1.
func tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	httpx.WriteError(w, http.StatusTooManyRequests, msgAttestTooMany)
}

// attestBodyError answers a body that did not decode: 413 and the NUL's 422 as on every route (their
// texts hold no value), any other failure one fixed 400 (httpx.WriteDecodeError's would quote the
// decoder, which can name a field or a character of the request).
func attestBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || errors.Is(err, httpx.ErrNUL) {
		httpx.WriteDecodeError(w, err)
		return
	}
	httpx.WriteError(w, http.StatusBadRequest, msgAttestBody)
}

// challenge is POST /v1/app-attest/challenge: an empty body or {}; 200 AppAttestChallenge.
func (a *appAttest) challenge(w http.ResponseWriter, r *http.Request) {
	if !a.served(w) {
		return
	}
	user := userOf(r).ID
	if !a.allow(w, a.challenges, user) {
		return
	}
	var empty struct{}
	if err := httpx.ReadJSON(w, r, &empty, httpx.MaxBodyDefault); err != nil && !errors.Is(err, io.EOF) {
		attestBodyError(w, err)
		return
	}
	c, err := appattest.NewChallenge()
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	expires := a.now().Add(appattest.ChallengeTTL).UTC().Truncate(time.Second)
	if err := a.st.CreateAttestChallenge(r.Context(), user, appattest.ClientDataHash(c), expires); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, model.AppAttestChallenge{Challenge: c, ExpiresAt: expires})
}

// keys is POST /v1/app-attest/keys: the account's budget, the challenge (used up whatever the answer),
// the body's fields, then the attestation's seven checks; 201 AppAttestKey.
func (a *appAttest) keys(w http.ResponseWriter, r *http.Request) {
	if !a.served(w) {
		return
	}
	ctx, user, now := r.Context(), userOf(r).ID, a.now()
	n, oldest, err := a.st.AttestationsSince(ctx, user, now.Add(-attestKeysWindow))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if n >= attestKeysMax {
		tooMany(w, oldest.Add(attestKeysWindow).Sub(now))
		return
	}
	var p model.AppAttestKeyPost
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		attestBodyError(w, err)
		return
	}
	fresh, cdh, ok := a.consume(w, r, user, p.Challenge, now)
	if !ok {
		return
	}
	keyID, ok := appattest.ParseKeyID(p.KeyID)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, msgAttestKeyIDField)
		return
	}
	att, ok := appattest.DecodeBlob(p.Attestation, maxAttestationLen)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, msgAttestationField)
		return
	}
	if !fresh {
		a.log.InfoContext(ctx, "app attest challenge refused") // the request's route says which
		httpx.WriteCodedError(w, r, http.StatusUnprocessableEntity, model.CodeAppAttestChallengeInvalid, msgAttestChallenge)
		return
	}
	got, err := a.verifier.Attestation(att, keyID, cdh)
	if err != nil {
		a.log.InfoContext(ctx, "app attest attestation refused", "step", appattest.Step(err))
		httpx.WriteCodedError(w, r, http.StatusUnprocessableEntity, model.CodeAppAttestInvalid, msgAttestInvalid)
		return
	}
	if err := a.st.PutAttestKey(ctx, store.AppAttestKey{UserID: user, KeyID: keyID, PublicKey: got.PublicKey,
		Environment: got.Environment, AttestedAt: now}); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.log.InfoContext(ctx, "app attest key attested")
	httpx.WriteJSON(w, http.StatusCreated, model.AppAttestKey{KeyID: p.KeyID})
}

// token is POST /v1/app-attest/token: the account's budget, the challenge (used up whatever the
// answer), the body's fields, the key, then the assertion's checks and the counter's
// compare-and-set; 200 AppAttestToken.
func (a *appAttest) token(w http.ResponseWriter, r *http.Request) {
	if !a.served(w) {
		return
	}
	ctx, user, now := r.Context(), userOf(r).ID, a.now()
	if !a.allow(w, a.tokens, user) {
		return
	}
	var p model.AppAttestTokenPost
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		attestBodyError(w, err)
		return
	}
	fresh, cdh, ok := a.consume(w, r, user, p.Challenge, now)
	if !ok {
		return
	}
	keyID, ok := appattest.ParseKeyID(p.KeyID)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, msgAttestKeyIDField)
		return
	}
	assertion, ok := appattest.DecodeBlob(p.Assertion, maxAssertionLen)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, msgAssertionField)
		return
	}
	key, err := a.st.AttestKey(ctx, user, keyID)
	if errors.Is(err, store.ErrNotFound) {
		a.log.InfoContext(ctx, "app attest key unknown")
		httpx.WriteCodedError(w, r, http.StatusNotFound, model.CodeAppAttestKeyUnknown, msgAttestKeyUnknown)
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !fresh {
		a.log.InfoContext(ctx, "app attest challenge refused")
		httpx.WriteCodedError(w, r, http.StatusUnprocessableEntity, model.CodeAppAttestChallengeInvalid, msgAttestChallenge)
		return
	}
	counter, err := a.verifier.Assertion(assertion, key.PublicKey, cdh, key.Counter)
	if err != nil {
		a.log.InfoContext(ctx, "app attest assertion refused", "step", appattest.Step(err))
		httpx.WriteCodedError(w, r, http.StatusUnprocessableEntity, model.CodeAppAttestInvalid, msgAttestInvalid)
		return
	}
	advanced, err := a.st.AdvanceAttestCounter(ctx, user, keyID, counter, now)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !advanced {
		// Another renewal with this key stored a counter at least as high since the key was read.
		a.log.InfoContext(ctx, "app attest assertion refused", "step", "counter")
		httpx.WriteCodedError(w, r, http.StatusUnprocessableEntity, model.CodeAppAttestInvalid, msgAttestInvalid)
		return
	}
	tok, expires := a.ring.Mint(user, keyID, now)
	httpx.WriteJSON(w, http.StatusOK, model.AppAttestToken{Token: tok, ExpiresAt: expires, TTLSec: int(appattest.TokenTTL / time.Second)})
}

// consume uses the account's challenge up, before any other field is checked, so that a call uses it
// whatever it answers (API.md). fresh is whether it was open; ok false: the answer is written (an
// empty challenge is the 400 of a missing field, a database error the 5xx).
func (a *appAttest) consume(w http.ResponseWriter, r *http.Request, user uuid.UUID, challenge string, now time.Time) (fresh bool, cdh [32]byte, ok bool) {
	if challenge == "" {
		httpx.WriteError(w, http.StatusBadRequest, msgAttestChallengeField)
		return false, cdh, false
	}
	cdh = appattest.ClientDataHash(challenge)
	fresh, err := a.st.ConsumeAttestChallenge(r.Context(), user, cdh, now)
	if err != nil {
		httpx.Fail(w, r, err)
		return false, cdh, false
	}
	return fresh, cdh, true
}

// purgeAppAttest is the tick's "app attest purge" step: the expired challenges and the keys unused for
// 90 days (D245). Idempotent, so whichever instance gets the tick runs it; a pass that deletes nothing
// is silent, a failed one is logged and returned for the tick.
func purgeAppAttest(ctx context.Context, st *store.Store, log *slog.Logger) error {
	challenges, keys, err := st.PurgeAppAttest(ctx, time.Now())
	if err != nil {
		log.WarnContext(ctx, "app attest purge failed", "err", err)
		return err
	}
	if challenges > 0 || keys > 0 {
		log.InfoContext(ctx, "app attest purged", "challenges", challenges, "keys", keys)
	}
	return nil
}
