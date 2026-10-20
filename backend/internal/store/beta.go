package store

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// ErrInvalidCode: unknown, expired or used up — one answer for all three (422).
var ErrInvalidCode = errors.New("invalid or used code")

// GrantConsent records the user's consent to policyVersion. A current consent
// to the same version is kept as it is (its granted_at stays).
func (s *Store) GrantConsent(ctx context.Context, userID uuid.UUID, policyVersion string, appVersion *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO consents (user_id, kind, policy_version, app_version)
		SELECT $1, 'health_processing', $2, $3
		WHERE NOT EXISTS (SELECT 1 FROM consents WHERE user_id=$1 AND kind='health_processing'
		  AND revoked_at IS NULL AND policy_version=$2)`, userID, policyVersion, appVersion)
	return err
}

// RevokeConsent withdraws every current consent of the user (none is fine).
func (s *Store) RevokeConsent(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE consents SET revoked_at = now()
		WHERE user_id=$1 AND kind='health_processing' AND revoked_at IS NULL`, userID)
	return err
}

// inviteAlphabet has no 0/O, 1/I/L: codes are read aloud and typed on a phone.
const inviteAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// NormalizeInvite turns what a user typed (any case, spaces, dashes) into the
// stored form RYFT7K3MQX9P; "" when it cannot be a code.
func NormalizeInvite(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\t", "").Replace(s))
	if !strings.HasPrefix(s, "RYFT") {
		s = "RYFT" + s
	}
	if len(s) != 12 {
		return ""
	}
	for _, r := range s[4:] {
		if !strings.ContainsRune(inviteAlphabet, r) {
			return ""
		}
	}
	return s
}

// ShowInvite formats a stored code for people: RYFT-7K3M-QX9P.
func ShowInvite(code string) string {
	if len(code) != 12 {
		return code
	}
	return code[:4] + "-" + code[4:8] + "-" + code[8:]
}

// newInviteCode draws 8 characters uniformly (bytes ≥ 248 are rejected, so the
// modulo is unbiased): 31^8 ≈ 8.5·10^11 codes.
func newInviteCode() (string, error) {
	out := make([]byte, 0, 8)
	b := make([]byte, 16)
	for len(out) < 8 {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		for _, v := range b {
			if int(v) < 248 && len(out) < 8 {
				out = append(out, inviteAlphabet[int(v)%len(inviteAlphabet)])
			}
		}
	}
	return "RYFT" + string(out), nil
}

// RedeemInvite gives the user beta access with code. used says whether a use
// of the code was spent (false for an account that already had access).
func (s *Store) RedeemInvite(ctx context.Context, userID uuid.UUID, code string) (used bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var has bool
		if err := tx.QueryRow(ctx, `SELECT beta_access_at IS NOT NULL FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&has); err != nil {
			return err
		}
		if has {
			return nil
		}
		norm := NormalizeInvite(code)
		if norm == "" {
			return ErrInvalidCode
		}
		tag, err := tx.Exec(ctx, `UPDATE beta_invite_codes SET uses = uses + 1
			WHERE code=$1 AND uses < max_uses AND (expires_at IS NULL OR expires_at > now())`, norm)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrInvalidCode
		}
		used = true
		_, err = tx.Exec(ctx, `UPDATE users SET beta_access_at = now(), beta_invite_code = $2 WHERE id=$1`, userID, norm)
		return err
	})
	return used, err
}

const inviteColumns = `code, note, max_uses, uses, expires_at, created_at`

func scanInvite(r pgx.Row) (model.BetaInvite, error) {
	var i model.BetaInvite
	err := r.Scan(&i.Code, &i.Note, &i.MaxUses, &i.Uses, &i.ExpiresAt, &i.CreatedAt)
	i.Code = ShowInvite(i.Code)
	return i, err
}

// CreateInvite makes a new code (retrying the rare collision).
func (s *Store) CreateInvite(ctx context.Context, note string, maxUses int, expiresAt *time.Time) (model.BetaInvite, error) {
	for attempt := 0; ; attempt++ {
		code, err := newInviteCode()
		if err != nil {
			return model.BetaInvite{}, err
		}
		inv, err := scanInvite(s.pool.QueryRow(ctx, `INSERT INTO beta_invite_codes (code, note, max_uses, expires_at)
			VALUES ($1,$2,$3,$4) ON CONFLICT (code) DO NOTHING RETURNING `+inviteColumns, code, note, maxUses, expiresAt))
		if errors.Is(err, pgx.ErrNoRows) && attempt < 5 {
			continue
		}
		return inv, err
	}
}

// Invites lists every code, newest first.
func (s *Store) Invites(ctx context.Context) ([]model.BetaInvite, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+inviteColumns+` FROM beta_invite_codes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.BetaInvite, error) { return scanInvite(r) })
}

// DeleteInvite removes a code (who redeemed it keeps access); ErrNotFound when unknown.
func (s *Store) DeleteInvite(ctx context.Context, code string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM beta_invite_codes WHERE code=$1`, NormalizeInvite(code))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
