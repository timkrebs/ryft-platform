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

// AdminUserDetail is everything the database knows for the admin user view;
// the api adds the Supabase identities and masks the emails. ErrNotFound when
// there is no such user.
func (s *Store) AdminUserDetail(ctx context.Context, id uuid.UUID) (model.AdminUserDetail, error) {
	var d model.AdminUserDetail
	a := &d.Account
	var policy, consentApp *string
	var grantedAt *time.Time
	err := s.pool.QueryRow(ctx, `SELECT u.id, u.created_at, u.provider, u.email, u.beta_access_at, u.max_hr, u.max_hr_source, u.tz,
		  c.policy_version, c.granted_at, c.app_version
		FROM users u LEFT JOIN LATERAL (SELECT policy_version, granted_at, app_version FROM consents
		  WHERE user_id = u.id AND kind = 'health_processing' AND revoked_at IS NULL ORDER BY granted_at DESC LIMIT 1) c ON true
		WHERE u.id = $1`, id).Scan(&a.ID, &a.CreatedAt, &a.Provider, &a.Email, &a.BetaAccessAt, &a.MaxHR, &a.MaxHRSource, &a.TZ,
		&policy, &grantedAt, &consentApp)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	a.ShortID = a.ID.String()[:8]
	if policy != nil && grantedAt != nil {
		a.Consent = &model.AdminConsent{PolicyVersion: *policy, GrantedAt: grantedAt.UTC(), AppVersion: consentApp}
	}

	rows, err := s.pool.Query(ctx, `SELECT d.id, d.name, d.app_version, d.os_version, d.created_at, d.last_sync_at,
		  d.last_sync_summary, p.env, p.last_success_at
		FROM devices d LEFT JOIN push_tokens p ON p.user_id = d.user_id AND p.device_id = d.id
		WHERE d.user_id = $1 ORDER BY d.last_sync_at DESC NULLS LAST, d.created_at DESC`, id)
	if err != nil {
		return d, err
	}
	d.Devices = []model.AdminDevice{}
	var dev model.AdminDevice
	var summary []byte
	if _, err = pgx.ForEachRow(rows, []any{&dev.ID, &dev.Name, &dev.AppVersion, &dev.OSVersion, &dev.FirstSyncAt, &dev.LastSyncAt,
		&summary, &dev.PushEnv, &dev.PushLastSuccessAt}, func() error {
		dev.LastSync = nil
		if summary != nil {
			dev.LastSync = append(json.RawMessage(nil), summary...)
		}
		d.Devices = append(d.Devices, dev)
		dev = model.AdminDevice{}
		return nil
	}); err != nil {
		return d, err
	}
	for _, dv := range d.Devices { // newest first: the last sync of the account
		if dv.LastSyncAt != nil {
			d.Data.LastSyncAt, d.Data.LastSync = dv.LastSyncAt, dv.LastSync
			break
		}
	}

	if err := s.pool.QueryRow(ctx, `SELECT count(*),
		  count(*) FILTER (WHERE COALESCE(deep_min, 0) + COALESCE(rem_min, 0) > 0),
		  count(*) FILTER (WHERE hrv_ms IS NOT NULL),
		  (SELECT count(*) FROM feedback WHERE user_id = $1 AND resolved_at IS NULL)
		FROM daily_scores WHERE user_id = $1 AND day > current_date - 14`, id).
		Scan(&d.Data.DaysScored14, &d.Data.NightsStaged14, &d.Data.DaysHRV14, &d.Data.OpenFeedback); err != nil {
		return d, err
	}
	rows, err = s.pool.Query(ctx, `SELECT kind, max(last_seen) FROM user_sources WHERE user_id = $1 GROUP BY kind`, id)
	if err != nil {
		return d, err
	}
	d.Data.Latest = map[string]time.Time{}
	var kind string
	var seen time.Time
	if _, err = pgx.ForEachRow(rows, []any{&kind, &seen}, func() error {
		d.Data.Latest[kind] = seen.UTC()
		return nil
	}); err != nil {
		return d, err
	}

	rows, err = s.pool.Query(ctx, `SELECT o.id, o.provider, max(od.last_sync_at),
		  (SELECT count(*) FROM daily_scores WHERE user_id = o.id), (SELECT max(last_seen) FROM user_sources WHERE user_id = o.id)
		FROM devices mine JOIN devices od ON od.id = mine.id AND od.user_id <> mine.user_id JOIN users o ON o.id = od.user_id
		WHERE mine.user_id = $1 AND od.last_sync_at > now() - interval '30 days'
		GROUP BY o.id, o.provider ORDER BY max(od.last_sync_at) DESC`, id)
	if err != nil {
		return d, err
	}
	d.Duplicates = []model.AdminDuplicate{}
	var dup model.AdminDuplicate
	_, err = pgx.ForEachRow(rows, []any{&dup.ID, &dup.Provider, &dup.LastSyncAt, &dup.DaysScored, &dup.NewestRecord}, func() error {
		dup.ShortID = dup.ID.String()[:8]
		d.Duplicates = append(d.Duplicates, dup)
		dup = model.AdminDuplicate{}
		return nil
	})
	return d, err
}
