package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// ErrAmbiguous: a short id matches more than one user.
var ErrAmbiguous = errors.New("the short id matches more than one user")

var shortID = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ResolveUser turns a full user id or its first 8 hex digits into the user's
// id: ErrNotFound when no user has it, ErrAmbiguous when a short id is not unique.
func (s *Store) ResolveUser(ctx context.Context, id string) (uuid.UUID, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if u, err := uuid.Parse(id); err == nil {
		var found uuid.UUID
		err := s.pool.QueryRow(ctx, `SELECT id FROM users WHERE id = $1`, u).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return found, err
	}
	if !shortID.MatchString(id) {
		return uuid.Nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM users WHERE id::text LIKE $1 || '%' LIMIT 2`, id)
	if err != nil {
		return uuid.Nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	switch {
	case err != nil:
		return uuid.Nil, err
	case len(ids) == 0:
		return uuid.Nil, ErrNotFound
	case len(ids) > 1:
		return uuid.Nil, ErrAmbiguous
	}
	return ids[0], nil
}

// SetBetaAccess grants (keeping the first grant's time) or clears an
// account's beta access without a code. ErrNotFound when there is no such user.
func (s *Store) SetBetaAccess(ctx context.Context, id uuid.UUID, grant bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET beta_access_at = CASE WHEN $2 THEN COALESCE(beta_access_at, now()) END
		WHERE id = $1`, id, grant)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AuditEntry is one admin_audit row (D88).
type AuditEntry struct {
	KeyFingerprint string
	Action         string // the route pattern
	TargetUser     *uuid.UUID
	Status         int
	RequestID      string
	Details        map[string]any
}

// Audit records an admin call.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO admin_audit (key_fingerprint, action, target_user, status, request_id, details)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6::jsonb)`, e.KeyFingerprint, e.Action, e.TargetUser, e.Status, e.RequestID, string(raw))
	return err
}

// AuditSince lists admin calls at or after since, newest first, at most limit.
func (s *Store) AuditSince(ctx context.Context, since time.Time, limit int) ([]model.AuditRecord, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, at, key_fingerprint, action, target_user, status, COALESCE(request_id, ''), details
		FROM admin_audit WHERE at >= $1 ORDER BY at DESC, id DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	out := []model.AuditRecord{}
	var a model.AuditRecord
	var details []byte
	_, err = pgx.ForEachRow(rows, []any{&a.ID, &a.At, &a.KeyFingerprint, &a.Action, &a.TargetUser, &a.Status, &a.RequestID, &details},
		func() error {
			a.At = a.At.UTC()
			a.Details = append(json.RawMessage(nil), details...)
			out = append(out, a)
			a = model.AuditRecord{}
			return nil
		})
	return out, err
}
