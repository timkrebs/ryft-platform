package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/model"
)

// The provider inbox, ingest's side (D257, design 7.2 to 7.4; migration 0042): the hook stores one row per
// provider record with its event in the same transaction, the consumer applies it, the tick keeps it.

// MaxPendingPerUser is how many pending inbox rows an account may hold (review S5): a day's real traffic is a few
// records, so more is a stuck consumer or a forged flood. Items past it are not stored.
const MaxPendingPerUser = 500

// Inbox retention (design 7.4).
const (
	InboxRepublishAfter = time.Hour
	InboxExpireAfter    = 7 * 24 * time.Hour
	InboxKeepDone       = 30 * 24 * time.Hour // processed and dropped: the dedupe window
	InboxKeepFailed     = 7 * 24 * time.Hour
	// MaxInboxAttempts is how many deliveries a row gets before it is failed (the topology's 20).
	MaxInboxAttempts = 20
)

// HookItem is one item of a verified push, bounded by the hook.
type HookItem struct {
	Kind, RecordID, ProviderUserID string
	Version                        int64
	Payload                        []byte // the item's JSON
}

// HookOutcome is what became of one item (ryft_connect_push_items_total).
type HookOutcome string

// The item outcomes.
const (
	HookStored      HookOutcome = "stored"
	HookUpdated     HookOutcome = "updated"
	HookDuplicate   HookOutcome = "duplicate"
	HookStale       HookOutcome = "stale"
	HookUnknownUser HookOutcome = "unknown_user"
	HookNoConsent   HookOutcome = "no_consent"
	HookCollision   HookOutcome = "id_collision"
	HookOverCap     HookOutcome = "over_cap"
	HookBadItem     HookOutcome = "bad_item" // the database refused the item as data (P2-17)
)

// StoreInbox is the hook's write. It resolves the items' provider users to accounts with a live link, drops the
// accounts whose consent is not to minPolicy or a later version, or is withdrawn (not stored: the same rule as the
// consumer's, D259), and in one transaction
// inserts or updates one inbox row per item, with the inbox.received event of each new or changed row written by
// emit in that transaction. The outcomes line up with items. The conflict update is guarded by user_id (S1), by
// the payload's digest (an identical re-push is a no-op) and by the version (a lower one is stale).
func (s *Store) StoreInbox(ctx context.Context, provider, minPolicy string, items []HookItem,
	emit func(tx pgx.Tx, ev model.InboxReceivedEvent) error) ([]HookOutcome, error) {
	out := make([]HookOutcome, len(items))
	if len(items) == 0 {
		return out, nil
	}
	puids := make([]string, 0, len(items))
	for _, it := range items {
		puids = append(puids, it.ProviderUserID)
	}
	// One transaction: the owners are read with their link rows locked FOR SHARE, so a confirm that replaces or relinks
	// the link (it locks the same row) either commits before this lookup, and the push is for nobody, or waits for
	// this commit and then deletes the rows stored here (P2-16).
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT provider_user_id, user_id FROM connections
			WHERE provider=$1 AND provider_user_id = ANY($2) AND status IN ('connected','needs_reauth') ORDER BY user_id FOR SHARE`, provider, puids)
		if err != nil {
			return err
		}
		owner := map[string]uuid.UUID{}
		var puid string
		var uid uuid.UUID
		if _, err := pgx.ForEachRow(rows, []any{&puid, &uid}, func() error { owner[puid] = uid; return nil }); err != nil {
			return err
		}
		users := make([]uuid.UUID, 0, len(owner))
		for _, u := range owner {
			users = append(users, u)
		}
		consented := map[uuid.UUID]bool{}
		pending := map[uuid.UUID]int{}
		if len(users) > 0 {
			// The account's current consent (not withdrawn, the latest granted: the user row's, as the consumer reads
			// it) is to minPolicy or a later version (D259). A withdrawal revokes every row, so it always drops.
			if rows, err = tx.Query(ctx, `SELECT u FROM unnest($1::uuid[]) AS u
				WHERE (SELECT c.policy_version FROM consents c WHERE c.user_id = u AND c.kind='health_processing'
				  AND c.revoked_at IS NULL ORDER BY c.granted_at DESC LIMIT 1) >= $2`, users, minPolicy); err != nil {
				return err
			}
			if _, err := pgx.ForEachRow(rows, []any{&uid}, func() error { consented[uid] = true; return nil }); err != nil {
				return err
			}
			if rows, err = tx.Query(ctx, `SELECT user_id, count(*) FROM provider_inbox WHERE user_id = ANY($1) AND state='pending' GROUP BY 1`, users); err != nil {
				return err
			}
			var n int
			if _, err := pgx.ForEachRow(rows, []any{&uid, &n}, func() error { pending[uid] = n; return nil }); err != nil {
				return err
			}
		}
		counted := copyCounts(pending)
		for i, it := range items {
			u, ok := owner[it.ProviderUserID]
			switch {
			case !ok:
				out[i] = HookUnknownUser
				continue
			case !consented[u]:
				out[i] = HookNoConsent
				continue
			case counted[u] >= MaxPendingPerUser:
				out[i] = HookOverCap
				continue
			}
			o, err := storeOne(ctx, tx, provider, u, it, emit)
			if err != nil {
				return err
			}
			if o == HookStored || o == HookUpdated {
				counted[u]++
			}
			out[i] = o
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// storeOne is storeItem in a savepoint: an item the database refuses as data (a JSON escape jsonb cannot hold, such as
// a lone surrogate: SQLSTATE class 22) is rolled back alone and counted bad_item, so one bad item neither fails the
// push nor, as a 503, makes the provider send it again for ever (P2-17). Any other error ends the transaction.
func storeOne(ctx context.Context, tx pgx.Tx, provider string, user uuid.UUID, it HookItem,
	emit func(tx pgx.Tx, ev model.InboxReceivedEvent) error) (HookOutcome, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	o, err := storeItem(ctx, sp, provider, user, it, emit)
	if err != nil {
		_ = sp.Rollback(ctx)
		var pg *pgconn.PgError
		if errors.As(err, &pg) && strings.HasPrefix(pg.Code, "22") {
			return HookBadItem, nil
		}
		return "", err
	}
	return o, sp.Commit(ctx)
}

func copyCounts(m map[uuid.UUID]int) map[uuid.UUID]int {
	c := make(map[uuid.UUID]int, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func storeItem(ctx context.Context, tx pgx.Tx, provider string, user uuid.UUID, it HookItem,
	emit func(tx pgx.Tx, ev model.InboxReceivedEvent) error) (HookOutcome, error) {
	sum := sha256.Sum256(it.Payload)
	var id int64
	var revision int
	var inserted bool
	err := tx.QueryRow(ctx, `INSERT INTO provider_inbox (provider, kind, record_id, user_id, version, payload, payload_sha256)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
		ON CONFLICT (provider, kind, record_id) DO UPDATE SET version=EXCLUDED.version, revision=provider_inbox.revision + 1,
		  payload=EXCLUDED.payload, payload_sha256=EXCLUDED.payload_sha256, state='pending', received_at=now(),
		  processed_at=NULL, attempts=0, error=NULL
		WHERE provider_inbox.user_id = EXCLUDED.user_id AND provider_inbox.payload_sha256 <> EXCLUDED.payload_sha256
		  AND EXCLUDED.version >= provider_inbox.version
		RETURNING id, revision, (xmax = 0)`, provider, it.Kind, it.RecordID, user, it.Version, string(it.Payload), sum[:]).
		Scan(&id, &revision, &inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return classify(ctx, tx, provider, user, it, sum)
	}
	if err != nil {
		return "", fmt.Errorf("inbox insert: %w", err)
	}
	if emit != nil {
		if err := emit(tx, model.InboxReceivedEvent{InboxID: id, UserID: user, Provider: provider, Kind: it.Kind, Revision: revision}); err != nil {
			return "", err
		}
	}
	if inserted {
		return HookStored, nil
	}
	return HookUpdated, nil
}

// classify says why the guarded upsert touched nothing: another account's row, an identical payload, or a lower
// version.
func classify(ctx context.Context, tx pgx.Tx, provider string, user uuid.UUID, it HookItem, sum [32]byte) (HookOutcome, error) {
	var owner uuid.UUID
	var version int64
	var stored []byte
	if err := tx.QueryRow(ctx, `SELECT user_id, version, payload_sha256 FROM provider_inbox WHERE provider=$1 AND kind=$2 AND record_id=$3`,
		provider, it.Kind, it.RecordID).Scan(&owner, &version, &stored); err != nil {
		return "", fmt.Errorf("inbox classify: %w", err)
	}
	switch {
	case owner != user:
		return HookCollision, nil
	case string(stored) == string(sum[:]):
		return HookDuplicate, nil
	}
	return HookStale, nil
}

// ---- the consumer's reads and writes ------------------------------------------------------------------------------

// InboxItem is an inbox row as the consumer reads it.
type InboxItem struct {
	ID       int64
	UserID   uuid.UUID
	Provider string
	Kind     string
	RecordID string
	Version  int64
	Revision int    // bumped by each re-push of the record: the apply must still find the revision it read
	Payload  []byte // nil once processed or dropped
	State    string
	Attempts int
}

// InboxRow loads a row; ErrNotFound when it is gone (deleted by a disconnect).
func (s *Store) InboxRow(ctx context.Context, id int64) (InboxItem, error) {
	var it InboxItem
	var raw *string
	err := s.pool.QueryRow(ctx, `SELECT id, user_id, provider, kind, record_id, version, revision, payload::text, state, attempts
		FROM provider_inbox WHERE id=$1`, id).
		Scan(&it.ID, &it.UserID, &it.Provider, &it.Kind, &it.RecordID, &it.Version, &it.Revision, &raw, &it.State, &it.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxItem{}, ErrNotFound
	}
	if raw != nil {
		it.Payload = []byte(*raw)
	}
	return it, err
}

// PendingDirect lists up to limit pending sleep and hrv rows of (user, provider) other than except, oldest first,
// with their payloads: the rows the consumer may store in the same transaction as the one it holds (DirectRecord.Also).
// Rows past MaxInboxAttempts are left to their own delivery.
func (s *Store) PendingDirect(ctx context.Context, user uuid.UUID, provider string, except int64, limit int) ([]InboxItem, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, provider, kind, record_id, version, revision, payload::text, state, attempts
		FROM provider_inbox WHERE user_id=$1 AND provider=$2 AND state='pending' AND id <> $3 AND kind IN ('sleep','hrv')
		  AND payload IS NOT NULL AND attempts < $5
		ORDER BY received_at, id LIMIT $4`, user, provider, except, limit, MaxInboxAttempts-1)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (InboxItem, error) {
		var it InboxItem
		var raw *string
		if err := r.Scan(&it.ID, &it.UserID, &it.Provider, &it.Kind, &it.RecordID, &it.Version, &it.Revision, &raw, &it.State, &it.Attempts); err != nil {
			return InboxItem{}, err
		}
		if raw != nil {
			it.Payload = []byte(*raw)
		}
		return it, nil
	})
}

// BumpInboxAttempt counts one delivery of a pending row and returns the count; ErrNotFound when the row is gone or
// no longer pending.
func (s *Store) BumpInboxAttempt(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `UPDATE provider_inbox SET attempts = attempts + 1 WHERE id=$1 AND state='pending' RETURNING attempts`, id).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

// FinishInbox moves a pending row to dropped or failed with its code. A dropped row's payload goes at once; a
// failed one's stays for its 7 days (the purge deletes it).
func (s *Store) FinishInbox(ctx context.Context, id int64, state, code string) error {
	_, err := s.pool.Exec(ctx, `UPDATE provider_inbox SET state=$2, error=$3, processed_at=now(),
		payload = CASE WHEN $2 = 'failed' THEN payload ELSE NULL END WHERE id=$1 AND state='pending'`, id, state, code)
	return err
}

// ---- the tick's sweeps --------------------------------------------------------------------------------------------

// RepublishInbox writes the inbox.received event again for up to limit pending rows older than InboxRepublishAfter
// (an event lost or a consumer that never got to it), oldest first, in one transaction. The consumer is idempotent.
func (s *Store) RepublishInbox(ctx context.Context, limit int, emit func(tx pgx.Tx, ev model.InboxReceivedEvent) error) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, user_id, provider, kind, revision FROM provider_inbox
			WHERE state='pending' AND received_at < now() - $1::interval ORDER BY received_at LIMIT $2 FOR UPDATE SKIP LOCKED`,
			fmt.Sprintf("%d seconds", int(InboxRepublishAfter.Seconds())), limit)
		if err != nil {
			return err
		}
		evs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.InboxReceivedEvent, error) {
			var ev model.InboxReceivedEvent
			return ev, r.Scan(&ev.InboxID, &ev.UserID, &ev.Provider, &ev.Kind, &ev.Revision)
		})
		if err != nil {
			return err
		}
		for _, ev := range evs {
			if err := emit(tx, ev); err != nil {
				return err
			}
		}
		n = len(evs)
		return nil
	})
	return n, err
}

// ExpireInbox fails the pending rows older than InboxExpireAfter (code expired), at most limit.
func (s *Store) ExpireInbox(ctx context.Context, limit int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE provider_inbox SET state='failed', error='expired', processed_at=now()
		WHERE id IN (SELECT id FROM provider_inbox WHERE state='pending' AND received_at < now() - $1::interval LIMIT $2)`,
		fmt.Sprintf("%d seconds", int(InboxExpireAfter.Seconds())), limit)
	return tag.RowsAffected(), err
}

// PurgeInbox deletes processed and dropped rows InboxKeepDone after they left pending, and failed rows
// InboxKeepFailed after, at most limit of each; it returns how many of each state.
func (s *Store) PurgeInbox(ctx context.Context, limit int) (map[string]int64, error) {
	out := map[string]int64{}
	for state, keep := range map[string]time.Duration{"processed": InboxKeepDone, "dropped": InboxKeepDone, "failed": InboxKeepFailed} {
		tag, err := s.pool.Exec(ctx, `DELETE FROM provider_inbox WHERE id IN (SELECT id FROM provider_inbox WHERE state=$1
			AND processed_at < now() - $2::interval LIMIT $3)`, state, fmt.Sprintf("%d seconds", int(keep.Seconds())), limit)
		if err != nil {
			return out, err
		}
		out[state] = tag.RowsAffected()
	}
	return out, nil
}

// InboxAges are the ages in seconds of the oldest pending and the oldest failed row (0 when none), and how many
// failed rows there are.
type InboxAges struct {
	Pending, Failed float64
	FailedRows      int
}

// InboxAges reads the inbox's ages for the tick's gauges.
func (s *Store) InboxAges(ctx context.Context) (InboxAges, error) {
	var a InboxAges
	var p, f *float64
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT extract(epoch FROM now() - min(received_at)) FROM provider_inbox WHERE state='pending'),
		(SELECT extract(epoch FROM now() - min(processed_at)) FROM provider_inbox WHERE state='failed'),
		(SELECT count(*) FROM provider_inbox WHERE state='failed')`).Scan(&p, &f, &a.FailedRows)
	if p != nil {
		a.Pending = *p
	}
	if f != nil {
		a.Failed = *f
	}
	return a, err
}
