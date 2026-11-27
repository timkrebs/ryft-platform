package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
)

func (h *handler) profilePolicy() func(string) bool {
	if h.profilePolicyNames != nil {
		return h.profilePolicyNames
	}
	return model.NamesProfileBody
}

func profileConsentRequired(w http.ResponseWriter, r *http.Request) {
	gate.Apply(w, r, config.ModeOn, true, model.CodeConsentRequired,
		"consent to a privacy policy that covers profile body data is required", "api")
}

func profileFail(w http.ResponseWriter, r *http.Request, err error) {
	// A database error can contain a profile value. Preserve timeout handling
	// without passing the database's error to the logger.
	if errors.Is(err, context.DeadlineExceeded) {
		httpx.Fail(w, r, context.DeadlineExceeded)
		return
	}
	httpx.Fail(w, r, profileErr(err))
}

// profileErr keeps what diagnoses a failure and holds no value: a Postgres error's SQLSTATE and constraint
// name (its message and detail can quote a row), otherwise the error's type.
func profileErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("profile store operation failed: sqlstate %s, constraint %q", pgErr.Code, pgErr.ConstraintName)
	}
	return fmt.Errorf("profile store operation failed: %T", err)
}

// withWeight resolves the public weight fields for GET and PATCH /v1/me.
func (h *handler) withWeight(r *http.Request, u model.User) (model.User, error) {
	var manual, healthkit *scoring.WeighIn
	if u.WeightManualKG != nil && u.WeightManualAt != nil {
		manual = &scoring.WeighIn{KG: *u.WeightManualKG, Source: model.WeightSourceManual, MeasuredAt: *u.WeightManualAt}
	}
	if u.Consent != nil && h.profilePolicy()(u.Consent.PolicyVersion) {
		ctx := r.Context()
		on, err := h.st.FitnessTypesEnabled(ctx, u.ID)
		if err != nil {
			return u, err
		}
		if on {
			kg, measuredAt, err := h.st.HealthKitWeighIn(ctx, u.ID)
			if err != nil {
				return u, err
			}
			if kg != nil {
				healthkit = &scoring.WeighIn{KG: *kg, Source: model.WeightSourceHealthKit, MeasuredAt: measuredAt}
			}
		}
	}
	u.WeightKG, u.WeightSource, u.WeightMeasuredAt = nil, nil, nil
	if current := scoring.CurrentWeighIn(manual, healthkit); current != nil {
		u.WeightKG, u.WeightSource = &current.KG, &current.Source
		u.WeightMeasuredAt = ptr(current.MeasuredAt.UTC())
	}
	return u, nil
}
