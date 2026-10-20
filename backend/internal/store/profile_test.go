package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// D81: max_hr from the default, an estimate for the birth year, or by hand;
// manual wins until max_hr_manual false.
func TestMaxHRSources(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	u, err := st.EnsureUser(ctx, Identity{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	check := func(step string, u model.User, err error, hr int, source string, birth *int) {
		t.Helper()
		if err != nil || u.MaxHR != hr || u.MaxHRSource != source || (birth == nil) != (u.BirthYear == nil) ||
			(birth != nil && *birth != *u.BirthYear) {
			t.Fatalf("%s: max_hr %d source %s birth %v err %v", step, u.MaxHR, u.MaxHRSource, u.BirthYear, err)
		}
	}
	check("new", u, nil, 190, model.HRSourceDefault, nil)
	u, err = st.UpdateUser(ctx, id, model.UserPatch{BirthYear: ptr(1990)}, ptr(183))
	check("a birth year", u, err, 183, model.HRSourceEstimated, ptr(1990))
	u, err = st.UpdateUser(ctx, id, model.UserPatch{BirthYear: ptr(1980)}, ptr(176))
	check("another birth year", u, err, 176, model.HRSourceEstimated, ptr(1980))
	u, err = st.UpdateUser(ctx, id, model.UserPatch{TZ: ptr("UTC")}, ptr(176))
	check("another setting keeps it", u, err, 176, model.HRSourceEstimated, ptr(1980))
	u, err = st.UpdateUser(ctx, id, model.UserPatch{MaxHR: ptr(185)}, ptr(176))
	check("by hand", u, err, 185, model.HRSourceManual, ptr(1980))
	u, err = st.UpdateUser(ctx, id, model.UserPatch{BirthYear: ptr(1970)}, ptr(169))
	check("manual wins over a birth year", u, err, 185, model.HRSourceManual, ptr(1970))
	u, err = st.UpdateUser(ctx, id, model.UserPatch{MaxHRManual: ptr(false)}, ptr(169))
	check("back to the estimate", u, err, 169, model.HRSourceEstimated, ptr(1970))
	u, err = st.UpdateUser(ctx, id, model.UserPatch{MaxHRManual: ptr(true)}, ptr(169))
	check("freeze the estimate", u, err, 169, model.HRSourceManual, ptr(1970))

	other := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: other}); err != nil {
		t.Fatal(err)
	}
	u, err = st.UpdateUser(ctx, other, model.UserPatch{MaxHR: ptr(201)}, nil)
	check("by hand, no birth year", u, err, 201, model.HRSourceManual, nil)
	u, err = st.UpdateUser(ctx, other, model.UserPatch{MaxHRManual: ptr(false)}, nil)
	check("back to the default", u, err, 190, model.HRSourceDefault, nil)
}
