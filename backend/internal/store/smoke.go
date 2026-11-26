package store

import (
	"context"

	"github.com/google/uuid"
)

// IsSmokeAccount reports whether id is the account the operator bound as the smoke account
// (smoke_account, D286). The smoke sign-in checks it on every request, so a wrong SMOKE_USER_ID
// that names a real user is still refused. A database error is the caller's to treat as "cannot
// tell" (503), never as "not bound".
func (s *Store) IsSmokeAccount(ctx context.Context, id uuid.UUID) (bool, error) {
	var bound bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM smoke_account WHERE user_id=$1)`, id).Scan(&bound)
	return bound, err
}
