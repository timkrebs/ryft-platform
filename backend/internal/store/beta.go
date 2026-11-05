package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/model"
)

// ErrInvalidCode is an invite redemption refused: the code is unknown, expired, used up, revoked, bound to
// another email than the account's confirmed one, or already redeemed by this account. It is one error for
// all of them (answered 422), so a caller cannot probe which codes exist or why one fails.
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

// FoldEmail is the form an address is compared in: trimmed, with the ASCII letters lower-cased and nothing
// else changed. No plus-address or dot folding (both would widen who may redeem a bound code), and no Unicode
// case folding: the Kelvin sign would otherwise lower-case to an ASCII k and match a mailbox it is not.
func FoldEmail(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSpace(s))
}

// ParseInviteEmail validates the address an invite is made for and returns it in the stored form: printable
// ASCII without spaces, at most 254 characters, one @, a name, and a domain of at least two non-empty labels.
// It matches the CHECK of beta_invite_codes.email (migration 0033), which is the backstop. The address is
// never looked up.
func ParseInviteEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 5 || len(s) > 254 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if s[i] <= ' ' || s[i] > '~' { // control characters, space, DEL and everything that is not ASCII
			return "", false
		}
	}
	e := FoldEmail(s)
	name, domain, ok := strings.Cut(e, "@")
	if !ok || name == "" || strings.Contains(domain, "@") {
		return "", false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, l := range labels {
		if l == "" {
			return "", false
		}
	}
	return e, true
}

// openInvite is the condition for a code that can be redeemed now (used by the lookup and by the
// statement that spends the use, so the two cannot drift). $1 is the stored code.
const openInvite = `code = $1 AND revoked_at IS NULL AND uses < max_uses AND (expires_at IS NULL OR expires_at > now())`

// BoundInviteEmail says whether code can be redeemed now and is bound to an email, and to which one. The
// caller asks Supabase for the account's confirmed email only then, so an unbound code never depends on
// that lookup and nothing is asked for a code that does not exist. A bound code that is not open reads
// as ("", false), like an unknown one: RedeemInvite refuses both the same way.
func (s *Store) BoundInviteEmail(ctx context.Context, code string) (email string, bound bool, err error) {
	norm := NormalizeInvite(code)
	if norm == "" {
		return "", false, nil
	}
	var e *string
	err = s.pool.QueryRow(ctx, `SELECT email FROM beta_invite_codes WHERE `+openInvite, norm).Scan(&e)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, err
	case e == nil:
		return "", false, nil
	}
	return *e, true, nil
}

// spendInvite uses one use of a code for an account and records who: one statement decides, so a use is
// never spent without its row and two accounts cannot both take the last one (the row lock of the UPDATE
// serialises them, and READ COMMITTED re-evaluates uses < max_uses and revoked_at for the second).
// $3 is the account's confirmed email, NULL when it has none: a bound code then never matches.
const spendInvite = `WITH spent AS (
	UPDATE beta_invite_codes SET uses = uses + 1
	WHERE ` + openInvite + `
	  AND (email IS NULL OR email = $3::text)
	  AND NOT EXISTS (SELECT 1 FROM beta_invite_uses WHERE code = $1 AND user_id = $2::uuid)
	RETURNING code)
INSERT INTO beta_invite_uses (code, user_id) SELECT code, $2::uuid FROM spent RETURNING code`

// RedeemInvite gives the user beta access with code. used says whether a use of the code was spent (false
// for an account that already had access, which is checked first and spends nothing). verifiedEmail is the
// email Supabase holds for the account and has confirmed ("" when it has none, it is not confirmed, or
// Supabase was not asked): a bound code is spent only for the same address, trimmed and lower-cased, with no
// plus-address or dot folding. Every refusal is ErrInvalidCode.
func (s *Store) RedeemInvite(ctx context.Context, userID uuid.UUID, code, verifiedEmail string) (used bool, err error) {
	var verified *string
	if e := FoldEmail(verifiedEmail); e != "" {
		verified = &e
	}
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
		var spent string
		err := tx.QueryRow(ctx, spendInvite, norm, userID, verified).Scan(&spent)
		var pgErr *pgconn.PgError
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrInvalidCode
		case errors.As(err, &pgErr) && pgErr.Code == "23505": // the primary key: this account has used the code
			return ErrInvalidCode
		case err != nil:
			return err
		}
		used = true
		_, err = tx.Exec(ctx, `UPDATE users SET beta_access_at = now(), beta_invite_code = $2 WHERE id=$1`, userID, norm)
		return err
	})
	return used, err
}

// NewInvite is what CreateInvite stores.
type NewInvite struct {
	Note      string
	MaxUses   int
	ExpiresAt *time.Time
	Email     *string // lower-cased and validated by the caller; nil: anyone with the code
	CreatedBy string  // "key:3fa1c2d9" or "user:<uuid>"
}

const inviteColumns = `code, note, max_uses, uses, expires_at, created_at, email, created_by, revoked_at`

func scanInvite(r pgx.Row) (model.BetaInvite, error) {
	var i model.BetaInvite
	var createdBy string
	err := r.Scan(&i.Code, &i.Note, &i.MaxUses, &i.Uses, &i.ExpiresAt, &i.CreatedAt, &i.Email, &createdBy, &i.RevokedAt)
	i.Code = ShowInvite(i.Code)
	i.CreatedBy = actorFromCreatedBy(createdBy)
	i.UsesBy = []model.InviteUse{}
	return i, err
}

// actorFromCreatedBy reads beta_invite_codes.created_by: "key" (the fingerprint was not kept: no id),
// "key:3fa1c2d9" or "user:<uuid>".
func actorFromCreatedBy(s string) model.Actor {
	kind, id, has := strings.Cut(s, ":")
	if !has || id == "" {
		return model.Actor{Kind: kind}
	}
	return model.Actor{Kind: kind, ID: &id}
}

// CreateInvite makes a new code (retrying the rare collision).
func (s *Store) CreateInvite(ctx context.Context, in NewInvite) (model.BetaInvite, error) {
	for attempt := 0; ; attempt++ {
		code, err := newInviteCode()
		if err != nil {
			return model.BetaInvite{}, err
		}
		inv, err := scanInvite(s.pool.QueryRow(ctx, `INSERT INTO beta_invite_codes (code, note, max_uses, expires_at, email, created_by)
			VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (code) DO NOTHING RETURNING `+inviteColumns,
			code, in.Note, in.MaxUses, in.ExpiresAt, in.Email, in.CreatedBy))
		if errors.Is(err, pgx.ErrNoRows) && attempt < 5 {
			continue
		}
		return inv, err
	}
}

// Invites lists every code, newest first, each with the accounts that redeemed it (oldest first) and still
// exist. Emails are as stored; the caller masks them. One snapshot, so a use is never listed without the
// code's count.
func (s *Store) Invites(ctx context.Context) ([]model.BetaInvite, error) {
	out := []model.BetaInvite{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+inviteColumns+` FROM beta_invite_codes ORDER BY created_at DESC, code`)
		if err != nil {
			return err
		}
		if out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.BetaInvite, error) { return scanInvite(r) }); err != nil {
			return err
		}
		at := make(map[string]int, len(out))
		for k, inv := range out {
			at[NormalizeInvite(inv.Code)] = k
		}
		rows, err = tx.Query(ctx, `SELECT i.code, i.user_id, u.email, i.at FROM beta_invite_uses i JOIN users u ON u.id = i.user_id
			ORDER BY i.at, i.user_id`)
		if err != nil {
			return err
		}
		var code string
		var u model.InviteUse
		_, err = pgx.ForEachRow(rows, []any{&code, &u.UserID, &u.Email, &u.At}, func() error {
			if k, ok := at[code]; ok {
				u.ShortID = u.UserID.String()[:8]
				u.At = u.At.UTC()
				out[k].UsesBy = append(out[k].UsesBy, u)
			}
			u = model.InviteUse{}
			return nil
		})
		return err
	})
	return out, err
}

// RevokeInvite revokes a code: it stays listed and nobody redeems it any more; accounts that redeemed it
// keep their access. Idempotent (the first revoke time is kept). It also caps expires_at at the revoke
// time, so an image from before migration 0033, which reads no revoked_at, refuses the code too.
// ErrNotFound when the code is unknown.
func (s *Store) RevokeInvite(ctx context.Context, code string) error {
	norm := NormalizeInvite(code)
	if norm == "" {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE beta_invite_codes
		SET revoked_at = COALESCE(revoked_at, now()),
		    expires_at = CASE WHEN revoked_at IS NULL THEN LEAST(COALESCE(expires_at, now()), now()) ELSE expires_at END
		WHERE code = $1`, norm)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// PurgeInviteEmails clears the email of every code that has not been redeemable for longer than age
// (revoked, expired, or used up that long ago): the address of someone who only asked for an invite is not
// kept for good. Idempotent. A used-up code whose account is gone has no use time left, so its creation
// time stands in. Clearing is safe: a code that cannot be redeemed cannot be widened by losing its binding.
func (s *Store) PurgeInviteEmails(ctx context.Context, age time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE beta_invite_codes c SET email = NULL
		WHERE c.email IS NOT NULL AND (
		  c.revoked_at < now() - $1::interval
		  OR c.expires_at < now() - $1::interval
		  OR (c.uses >= c.max_uses AND COALESCE((SELECT max(i.at) FROM beta_invite_uses i WHERE i.code = c.code), c.created_at)
		      < now() - $1::interval))`, fmt.Sprintf("%d seconds", int64(age.Seconds())))
	return tag.RowsAffected(), err
}
