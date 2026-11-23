package store

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/model"
)

// Direct connections, the api's side (D257, design 6; migration 0042): the OAuth states, the grant waiting for its
// confirm, the live connection with its sealed tokens, the cap, disconnecting, and the queue that ends grants at the
// provider. The store holds sealed bytes only; opening and sealing is cmd/api's (internal/sealed stays there).

// The refusals the confirm and the cap give.
var (
	ErrCapFull          = errors.New("connection: the provider's user cap is full")
	ErrTicketInvalid    = errors.New("connection: unknown ticket")
	ErrConfirmExpired   = errors.New("connection: the confirm came too late")
	ErrConnectNoAccount = errors.New("connection: the account is gone")
)

// Times of the flow and of the queue.
const (
	StateTTL            = 10 * time.Minute // an open OAuth state and a callback's confirm window
	MaxOpenStates       = 3                // per account and provider: a fourth cancels the oldest
	DeregistrationLimit = 30 * 24 * time.Hour
	RevokedKeep         = 90 * 24 * time.Hour
	StaleAfter          = 36 * time.Hour
)

// ConnectionRow is a connection as the app sees it (GET /v1/me/connections).
type ConnectionRow struct {
	Provider       string
	Status         string
	ConnectedAt    time.Time
	LastDataAt     *time.Time
	ProviderUserID *string
	Permissions    []string
}

const connectionColumns = `provider, status, connected_at, last_data_at, provider_user_id, permissions`

func scanConnection(r pgx.Row) (ConnectionRow, error) {
	var c ConnectionRow
	err := r.Scan(&c.Provider, &c.Status, &c.ConnectedAt, &c.LastDataAt, &c.ProviderUserID, &c.Permissions)
	if c.Permissions == nil {
		c.Permissions = []string{}
	}
	return c, err
}

// Connections lists the account's connections, by provider.
func (s *Store) Connections(ctx context.Context, user uuid.UUID) ([]ConnectionRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+connectionColumns+` FROM connections WHERE user_id=$1 ORDER BY provider`, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ConnectionRow, error) { return scanConnection(r) })
}

// liveSQL is "this account holds the provider as connected or needs_reauth".
const liveSQL = `EXISTS (SELECT 1 FROM connections WHERE user_id=$1 AND provider=$2 AND status IN ('connected','needs_reauth'))`

// loadSQL counts what the provider's cap counts, for everyone but $2: its live connections, and the accounts
// without one that hold an unexpired state or an unconfirmed, unexpired pending grant. The caller's own flow never
// counts against it (a sheet cancelled and started again must not fill the last place twice).
const loadSQL = `SELECT
  (SELECT count(*) FROM connections WHERE provider=$1 AND status IN ('connected','needs_reauth') AND user_id <> $2)
  + (SELECT count(*) FROM (
       SELECT user_id FROM connection_states WHERE provider=$1 AND expires_at > now() AND user_id <> $2
       UNION
       SELECT user_id FROM connection_pending WHERE provider=$1 AND confirmed_at IS NULL AND confirm_expires_at > now() AND user_id <> $2
     ) o WHERE NOT EXISTS (SELECT 1 FROM connections c WHERE c.user_id=o.user_id AND c.provider=$1 AND c.status IN ('connected','needs_reauth')))`

type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func providerLoad(ctx context.Context, q queryer, provider string, except uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx, loadSQL, provider, except).Scan(&n)
	return n, err
}

// ProviderLoad is what the provider's cap counts for everyone but except (uuid.Nil: everyone); the gauges use it.
func (s *Store) ProviderLoad(ctx context.Context, provider string, except uuid.UUID) (int, error) {
	return providerLoad(ctx, s.pool, provider, except)
}

// Accepting says whether user could start a connection to the provider now: they hold it live (a reconnect never
// meets the cap), or the others leave a place.
func (s *Store) Accepting(ctx context.Context, user uuid.UUID, provider string, cap int) (bool, error) {
	var live bool
	if err := s.pool.QueryRow(ctx, `SELECT `+liveSQL, user, provider).Scan(&live); err != nil || live {
		return live, err
	}
	n, err := providerLoad(ctx, s.pool, provider, user)
	return n < cap, err
}

// withCap runs fn in a transaction holding the provider's cap lock, after checking the cap for user: a live
// connection of theirs passes; otherwise the others must leave a place.
func (s *Store) withCap(ctx context.Context, user uuid.UUID, provider string, cap int, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockConnectCap, provider); err != nil {
			return err
		}
		return capped(ctx, tx, user, provider, cap, fn)
	})
}

func capped(ctx context.Context, tx pgx.Tx, user uuid.UUID, provider string, cap int, fn func(tx pgx.Tx) error) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT `+liveSQL, user, provider).Scan(&live); err != nil {
		return err
	}
	if !live {
		n, err := providerLoad(ctx, tx, provider, user)
		if err != nil {
			return err
		}
		if n >= cap {
			return ErrCapFull
		}
	}
	return fn(tx)
}

// ---- start and callback ------------------------------------------------------------------------------------------

// NewState is an OAuth flow to open: the state's SHA-256 (never the state), the sealed PKCE verifier (AD
// connect:state:<hex of Hash>) and the policy version consented to.
type NewState struct {
	Hash          [32]byte
	VerifierEnc   []byte
	PolicyVersion string
	Expires       time.Time
}

// StartConnect opens a flow under the cap (ErrCapFull) and keeps the account's newest MaxOpenStates for the
// provider, the new one always among them.
func (s *Store) StartConnect(ctx context.Context, user uuid.UUID, provider string, cap int, st NewState) error {
	err := s.withCap(ctx, user, provider, cap, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO connection_states (state_hash, user_id, provider, verifier_enc, policy_version, created_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,clock_timestamp(),$6)`, st.Hash[:], user, provider, st.VerifierEnc, st.PolicyVersion, st.Expires); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM connection_states WHERE user_id=$1 AND provider=$2 AND state_hash <> $4
			AND (expires_at <= now() OR state_hash NOT IN (SELECT o.state_hash FROM connection_states o
			  WHERE o.user_id=$1 AND o.provider=$2 AND o.state_hash <> $4 AND o.expires_at > now()
			  ORDER BY o.created_at DESC, o.state_hash DESC LIMIT $3))`, user, provider, MaxOpenStates-1, st.Hash[:])
		return err
	})
	return noAccount(err)
}

// noAccount turns a foreign-key violation on users into ErrConnectNoAccount.
func noAccount(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" {
		return ErrConnectNoAccount
	}
	return err
}

// FlowState is a consumed OAuth state.
type FlowState struct {
	UserID        uuid.UUID
	VerifierEnc   []byte
	PolicyVersion string
	Expires       time.Time
}

// ConsumeState uses the flow up: single use by DELETE ... RETURNING, whatever the answer. ErrNotFound for an
// unknown, used or other provider's state. The caller compares Expires with its clock.
func (s *Store) ConsumeState(ctx context.Context, hash [32]byte, provider string) (FlowState, error) {
	var f FlowState
	err := s.pool.QueryRow(ctx, `DELETE FROM connection_states WHERE state_hash=$1 AND provider=$2
		RETURNING user_id, verifier_enc, policy_version, expires_at`, hash[:], provider).
		Scan(&f.UserID, &f.VerifierEnc, &f.PolicyVersion, &f.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return FlowState{}, ErrNotFound
	}
	return f, err
}

// Pending is a grant waiting for its confirm. The tokens are sealed with the pending ADs
// (connect:pending-access:<provider>:<user>, connect:pending-refresh:...).
type Pending struct {
	UserID                    uuid.UUID
	Provider                  string
	ProviderUserID            string
	Permissions               []string
	AccessEnc, RefreshEnc     []byte
	AccessExpires, RefreshDue time.Time // zero: none
	ConfirmHash               [32]byte
	Expires                   time.Time
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// PutPending stores the callback's grant under the cap (ErrCapFull; the caller then queues the grant itself,
// QueueDeregistration, reason cap). A grant this account already had waiting, unconfirmed, goes to the queue first
// (reason confirm_expired). The live connection is untouched (B1). ErrConnectNoAccount when the account is gone.
func (s *Store) PutPending(ctx context.Context, p Pending) (err error) {
	return s.putPending(ctx, p, 0, false)
}

// PutPendingCapped is PutPending under the provider's cap.
func (s *Store) PutPendingCapped(ctx context.Context, p Pending, cap int) error {
	return s.putPending(ctx, p, cap, true)
}

func (s *Store) putPending(ctx context.Context, p Pending, cap int, enforce bool) error {
	body := func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
			SELECT user_id, provider, provider_user_id, 'confirm_expired', access_enc, refresh_enc FROM connection_pending
			WHERE user_id=$1 AND provider=$2 AND confirmed_at IS NULL AND access_enc IS NOT NULL`, p.UserID, p.Provider); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO connection_pending (user_id, provider, provider_user_id, permissions, access_enc, refresh_enc,
			  access_expires_at, refresh_due_at, confirm_hash, confirm_expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (user_id, provider) DO UPDATE SET provider_user_id=EXCLUDED.provider_user_id, permissions=EXCLUDED.permissions,
			  access_enc=EXCLUDED.access_enc, refresh_enc=EXCLUDED.refresh_enc, access_expires_at=EXCLUDED.access_expires_at,
			  refresh_due_at=EXCLUDED.refresh_due_at, confirm_hash=EXCLUDED.confirm_hash, confirm_expires_at=EXCLUDED.confirm_expires_at,
			  confirmed_at=NULL, created_at=now()`,
			p.UserID, p.Provider, p.ProviderUserID, nonNil(p.Permissions), p.AccessEnc, p.RefreshEnc,
			nullTime(p.AccessExpires), nullTime(p.RefreshDue), p.ConfirmHash[:], p.Expires)
		return err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockConnectCap, p.Provider); err != nil {
			return err
		}
		if enforce {
			return capped(ctx, tx, p.UserID, p.Provider, cap, body)
		}
		return body(tx)
	})
	return noAccount(err)
}

// ---- confirm -----------------------------------------------------------------------------------------------------

// ConfirmIn is the confirm. Reseal turns the pending grant's sealed tokens (pending ADs) into the live ones (live
// ADs): the store never holds the key. Emit writes the ingested event (reason disconnect) of a connection the swap
// ends, in the same transaction.
type ConfirmIn struct {
	UserID        uuid.UUID
	Provider      string
	TicketHash    [32]byte
	Cap           int
	PolicyVersion string
	Reseal        func(accessEnc, refreshEnc []byte) (access, refresh []byte, err error)
	Emit          func(tx pgx.Tx, user uuid.UUID, days []string) error
}

// The confirm's outcomes (ryft_connect_confirms_total).
const (
	ConfirmOK       = "ok"
	ConfirmRepeat   = "repeat"
	ConfirmReplaced = "replaced"
	ConfirmRelinked = "relinked"
)

// ConfirmOut is what the confirm did.
type ConfirmOut struct {
	Connection ConnectionRow
	Outcome    string
	Queued     []int64 // deregistration rows the swap queued: the caller makes one attempt after the commit
	Relinked   bool    // another account lost the provider account (its days are queued, its event written)
}

// ConfirmConnection makes the pending grant the account's connection (design 6.2): under the provider's cap lock
// and the account's apply lock, the ticket must match this account's pending row; a repeat within the window
// answers the connection again; one past it drops the grant (queued, reason confirm_expired) and answers
// ErrConfirmExpired. The swap: a live link of another account to the same provider account ends (relinked: revoked,
// its data deleted, its tokens deleted without a deregistration); this account's live link to another provider
// account ends first (replaced, as a disconnect); then the connection is written with the live tokens.
func (s *Store) ConfirmConnection(ctx context.Context, in ConfirmIn) (ConfirmOut, error) {
	var out ConfirmOut
	expired := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockConnectCap, in.Provider); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, lockApply, in.UserID); err != nil {
			return err
		}
		var p struct {
			puid                  string
			perms                 []string
			accessEnc, refreshEnc []byte
			accessExp, refreshDue *time.Time
			hash                  []byte
			expires               time.Time
			confirmed             *time.Time
			over                  bool
		}
		err := tx.QueryRow(ctx, `SELECT provider_user_id, permissions, access_enc, refresh_enc, access_expires_at, refresh_due_at,
			  confirm_hash, confirm_expires_at, confirmed_at, confirm_expires_at <= now()
			FROM connection_pending WHERE user_id=$1 AND provider=$2 FOR UPDATE`, in.UserID, in.Provider).
			Scan(&p.puid, &p.perms, &p.accessEnc, &p.refreshEnc, &p.accessExp, &p.refreshDue, &p.hash, &p.expires, &p.confirmed, &p.over)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTicketInvalid
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(p.hash, in.TicketHash[:]) != 1 {
			return ErrTicketInvalid
		}
		if p.over {
			if err := dropPending(ctx, tx, in.UserID, in.Provider, "confirm_expired"); err != nil {
				return err
			}
			expired = true
			return nil
		}
		if p.confirmed != nil {
			out.Outcome = ConfirmRepeat
			out.Connection, err = scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE user_id=$1 AND provider=$2`, in.UserID, in.Provider))
			// The connection ended after the confirm (gone, or revoked by a relink to another account or the
			// provider's notice): the ticket is spent, and the app reads the list (P2-20).
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && out.Connection.Status == model.ConnectionRevoked) {
				return ErrTicketInvalid
			}
			return err
		}
		return swap(ctx, tx, in, p.puid, p.perms, p.accessEnc, p.refreshEnc, p.accessExp, p.refreshDue, &out)
	})
	if err != nil {
		return ConfirmOut{}, err
	}
	if expired {
		return ConfirmOut{}, ErrConfirmExpired
	}
	return out, nil
}

// swap is the confirm's writes, in the order the unique index needs (design 6.2).
func swap(ctx context.Context, tx pgx.Tx, in ConfirmIn, puid string, perms []string, accessEnc, refreshEnc []byte, accessExp, refreshDue *time.Time, out *ConfirmOut) error {
	var minePUID *string
	var mineStatus string
	err := tx.QueryRow(ctx, `SELECT provider_user_id, status FROM connections WHERE user_id=$1 AND provider=$2 FOR UPDATE`, in.UserID, in.Provider).
		Scan(&minePUID, &mineStatus)
	mine := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	live := mine && mineStatus != model.ConnectionRevoked
	if !live {
		n, err := providerLoad(ctx, tx, in.Provider, in.UserID)
		if err != nil {
			return err
		}
		if n >= in.Cap {
			return ErrCapFull
		}
	}
	out.Outcome = ConfirmOK
	if !live {
		// A link that ended (revoked by the provider's notice, say) may have left its processed deregistration notice
		// in the inbox for the dedupe window: a later identical notice would be taken for a repeat and ignored (P2-21).
		if _, err := tx.Exec(ctx, `DELETE FROM provider_inbox WHERE user_id=$1 AND provider=$2`, in.UserID, in.Provider); err != nil {
			return fmt.Errorf("clear inbox: %w", err)
		}
	}
	// Another account holds this provider account: it ends there, without a deregistration call (it would end the
	// new grant too: same provider user, same app). Its apply lock comes before its row (ApplyDirect, Disconnect and
	// the rest take the apply lock first): find it unlocked, take the lock, then read the row FOR UPDATE again (P2-14).
	const holder = `SELECT user_id FROM connections WHERE provider=$1 AND provider_user_id=$2
		AND status IN ('connected','needs_reauth') AND user_id <> $3`
	var other uuid.UUID
	err = tx.QueryRow(ctx, holder, in.Provider, puid, in.UserID).Scan(&other)
	if err == nil {
		if _, err := tx.Exec(ctx, lockApply, other); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, holder+` FOR UPDATE`, in.Provider, puid, in.UserID).Scan(&other)
	}
	switch {
	case err == nil:
		if _, err := endConnection(ctx, tx, other, in.Provider, endRelink, "", false, in.Emit); err != nil {
			return err
		}
		out.Outcome, out.Relinked = ConfirmRelinked, true
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	// This account's live link to another provider account ends first, as a disconnect.
	if live && (minePUID == nil || *minePUID != puid) {
		q, err := endConnection(ctx, tx, in.UserID, in.Provider, endDisconnect, "replaced", false, in.Emit)
		if err != nil {
			return err
		}
		out.Queued = append(out.Queued, q...)
		if out.Outcome == ConfirmOK {
			out.Outcome = ConfirmReplaced
		}
	}
	access, refresh, err := in.Reseal(accessEnc, refreshEnc)
	if err != nil {
		return fmt.Errorf("reseal: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version, connected_at, status_at)
		VALUES ($1,$2,$3,'connected',$4,$5,now(),now())
		ON CONFLICT (user_id, provider) DO UPDATE SET provider_user_id=EXCLUDED.provider_user_id, status='connected', status_reason=NULL,
		  permissions=EXCLUDED.permissions, policy_version=EXCLUDED.policy_version, connected_at=now(), status_at=now(),
		  last_data_at = CASE WHEN connections.status = 'revoked' THEN NULL ELSE connections.last_data_at END`,
		in.UserID, in.Provider, puid, nonNil(perms), in.PolicyVersion); err != nil {
		return fmt.Errorf("write connection: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO connection_tokens (user_id, provider, access_enc, refresh_enc, access_expires_at, refresh_due_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (user_id, provider) DO UPDATE SET access_enc=EXCLUDED.access_enc, refresh_enc=EXCLUDED.refresh_enc,
		  access_expires_at=EXCLUDED.access_expires_at, refresh_due_at=EXCLUDED.refresh_due_at, refresh_attempts=0, last_error=NULL, updated_at=now()`,
		in.UserID, in.Provider, access, refresh, accessExp, refreshDue); err != nil {
		return fmt.Errorf("write tokens: %w", err)
	}
	// The pending row keeps the ticket's hash until its window ends (a repeat confirm answers 200, S3), holds no token.
	if _, err := tx.Exec(ctx, `UPDATE connection_pending SET access_enc=NULL, refresh_enc=NULL, confirmed_at=now() WHERE user_id=$1 AND provider=$2`,
		in.UserID, in.Provider); err != nil {
		return err
	}
	out.Connection, err = scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE user_id=$1 AND provider=$2`, in.UserID, in.Provider))
	return err
}

// ---- ending a connection -----------------------------------------------------------------------------------------

type endMode int

const (
	endDisconnect endMode = iota // tokens to the queue, the rows deleted
	endRelink                    // the row stays as revoked / relinked; tokens deleted, no deregistration
)

// endConnection is what every end of a connection but the provider's own notice does, in the caller's transaction
// (which holds the cap lock or not, but the account's apply lock is taken here): tokens (queued under reason, or
// deleted for a relink), the rows, the inbox, the direct data and its days, and the event for them. It returns the
// queue rows it made.
func endConnection(ctx context.Context, tx pgx.Tx, user uuid.UUID, provider string, mode endMode, reason string, withPending bool,
	emit func(tx pgx.Tx, user uuid.UUID, days []string) error) ([]int64, error) {
	if _, err := tx.Exec(ctx, lockApply, user); err != nil {
		return nil, err
	}
	loc, err := userLoc(ctx, tx, user)
	if err != nil {
		return nil, err
	}
	var queued []int64
	collect := func(sql string, args ...any) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		queued = append(queued, ids...)
		return err
	}
	switch mode {
	case endDisconnect:
		if err := collect(`INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
			SELECT c.user_id, c.provider, c.provider_user_id, $3, t.access_enc, t.refresh_enc FROM connections c
			JOIN connection_tokens t ON t.user_id=c.user_id AND t.provider=c.provider
			WHERE c.user_id=$1 AND c.provider=$2 AND c.status <> 'revoked' AND c.provider_user_id IS NOT NULL RETURNING id`, user, provider, reason); err != nil {
			return nil, fmt.Errorf("queue tokens: %w", err)
		}
		stmts := []string{
			`DELETE FROM connection_tokens WHERE user_id=$1 AND provider=$2`,
			`DELETE FROM connections WHERE user_id=$1 AND provider=$2`,
		}
		if withPending { // a disconnect cancels the grant waiting for its confirm; the confirm's own swap is that grant
			if err := collect(`INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
				SELECT user_id, provider, provider_user_id, $3, access_enc, refresh_enc FROM connection_pending
				WHERE user_id=$1 AND provider=$2 AND confirmed_at IS NULL AND access_enc IS NOT NULL RETURNING id`, user, provider, reason); err != nil {
				return nil, fmt.Errorf("queue pending: %w", err)
			}
			stmts = append([]string{`DELETE FROM connection_pending WHERE user_id=$1 AND provider=$2`}, stmts...)
		}
		for _, q := range stmts {
			if _, err := tx.Exec(ctx, q, user, provider); err != nil {
				return nil, err
			}
		}
	case endRelink:
		if _, err := tx.Exec(ctx, `DELETE FROM connection_tokens WHERE user_id=$1 AND provider=$2`, user, provider); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE connections SET status='revoked', status_reason='relinked', provider_user_id=NULL,
			permissions='{}', status_at=now() WHERE user_id=$1 AND provider=$2`, user, provider); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM provider_inbox WHERE user_id=$1 AND provider=$2`, user, provider); err != nil {
		return nil, fmt.Errorf("delete inbox: %w", err)
	}
	days := newDaySet(loc, nil)
	if err := deleteDirectData(ctx, tx, user, provider, days); err != nil {
		return nil, err
	}
	var emitFn func(pgx.Tx, []string) error
	if emit != nil {
		emitFn = func(tx pgx.Tx, d []string) error { return emit(tx, user, d) }
	}
	return queued, queueAndEmit(ctx, tx, user, days.sorted(), emitFn)
}

// DisconnectOut is what Disconnect did.
type DisconnectOut struct {
	Existed bool    // there was a connection row (any status) or a pending grant
	Queued  []int64 // deregistration rows to attempt after the commit; none: nothing to end at the provider
}

// Disconnect ends the account's connection to the provider and deletes what it delivered, in one transaction
// (design 6.4). Idempotent: with nothing there it changes nothing.
func (s *Store) Disconnect(ctx context.Context, user uuid.UUID, provider string, emit func(tx pgx.Tx, user uuid.UUID, days []string) error) (DisconnectOut, error) {
	var out DisconnectOut
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockApply, user); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM connections WHERE user_id=$1 AND provider=$2)
			OR EXISTS (SELECT 1 FROM connection_pending WHERE user_id=$1 AND provider=$2)`, user, provider).Scan(&out.Existed); err != nil {
			return err
		}
		// The account's open states go too: a flow started and not finished must not complete after a disconnect.
		if _, err := tx.Exec(ctx, `DELETE FROM connection_states WHERE user_id=$1 AND provider=$2`, user, provider); err != nil {
			return err
		}
		if !out.Existed {
			return nil // nothing to end: no scan of the account's samples under the apply lock (P2-18)
		}
		var err error
		out.Queued, err = endConnection(ctx, tx, user, provider, endDisconnect, "disconnect", true, emit)
		return err
	})
	return out, err
}

// DropPending takes the account's unconfirmed pending grant out: its tokens go to the queue under reason (the
// live-grant rule decides at the attempt whether the provider is called) and the row is deleted. It returns the queue
// rows. Used when the confirm finds the provider no longer enabled for the account.
func (s *Store) DropPending(ctx context.Context, user uuid.UUID, provider, reason string) ([]int64, error) {
	var ids []int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
			SELECT user_id, provider, provider_user_id, $3, access_enc, refresh_enc FROM connection_pending
			WHERE user_id=$1 AND provider=$2 AND confirmed_at IS NULL AND access_enc IS NOT NULL RETURNING id`, user, provider, reason)
		if err != nil {
			return err
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[int64]); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM connection_pending WHERE user_id=$1 AND provider=$2`, user, provider)
		return err
	})
	return ids, err
}

func dropPending(ctx context.Context, tx pgx.Tx, user uuid.UUID, provider, reason string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
		SELECT user_id, provider, provider_user_id, $3, access_enc, refresh_enc FROM connection_pending
		WHERE user_id=$1 AND provider=$2 AND confirmed_at IS NULL AND access_enc IS NOT NULL`, user, provider, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM connection_pending WHERE user_id=$1 AND provider=$2`, user, provider)
	return err
}

// ---- the deregistration queue ------------------------------------------------------------------------------------

// Dereg is a queue row: tokens still sealed, with the ADs of the table they came from (the live ones, or the
// pending ones: the caller tries both).
type Dereg struct {
	ID                    int64
	UserID                uuid.UUID
	Provider              string
	ProviderUserID        string
	Reason                string
	AccessEnc, RefreshEnc []byte
	CreatedAt             time.Time
	Attempts              int
}

const deregColumns = `id, user_id, provider, provider_user_id, reason, access_enc, refresh_enc, created_at, attempts`

func scanDereg(r pgx.CollectableRow) (Dereg, error) {
	var d Dereg
	err := r.Scan(&d.ID, &d.UserID, &d.Provider, &d.ProviderUserID, &d.Reason, &d.AccessEnc, &d.RefreshEnc, &d.CreatedAt, &d.Attempts)
	return d, err
}

// QueueDeregistration queues a grant the caller holds sealed (the callback's, when the cap is full or a permission is
// missing). It returns the row's id.
func (s *Store) QueueDeregistration(ctx context.Context, user uuid.UUID, provider, puid, reason string, accessEnc, refreshEnc []byte) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`, user, provider, puid, reason, accessEnc, refreshEnc).Scan(&id)
	return id, err
}

// Deregistration is one queue row by id; ErrNotFound when it is gone.
func (s *Store) Deregistration(ctx context.Context, id int64) (Dereg, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deregColumns+` FROM connection_deregistrations WHERE id=$1`, id)
	if err != nil {
		return Dereg{}, err
	}
	d, err := pgx.CollectOneRow(rows, scanDereg)
	if errors.Is(err, pgx.ErrNoRows) {
		return Dereg{}, ErrNotFound
	}
	return d, err
}

// UserDeregistrations are the rows of an account, oldest first (DELETE /v1/me's one attempt).
func (s *Store) UserDeregistrations(ctx context.Context, user uuid.UUID) ([]Dereg, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+deregColumns+` FROM connection_deregistrations WHERE user_id=$1 ORDER BY id`, user)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanDereg)
}

// ClaimDeregistrations takes up to limit due rows for one attempt each: a claim pushes next_attempt_at ten minutes
// ahead, so two instances never call the provider for one row; RetryDeregistration sets the real backoff.
func (s *Store) ClaimDeregistrations(ctx context.Context, limit int) ([]Dereg, error) {
	rows, err := s.pool.Query(ctx, `UPDATE connection_deregistrations SET next_attempt_at = now() + interval '10 minutes'
		WHERE id IN (SELECT id FROM connection_deregistrations WHERE next_attempt_at <= now() ORDER BY next_attempt_at, id LIMIT $1 FOR UPDATE SKIP LOCKED)
		RETURNING `+deregColumns, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanDereg)
}

// HoldsLiveGrant says whether (provider, provider user id) is in use: an account holds it as connected or
// needs_reauth, or an account has a grant for it waiting for its confirm (unconfirmed, inside its window). Such a
// grant is never deregistered, whatever queued it (review B2, P2-6): the provider's call would end the other
// account's grant too (same provider user, same app). The check runs in a transaction that holds the provider's
// cap lock, the lock a confirm takes, so it cannot interleave with a confirm's swap: a confirm that committed
// before is seen, one that comes after waits for the check. The provider call itself cannot hold the lock (it is
// a network call), so a confirm landing after the check but before the call is the one window left; it ends in the
// link's refresh failing and the account being asked to reconnect.
func (s *Store) HoldsLiveGrant(ctx context.Context, provider, puid string) (bool, error) {
	var held bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockConnectCap, provider); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM connections WHERE provider=$1 AND provider_user_id=$2
				AND status IN ('connected','needs_reauth'))
			OR EXISTS (SELECT 1 FROM connection_pending WHERE provider=$1 AND provider_user_id=$2
				AND confirmed_at IS NULL AND access_enc IS NOT NULL AND confirm_expires_at > now())`, provider, puid).Scan(&held)
	})
	return held, err
}

// FinishDeregistration deletes a queue row (done, already gone, or skipped as live).
func (s *Store) FinishDeregistration(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM connection_deregistrations WHERE id=$1`, id)
	return err
}

// RetryDeregistration records a failed attempt (a code, never the provider's text) and when the next is due.
func (s *Store) RetryDeregistration(ctx context.Context, id int64, code string, next time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE connection_deregistrations SET attempts = attempts + 1, last_error=$2, next_attempt_at=$3 WHERE id=$1`, id, code, next)
	return err
}

// ExpiredDeregistration is a row given up on.
type ExpiredDeregistration struct{ Provider, Reason string }

// GiveUpDeregistrations deletes the rows older than DeregistrationLimit and says which.
func (s *Store) GiveUpDeregistrations(ctx context.Context) ([]ExpiredDeregistration, error) {
	rows, err := s.pool.Query(ctx, `DELETE FROM connection_deregistrations WHERE created_at < now() - $1::interval RETURNING provider, reason`,
		fmt.Sprintf("%d seconds", int(DeregistrationLimit.Seconds())))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ExpiredDeregistration, error) {
		var e ExpiredDeregistration
		return e, r.Scan(&e.Provider, &e.Reason)
	})
}

// OldestDeregistration is the age of the oldest queue row; 0 when the queue is empty.
func (s *Store) OldestDeregistration(ctx context.Context) (time.Duration, error) {
	var age *float64
	if err := s.pool.QueryRow(ctx, `SELECT extract(epoch FROM now() - min(created_at)) FROM connection_deregistrations`).Scan(&age); err != nil || age == nil {
		return 0, err
	}
	return time.Duration(*age * float64(time.Second)), nil
}

// ---- refresh -----------------------------------------------------------------------------------------------------

// RefreshDue is a connection whose tokens are due for a refresh.
type RefreshDue struct {
	UserID                uuid.UUID
	Provider              string
	ProviderUserID        string
	AccessEnc, RefreshEnc []byte
	Attempts              int
}

// ClaimRefreshes takes up to limit connected links whose refresh is due, oldest first. A claim pushes refresh_due_at
// ten minutes ahead, so two instances do not spend one single-use refresh token twice.
func (s *Store) ClaimRefreshes(ctx context.Context, limit int) ([]RefreshDue, error) {
	rows, err := s.pool.Query(ctx, `UPDATE connection_tokens t SET refresh_due_at = now() + interval '10 minutes'
		WHERE (t.user_id, t.provider) IN (SELECT t2.user_id, t2.provider FROM connection_tokens t2
		    JOIN connections c ON c.user_id=t2.user_id AND c.provider=t2.provider
		  WHERE t2.refresh_due_at <= now() AND t2.refresh_enc IS NOT NULL AND c.status='connected'
		  ORDER BY t2.refresh_due_at LIMIT $1 FOR UPDATE OF t2 SKIP LOCKED)
		RETURNING t.user_id, t.provider,
		  COALESCE((SELECT c.provider_user_id FROM connections c WHERE c.user_id=t.user_id AND c.provider=t.provider), ''),
		  t.access_enc, t.refresh_enc, t.refresh_attempts`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (RefreshDue, error) {
		var d RefreshDue
		return d, r.Scan(&d.UserID, &d.Provider, &d.ProviderUserID, &d.AccessEnc, &d.RefreshEnc, &d.Attempts)
	})
}

// SaveRefresh stores a refreshed grant's sealed tokens and the next due time (zero: never). saved is false when the
// account's token row is gone (a disconnect ran during the refresh): the caller then holds tokens nothing stores
// (P2-19).
func (s *Store) SaveRefresh(ctx context.Context, user uuid.UUID, provider string, accessEnc, refreshEnc []byte, accessExp, due time.Time) (saved bool, err error) {
	tag, err := s.pool.Exec(ctx, `UPDATE connection_tokens SET access_enc=$3, refresh_enc=COALESCE($4, refresh_enc), access_expires_at=$5,
		refresh_due_at=$6, refresh_attempts=0, last_error=NULL, updated_at=now() WHERE user_id=$1 AND provider=$2`,
		user, provider, accessEnc, refreshEnc, nullTime(accessExp), nullTime(due))
	return tag.RowsAffected() == 1, err
}

// RefreshFailed records a transient failure: the attempt count and when to try again.
func (s *Store) RefreshFailed(ctx context.Context, user uuid.UUID, provider, code string, attempts int, next time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_attempts=$3, last_error=$4, refresh_due_at=$5, updated_at=now()
		WHERE user_id=$1 AND provider=$2`, user, provider, attempts, code, next)
	return err
}

// NeedsReauth marks a connected link needs_reauth / refresh_failed; its tokens stay. The refresh no longer picks
// it up (it is not connected).
func (s *Store) NeedsReauth(ctx context.Context, user uuid.UUID, provider, code string) error {
	_, err := s.pool.Exec(ctx, `WITH c AS (UPDATE connections SET status='needs_reauth', status_reason='refresh_failed', status_at=now()
		WHERE user_id=$1 AND provider=$2 AND status='connected' RETURNING 1)
		UPDATE connection_tokens SET last_error=$3, refresh_due_at=NULL, updated_at=now() WHERE user_id=$1 AND provider=$2`, user, provider, code)
	return err
}

// ---- purges and gauges -------------------------------------------------------------------------------------------

// Purged is what PurgeConnect removed, by kind.
type Purged struct{ States, Pending, Tokens, Revoked int64 }

// PurgeConnect is the api tick's purge: expired states, the pending grants past their window (an unconfirmed one's
// tokens to the queue first, reason confirm_expired), the tokens of revoked connections (ingest marks a revocation
// and cannot touch tokens) and revoked rows RevokedKeep after they ended. It returns what it removed.
func (s *Store) PurgeConnect(ctx context.Context) (Purged, error) {
	var out Purged
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM connection_states WHERE expires_at < now()`)
		if err != nil {
			return err
		}
		out.States = tag.RowsAffected()
		if _, err := tx.Exec(ctx, `INSERT INTO connection_deregistrations (user_id, provider, provider_user_id, reason, access_enc, refresh_enc)
			SELECT user_id, provider, provider_user_id, 'confirm_expired', access_enc, refresh_enc FROM connection_pending
			WHERE confirm_expires_at < now() AND confirmed_at IS NULL AND access_enc IS NOT NULL`); err != nil {
			return err
		}
		if tag, err = tx.Exec(ctx, `DELETE FROM connection_pending WHERE confirm_expires_at < now()`); err != nil {
			return err
		}
		out.Pending = tag.RowsAffected()
		if tag, err = tx.Exec(ctx, `DELETE FROM connection_tokens t USING connections c
			WHERE c.user_id=t.user_id AND c.provider=t.provider AND c.status='revoked'`); err != nil {
			return err
		}
		out.Tokens = tag.RowsAffected()
		tag, err = tx.Exec(ctx, `DELETE FROM connections WHERE status='revoked' AND status_at < now() - $1::interval`,
			fmt.Sprintf("%d seconds", int(RevokedKeep.Seconds())))
		out.Revoked = tag.RowsAffected()
		return err
	})
	return out, err
}

// ConnectionCounts are the connections per provider and status (the gauges); a provider or status with none is
// absent.
func (s *Store) ConnectionCounts(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT provider, status, count(*) FROM connections GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{}
	var p, st string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&p, &st, &n}, func() error {
		if out[p] == nil {
			out[p] = map[string]int{}
		}
		out[p][st] = n
		return nil
	})
	return out, err
}

// StaleConnections counts the provider's connected links that were connected over StaleAfter ago and have stored
// nothing for as long.
func (s *Store) StaleConnections(ctx context.Context, provider string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM connections WHERE provider=$1 AND status='connected'
		AND connected_at < now() - $2::interval AND (last_data_at IS NULL OR last_data_at < now() - $2::interval)`,
		provider, fmt.Sprintf("%d seconds", int(StaleAfter.Seconds()))).Scan(&n)
	return n, err
}

// LiveConnectionCount is how many connected or needs_reauth connections the account holds: the admin merge refuses
// while the source has any (a revoked entry holds nothing to merge).
func (s *Store) LiveConnectionCount(ctx context.Context, user uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM connections WHERE user_id=$1 AND status IN ('connected','needs_reauth')`, user).Scan(&n)
	return n, err
}
