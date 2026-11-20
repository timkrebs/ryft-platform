package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// App Attest's state (D245, D247; migration 0037): the keys an account's installs attested and the
// challenges it has open. The cryptography is internal/appattest's; the store only keeps, caps and
// purges. A challenge is kept as its SHA-256 (appattest.ClientDataHash), never in clear.

// AppAttestKey is one attested key of an account (an app install).
type AppAttestKey struct {
	UserID      uuid.UUID
	KeyID       [32]byte
	PublicKey   []byte // 65-byte uncompressed P-256
	Environment string // "production"
	Counter     uint32
	AttestedAt  time.Time
	LastUsedAt  time.Time
}

const (
	// MaxOpenAttestChallenges is how many challenges an account may have open: a sixth
	// cancels the oldest.
	MaxOpenAttestChallenges = 5
	// MaxAttestKeys is how many keys an account keeps: an eleventh evicts the least
	// recently used.
	MaxAttestKeys = 10
	// AttestKeyIdle is how long an unused key is kept (PurgeAppAttest).
	AttestKeyIdle = 90 * 24 * time.Hour
)

// CreateAttestChallenge stores an open challenge (its hash) for the account, in one transaction:
// insert it, drop the account's expired ones (by the database's clock), and keep the newest
// MaxOpenAttestChallenges, the new one always among them. The account's lock (lockAttest) makes
// concurrent calls from several api instances count one after the other.
func (s *Store) CreateAttestChallenge(ctx context.Context, user uuid.UUID, hash [32]byte, expires time.Time) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockAttest, user); err != nil {
			return err
		}
		// clock_timestamp(), not now(): taken after the lock, so the new row is the newest even
		// when another transaction that started later held the lock first.
		if _, err := tx.Exec(ctx, `INSERT INTO app_attest_challenges (user_id, hash, expires_at, created_at)
			VALUES ($1, $2, $3, clock_timestamp())`, user, hash[:], expires); err != nil {
			return fmt.Errorf("insert challenge: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_attest_challenges WHERE user_id = $1 AND hash <> $2
			AND (expires_at <= now() OR hash NOT IN (SELECT o.hash FROM app_attest_challenges o
			  WHERE o.user_id = $1 AND o.hash <> $2 AND o.expires_at > now()
			  ORDER BY o.created_at DESC, o.expires_at DESC, o.hash DESC LIMIT $3))`,
			user, hash[:], MaxOpenAttestChallenges-1); err != nil {
			return fmt.Errorf("cap challenges: %w", err)
		}
		return nil
	})
}

// ConsumeAttestChallenge uses the account's challenge up: it is deleted whatever the answer
// (single use by DELETE ... RETURNING). True only when it was there and expires after now; a
// used, unknown, expired or another account's challenge is false (another account's stays as
// it is).
func (s *Store) ConsumeAttestChallenge(ctx context.Context, user uuid.UUID, hash [32]byte, now time.Time) (bool, error) {
	var expires time.Time
	err := s.pool.QueryRow(ctx, `DELETE FROM app_attest_challenges WHERE user_id = $1 AND hash = $2 RETURNING expires_at`,
		user, hash[:]).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return expires.After(now), nil
}

// AttestKey is the account's key by its id; ErrNotFound when there is none.
func (s *Store) AttestKey(ctx context.Context, user uuid.UUID, keyID [32]byte) (AppAttestKey, error) {
	k := AppAttestKey{UserID: user, KeyID: keyID}
	var counter int64
	err := s.pool.QueryRow(ctx, `SELECT public_key, environment, counter, attested_at, last_used_at
		FROM app_attest_keys WHERE user_id = $1 AND key_id = $2`, user, keyID[:]).
		Scan(&k.PublicKey, &k.Environment, &counter, &k.AttestedAt, &k.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AppAttestKey{}, ErrNotFound
	}
	if err != nil {
		return AppAttestKey{}, err
	}
	if counter < 0 || counter > 1<<32-1 { // the column's CHECK holds it; never wrap
		return AppAttestKey{}, fmt.Errorf("app attest counter out of range")
	}
	k.Counter = uint32(counter)
	return k, nil
}

// AttestationsSince counts the account's keys attested at or after since, and the oldest such
// attestation (zero when n is 0): POST /v1/app-attest/keys' 10 in 24 hours, and its Retry-After.
// Replaced and evicted keys no longer count; the cap of MaxAttestKeys makes that exact enough.
func (s *Store) AttestationsSince(ctx context.Context, user uuid.UUID, since time.Time) (n int, oldest time.Time, err error) {
	var first *time.Time
	if err = s.pool.QueryRow(ctx, `SELECT count(*), min(attested_at) FROM app_attest_keys
		WHERE user_id = $1 AND attested_at >= $2`, user, since).Scan(&n, &first); err != nil {
		return 0, time.Time{}, err
	}
	if first != nil {
		oldest = *first
	}
	return n, oldest, nil
}

// PutAttestKey stores a freshly attested key, in one transaction: an upsert on (user, key id) that
// sets the public key and environment, the counter to 0 and attested_at = last_used_at =
// k.AttestedAt (the database's now() when zero), then keeps the account's MaxAttestKeys most
// recently used keys, this one always among them. k.Counter and k.LastUsedAt are not read.
func (s *Store) PutAttestKey(ctx context.Context, k AppAttestKey) error {
	var at any // nil: the database's clock
	if !k.AttestedAt.IsZero() {
		at = k.AttestedAt
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockAttest, k.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app_attest_keys (user_id, key_id, public_key, environment, counter, attested_at, last_used_at)
			VALUES ($1, $2, $3, $4, 0, COALESCE($5::timestamptz, now()), COALESCE($5::timestamptz, now()))
			ON CONFLICT (user_id, key_id) DO UPDATE SET public_key = EXCLUDED.public_key, environment = EXCLUDED.environment,
			  counter = 0, attested_at = EXCLUDED.attested_at, last_used_at = EXCLUDED.last_used_at`,
			k.UserID, k.KeyID[:], k.PublicKey, k.Environment, at); err != nil {
			return fmt.Errorf("put key: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_attest_keys WHERE user_id = $1 AND key_id <> $2
			AND key_id NOT IN (SELECT o.key_id FROM app_attest_keys o WHERE o.user_id = $1 AND o.key_id <> $2
			  ORDER BY o.last_used_at DESC, o.attested_at DESC, o.key_id DESC LIMIT $3)`,
			k.UserID, k.KeyID[:], MaxAttestKeys-1); err != nil {
			return fmt.Errorf("cap keys: %w", err)
		}
		return nil
	})
}

// AdvanceAttestCounter stores an assertion's counter and when the key was used, as a
// compare-and-set: only when counter is above the stored one. False when it was not advanced
// (equal or lower: a replay, or two renewals that raced; or no such key).
func (s *Store) AdvanceAttestCounter(ctx context.Context, user uuid.UUID, keyID [32]byte, counter uint32, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE app_attest_keys SET counter = $3, last_used_at = $4
		WHERE user_id = $1 AND key_id = $2 AND counter < $3`, user, keyID[:], int64(counter), now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// PurgeAppAttest deletes every expired challenge (expires_at <= now: ConsumeAttestChallenge
// refuses those anyway) and every key unused for AttestKeyIdle (last_used_at before now minus
// 90 days), across accounts. For the api's tick; idempotent.
func (s *Store) PurgeAppAttest(ctx context.Context, now time.Time) (challenges, keys int64, err error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM app_attest_challenges WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, 0, fmt.Errorf("purge challenges: %w", err)
	}
	challenges = tag.RowsAffected()
	if tag, err = s.pool.Exec(ctx, `DELETE FROM app_attest_keys WHERE last_used_at < $1`, now.Add(-AttestKeyIdle)); err != nil {
		return challenges, 0, fmt.Errorf("purge keys: %w", err)
	}
	return challenges, tag.RowsAffected(), nil
}
