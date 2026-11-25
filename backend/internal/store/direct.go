package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
)

// What a direct connection stores and what its end deletes (D257, design 7.3 and 8). The rows are samples and
// sleep_segments like any other, with origin 'direct', the provider as vendor, the reserved source
// ryft-direct-<provider> and the record's key; the direct user_sources row names the same source.

// deleteDirectData deletes everything a connection received: the direct samples and sleep segments of (user,
// provider) and the direct user_sources rows, and adds the local days they touched. It is written once, for
// Disconnect, the confirm's replace and relink, and the deregistration notice (S2). The caller holds lockApply and
// queues the days.
func deleteDirectData(ctx context.Context, tx pgx.Tx, user uuid.UUID, provider string, days *daySet) error {
	var typ string
	var at time.Time
	if _, err := deleteRows(ctx, tx, "direct samples",
		`DELETE FROM samples WHERE user_id=$1 AND origin='direct' AND vendor=$2 RETURNING type, start_at`,
		[]any{user, provider}, []any{&typ, &at}, func() {
			days.addTime(at)
			if model.SampleSpillsToNextDay(typ) {
				days.addTime(at.Add(24 * time.Hour))
			}
		}); err != nil {
		return err
	}
	if _, err := deleteRows(ctx, tx, "direct sleep",
		`DELETE FROM sleep_segments WHERE user_id=$1 AND origin='direct' AND vendor=$2 RETURNING end_at`,
		[]any{user, provider}, []any{&at}, func() { days.addTime(at) }); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_sources WHERE user_id=$1 AND origin='direct' AND name=$2`,
		user, connect.SourceName(provider)); err != nil {
		return fmt.Errorf("delete direct source: %w", err)
	}
	return nil
}

// userLoc is the account's time zone, for the local days of rows.
func userLoc(ctx context.Context, tx pgx.Tx, user uuid.UUID) (*time.Location, error) {
	var tz string
	if err := tx.QueryRow(ctx, `SELECT tz FROM users WHERE id=$1`, user).Scan(&tz); err != nil {
		return nil, err
	}
	// The sync refuses a bad zone; a bad stored one must not stop a deletion, so it reads as UTC.
	if loc, err := time.LoadLocation(tz); err == nil {
		return loc, nil
	}
	return time.UTC, nil
}

// DirectOutcome is what ApplyDirect and its siblings did with an inbox row.
type DirectOutcome string

// The outcomes of ApplyDirect and its siblings.
const (
	DirectApplied      DirectOutcome = "processed"
	DirectNotPending   DirectOutcome = "not_pending"   // already handled: a repeat of the event (D100)
	DirectNoConnection DirectOutcome = "no_connection" // the link ended meanwhile: the row is dropped
	DirectNoConsent    DirectOutcome = "no_consent"
)

// DirectRecord is one parsed inbox record to store. The consumer has screened its rows (window, stages, types,
// span) and set nothing of Origin, Vendor, Source or RecordID: ApplyDirect stamps them.
type DirectRecord struct {
	InboxID int64
	// Revision is the row's revision the payload was read at (InboxItem.Revision): a re-push since then makes the
	// row another revision, and this apply is then a repeat of nothing (DirectNotPending; the new revision has its
	// own event, P2-12).
	Revision  int
	UserID    uuid.UUID
	Provider  string
	Kind      string
	RecordID  string // the provider's record id (the inbox row's)
	MinPolicy string // consent must still meet this cutoff when the write lock is held
	Sleep     []model.SleepSegment
	Samples   []model.Sample
	// NightState is the provider's validation state of a night: model.RecordProvisional or RecordFinal.
	NightState string
	Loc        *time.Location
	// Emit runs in the transaction with the final days (the outbox insert of an ingested event, reason connect).
	Emit func(tx pgx.Tx, days []string) error
	// Also are further records of the same account and provider that ApplyDirect stores in the same transaction:
	// their days are queued with this record's and Emit runs once for all (P2-3: one event, not one per row, so a
	// big push does not put N events ahead of the account's next sync in scoring's per-user order). Their Emit is
	// not used. A row that is no longer pending by the time the lock is held (another delivery got there first) is
	// skipped; its own event finds it handled. At most MaxBatchRecords records and MaxBatchRows rows in all.
	Also []DirectRecord
}

// The bounds of one ApplyDirect transaction, which holds the account's apply lock (a sync's apply waits for it).
const (
	MaxBatchRecords = 25
	MaxBatchRows    = 1500
)

// DirectResult is what ApplyDirect and ApplyDeregistration did, and the days they queued.
type DirectResult struct {
	Outcome DirectOutcome
	Days    []string
	// Also are the inbox ids of DirectRecord.Also that this transaction stored.
	Also []int64
}

// ApplyDirect stores one record in one transaction under the user's apply lock: the record's earlier rows go (the
// same record id, within the new rows' span plus 36 h each way), the new ones are inserted, the direct source is
// noted, last_data_at moves, the touched days (deleted and new) are queued, Emit runs and the inbox row becomes
// processed with its payload gone. A repeat of the event finds the row no longer pending and does nothing. A link
// that ended since the hook stored the row or a consent that no longer meets the cutoff drops the row instead.
func (s *Store) ApplyDirect(ctx context.Context, r DirectRecord) (DirectResult, error) {
	var out DirectResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out = DirectResult{}
		if _, err := tx.Exec(ctx, lockApply, r.UserID); err != nil {
			return err
		}
		o, err := lockInbox(ctx, tx, r.InboxID, r.Revision, r.UserID, r.Provider, r.MinPolicy)
		if err != nil || o != "" {
			out.Outcome = o
			return err
		}
		days := newDaySet(r.Loc, nil)
		if err := storeDirect(ctx, tx, r, days); err != nil {
			return err
		}
		rows := len(r.Sleep) + len(r.Samples)
		for _, x := range r.Also {
			if len(out.Also)+1 >= MaxBatchRecords || rows+len(x.Sleep)+len(x.Samples) > MaxBatchRows {
				break
			}
			if x.UserID != r.UserID || x.Provider != r.Provider || x.InboxID == r.InboxID {
				continue // not this account's: the caller's bug, never stored
			}
			if o, err := lockInbox(ctx, tx, x.InboxID, x.Revision, x.UserID, x.Provider, r.MinPolicy); err != nil {
				return err
			} else if o != "" {
				continue
			}
			if err := storeDirect(ctx, tx, x, days); err != nil {
				return err
			}
			rows += len(x.Sleep) + len(x.Samples)
			out.Also = append(out.Also, x.InboxID)
		}
		out.Days = days.sorted()
		if err := queueAndEmit(ctx, tx, r.UserID, out.Days, r.Emit); err != nil {
			return err
		}
		for _, id := range out.Also {
			if err := markInbox(ctx, tx, id, "processed", ""); err != nil {
				return err
			}
		}
		out.Outcome = DirectApplied
		return markInbox(ctx, tx, r.InboxID, "processed", "")
	})
	if err != nil {
		return DirectResult{}, err
	}
	return out, nil
}

// storeDirect writes one record's rows in tx (the caller holds the apply lock and has locked the inbox row): the
// record's earlier rows go, the new ones are inserted, the direct source is noted and last_data_at moves. The days
// they touch are added to days.
func storeDirect(ctx context.Context, tx pgx.Tx, r DirectRecord, days *daySet) error {
	key := connect.RecordKey(r.Kind, r.RecordID)
	lo, hi := span(r)
	if err := replaceRecord(ctx, tx, r, key, lo.Add(-connect.MaxSpan), hi.Add(connect.MaxSpan), days); err != nil {
		return err
	}
	source := connect.SourceName(r.Provider)
	kinds := map[string]time.Time{}
	for _, x := range r.Sleep {
		if _, err := tx.Exec(ctx, `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, origin, vendor, record_id, record_state)
			VALUES ($1,$2,$3,$4,$5,'direct',$6,$7,NULLIF($8,''))
			ON CONFLICT (user_id, start_at, end_at, stage, source) DO UPDATE SET
			  origin='direct', vendor=EXCLUDED.vendor, record_id=EXCLUDED.record_id, record_state=EXCLUDED.record_state`,
			r.UserID, x.Start, x.End, x.Stage, source, r.Provider, key, r.NightState); err != nil {
			return fmt.Errorf("insert direct sleep: %w", err)
		}
		days.addTime(x.End)
		if x.End.After(kinds[model.KindSleep]) {
			kinds[model.KindSleep] = x.End
		}
	}
	for _, x := range r.Samples {
		if _, err := tx.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source, origin, vendor, record_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,'direct',$8,$9)
			ON CONFLICT (user_id, type, start_at, end_at, source) DO UPDATE SET
			  value=EXCLUDED.value, origin='direct', vendor=EXCLUDED.vendor, record_id=EXCLUDED.record_id`,
			r.UserID, x.Type, x.Start, x.End, x.Value, x.Unit, source, r.Provider, key); err != nil {
			return fmt.Errorf("insert direct sample: %w", err)
		}
		days.addTime(x.Start)
		if model.SampleSpillsToNextDay(x.Type) {
			days.addTime(x.Start.Add(24 * time.Hour))
		}
		if k := model.SampleKinds[x.Type]; k != "" && x.End.After(kinds[k]) {
			kinds[k] = x.End
		}
	}
	for kind, last := range kinds {
		if _, err := tx.Exec(ctx, `INSERT INTO user_sources (user_id, name, kind, last_seen, origin) VALUES ($1,$2,$3,$4,'direct')
			ON CONFLICT (user_id, name, kind) DO UPDATE SET last_seen = GREATEST(user_sources.last_seen, EXCLUDED.last_seen), origin='direct'`,
			r.UserID, source, kind, last); err != nil {
			return fmt.Errorf("direct source: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE connections SET last_data_at = now() WHERE user_id=$1 AND provider=$2`, r.UserID, r.Provider); err != nil {
		return fmt.Errorf("last data: %w", err)
	}
	return nil
}

// span is the first start and the last end of a record's rows.
func span(r DirectRecord) (lo, hi time.Time) {
	for _, x := range r.Sleep {
		lo, hi = widen(lo, hi, x.Start, x.End)
	}
	for _, x := range r.Samples {
		lo, hi = widen(lo, hi, x.Start, x.End)
	}
	return lo, hi
}

func widen(lo, hi, start, end time.Time) (time.Time, time.Time) {
	if lo.IsZero() || start.Before(lo) {
		lo = start
	}
	if end.After(hi) {
		hi = end
	}
	return lo, hi
}

// replaceRecord deletes the record's earlier rows in [lo, hi], with the existing indexes (user_id leading), and
// adds their days.
func replaceRecord(ctx context.Context, tx pgx.Tx, r DirectRecord, key string, lo, hi time.Time, days *daySet) error {
	var at time.Time
	var typ string
	if len(r.Sleep) > 0 {
		if _, err := deleteRows(ctx, tx, "replaced sleep",
			`DELETE FROM sleep_segments WHERE user_id=$1 AND origin='direct' AND vendor=$2 AND record_id=$3 AND end_at >= $4 AND end_at <= $5 RETURNING end_at`,
			[]any{r.UserID, r.Provider, key, lo, hi}, []any{&at}, func() { days.addTime(at) }); err != nil {
			return err
		}
	}
	if len(r.Samples) > 0 {
		types := make([]string, 0, 2)
		for _, x := range r.Samples {
			if !slices.Contains(types, x.Type) {
				types = append(types, x.Type)
			}
		}
		if _, err := deleteRows(ctx, tx, "replaced samples",
			`DELETE FROM samples WHERE user_id=$1 AND type = ANY($2) AND start_at >= $3 AND start_at <= $4
			   AND origin='direct' AND vendor=$5 AND record_id=$6 RETURNING type, start_at`,
			[]any{r.UserID, types, lo, hi, r.Provider, key}, []any{&typ, &at}, func() {
				days.addTime(at)
				if model.SampleSpillsToNextDay(typ) {
					days.addTime(at.Add(24 * time.Hour))
				}
			}); err != nil {
			return err
		}
	}
	return nil
}

// lockInbox reads the inbox row under lock and the link behind it. "" means go on; DirectNotPending, the row is
// not pending, or is no longer the revision the caller read (a repeat of the event, or a re-push since: the new
// revision has its own event); DirectNoConnection, the link is gone, revoked or has no provider user id any more and
// the row is now dropped. A row holds no provider user id (the hook resolved it to the account), so a link that was
// replaced by another provider account is told by the confirm's swap deleting the account's inbox rows, and by the
// hook's owner lookup holding the link's row (StoreInbox). A qualifying grant must still be active and cover
// the time this revision arrived. Withdrawal followed by regrant does not revive a previously queued revision.
func lockInbox(ctx context.Context, tx pgx.Tx, id int64, revision int, user uuid.UUID, provider, minPolicy string) (DirectOutcome, error) {
	var state string
	var rev int
	var consented bool
	err := tx.QueryRow(ctx, `SELECT p.state, p.revision,
		COALESCE((SELECT policy_version FROM consents
			WHERE user_id=$2 AND kind='health_processing' AND revoked_at IS NULL
			ORDER BY granted_at DESC LIMIT 1) >= $4, false)
		AND EXISTS (SELECT 1 FROM consents
			WHERE user_id=$2 AND kind='health_processing' AND revoked_at IS NULL
			AND policy_version >= $4 AND granted_at <= p.received_at)
		FROM provider_inbox p
		WHERE p.id=$1 AND p.user_id=$2 AND p.provider=$3 FOR UPDATE OF p`, id, user, provider, minPolicy).Scan(&state, &rev, &consented)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (state != "pending" || rev != revision)) {
		return DirectNotPending, nil
	}
	if err != nil {
		return "", err
	}
	var status string
	var puid *string
	err = tx.QueryRow(ctx, `SELECT status, provider_user_id FROM connections WHERE user_id=$1 AND provider=$2`, user, provider).Scan(&status, &puid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err != nil || status == model.ConnectionRevoked || puid == nil {
		if err := markInbox(ctx, tx, id, "dropped", "no_connection"); err != nil {
			return "", err
		}
		return DirectNoConnection, nil
	}
	if !consented {
		if err := markInbox(ctx, tx, id, "dropped", "no_consent"); err != nil {
			return "", err
		}
		return DirectNoConsent, nil
	}
	return "", nil
}

// markInbox moves a row out of pending, with its payload gone and its code.
func markInbox(ctx context.Context, tx pgx.Tx, id int64, state, code string) error {
	_, err := tx.Exec(ctx, `UPDATE provider_inbox SET state=$2, error=NULLIF($3,''), payload=NULL, processed_at=now() WHERE id=$1`, id, state, code)
	if err != nil {
		return fmt.Errorf("mark inbox: %w", err)
	}
	return nil
}

// ApplyDeregistration is the provider's notice that the user removed RYFT: in one transaction under the apply
// lock the link becomes revoked / provider_revoked (provider user id and permissions cleared, S6), the direct data
// and the direct source go, the other inbox rows of (user, provider) are deleted, the days are queued, Emit runs and
// the row is processed. The tokens stay for api's purge: ingest cannot touch them.
func (s *Store) ApplyDeregistration(ctx context.Context, inboxID int64, revision int, user uuid.UUID, provider, minPolicy string,
	emit func(tx pgx.Tx, days []string) error) (DirectResult, error) {
	var out DirectResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockApply, user); err != nil {
			return err
		}
		o, err := lockInbox(ctx, tx, inboxID, revision, user, provider, minPolicy)
		if err != nil || o != "" {
			out.Outcome = o
			return err
		}
		loc, err := userLoc(ctx, tx, user)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE connections SET status='revoked', status_reason='provider_revoked', provider_user_id=NULL,
			permissions='{}', status_at=now() WHERE user_id=$1 AND provider=$2`, user, provider); err != nil {
			return fmt.Errorf("revoke: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM provider_inbox WHERE user_id=$1 AND provider=$2 AND id <> $3`, user, provider, inboxID); err != nil {
			return fmt.Errorf("delete inbox: %w", err)
		}
		days := newDaySet(loc, nil)
		if err := deleteDirectData(ctx, tx, user, provider, days); err != nil {
			return err
		}
		out.Days = days.sorted()
		if err := queueAndEmit(ctx, tx, user, out.Days, emit); err != nil {
			return err
		}
		out.Outcome = DirectApplied
		return markInbox(ctx, tx, inboxID, "processed", "")
	})
	if err != nil {
		return DirectResult{}, err
	}
	return out, nil
}

// ApplyPermissions is the provider's notice of what the user now permits. missing says a permission RYFT needs is
// gone: a connected link becomes needs_reauth / permissions; with nothing missing a link that a permission change
// suspended becomes connected again. A needs_reauth for another reason (a failed refresh) is left alone. The
// changed permissions list itself is written by the api at the next confirm.
func (s *Store) ApplyPermissions(ctx context.Context, inboxID int64, revision int, user uuid.UUID, provider, minPolicy string, missing bool) (DirectOutcome, error) {
	var out DirectOutcome
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockApply, user); err != nil {
			return err
		}
		o, err := lockInbox(ctx, tx, inboxID, revision, user, provider, minPolicy)
		if err != nil || o != "" {
			out = o
			return err
		}
		if missing {
			_, err = tx.Exec(ctx, `UPDATE connections SET status='needs_reauth', status_reason='permissions', status_at=now()
				WHERE user_id=$1 AND provider=$2 AND status='connected'`, user, provider)
		} else {
			_, err = tx.Exec(ctx, `UPDATE connections SET status='connected', status_reason=NULL, status_at=now()
				WHERE user_id=$1 AND provider=$2 AND status='needs_reauth' AND status_reason='permissions'`, user, provider)
		}
		if err != nil {
			return fmt.Errorf("permissions: %w", err)
		}
		out = DirectApplied
		return markInbox(ctx, tx, inboxID, "processed", "")
	})
	return out, err
}
