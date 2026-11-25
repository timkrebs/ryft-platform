package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition/score"
)

func TestN2RPostgresMorningGuidanceAndIsolation(t *testing.T) {
	st, root, a, b := nutritionTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC)
	const policy = "2026-10-01"
	if _, err := root.Exec(ctx, `UPDATE users SET tz='UTC' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutN2RProfile(ctx, a, model.N2RProfile{BodyMassKG: 70, ExpectedBedtime: "23:00"}); err != nil {
		t.Fatal(err)
	}
	if p, err := st.N2RProfile(ctx, b); err != nil || p != nil {
		t.Fatalf("cross account profile: %+v %v", p, err)
	}
	if _, err := st.PutNutritionGoals(ctx, a, model.NutritionGoalInput{Targets: map[string]float64{"energy_kcal": 2000}}); err != nil {
		t.Fatal(err)
	}
	// Simulate the typed biometric column supplied by direct wearable ingestion.
	if _, err := root.Exec(ctx, `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS hrv_metric text`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= 7; i++ {
		wake := now.AddDate(0, 0, -i)
		hrv, rhr := 44+float64(i), 50.0
		if i == 0 {
			hrv, rhr = 30, 53
		}
		if _, err := root.Exec(ctx, `INSERT INTO daily_scores(user_id,day,hrv_ms,hrv_metric,rhr,sleep_start,sleep_end,sleep_actual_min)
			VALUES($1,$2::date,$3,'rmssd',$4,$5,$6,450)`, a, wake.Format(time.DateOnly), hrv, rhr, wake.Add(-22*time.Hour), wake.Add(-14*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := root.Exec(ctx, `INSERT INTO workouts(id,user_id,activity,start_at,end_at,energy_kcal,strain)
		VALUES($1,$2,'strength', $3,$4,230,13)`, uuid.New(), a, now.Add(-4*time.Hour), now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	seedN2RFood(t, root, a)
	guide, err := st.N2RGuidance(ctx, a, now)
	if err != nil || guide.Score == nil || guide.Score.Budget.EnergyTarget != 2230 || guide.Biometrics == nil || guide.Biometrics.Change != "major" {
		t.Fatalf("guidance: %+v %v", guide, err)
	}
	var foundStrength, foundRed bool
	for _, card := range guide.Cards {
		if card.Code == score.GuideRed {
			foundRed = true
		}
		if card.Code != score.GuideStrength {
			continue
		}
		foundStrength = true
		if len(card.Foods) != 1 || card.Foods[0].Name != "Plain Skyr" {
			t.Fatalf("catalog candidates: %+v", card.Foods)
		}
	}
	if !foundStrength || !foundRed {
		t.Fatalf("missing cards: %+v", guide.Cards)
	}
	daily, err := st.NutritionDailyWithN2R(ctx, a, guide.Date, guide.Date, false, now, "")
	if err != nil || len(daily) != 1 || daily[0].N2R == nil || daily[0].N2R.Budget.EnergyTarget != 2230 {
		t.Fatalf("daily %+v %v", daily, err)
	}
	guide, err = st.N2RGuidance(ctx, b, now)
	if err != nil || guide.Score != nil || len(guide.Cards) != 0 {
		t.Fatalf("cross account guidance: %+v %v", guide, err)
	}

	enabled := func(uuid.UUID) bool { return true }
	if err := st.RefreshN2RNights(ctx, now, policy, enabled); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := root.QueryRow(ctx, `SELECT count(*) FROM nutrition.daily_biometric_baselines WHERE user_id=$1`, a).Scan(&count); err != nil || count != 0 {
		t.Fatalf("processing without consent: %d %v", count, err)
	}
	if err := st.GrantConsent(ctx, a, policy, nil); err != nil {
		t.Fatal(err)
	}
	for j := 0; j < 2; j++ {
		if err := st.RefreshN2RNights(ctx, now, policy, enabled); err != nil {
			t.Fatal(err)
		}
	}
	if err := root.QueryRow(ctx, `SELECT count(*) FROM nutrition.daily_biometric_baselines WHERE user_id=$1`, a).Scan(&count); err != nil || count != 2 {
		t.Fatalf("nightly upsert: %d %v", count, err)
	}
	// A wake day selected before withdrawal must lose its authority to process.
	due := dueNight{UserID: a, Date: now.Format(time.DateOnly)}
	if _, err := root.Exec(ctx, `UPDATE nutrition.daily_biometric_baselines
		SET processed_at='2001-01-01T00:00:00Z' WHERE user_id=$1 AND wake_date=$2::date`, a, due.Date); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeConsent(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.refreshN2RNight(ctx, due, now, policy); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if err := root.QueryRow(ctx, `SELECT processed_at='2001-01-01T00:00:00Z' FROM nutrition.daily_biometric_baselines
		WHERE user_id=$1 AND wake_date=$2::date`, a, due.Date).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("nightly processing after withdrawal: %v %v", unchanged, err)
	}
	if err := st.GrantConsent(ctx, a, policy, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.nutritionTx(ctx, b, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM nutrition.daily_biometric_baselines WHERE user_id=$1`, a).Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("baseline scope: %d %v", count, err)
	}
	if _, err := st.PutN2RProfile(ctx, a, model.N2RProfile{BodyMassKG: 70, ExpectedBedtime: "01:00"}); err != nil {
		t.Fatal(err)
	}
	guide, err = st.N2RGuidance(ctx, a, now.Add(210*time.Minute))
	if err != nil || guide.Date != now.Format(time.DateOnly) || guide.Score == nil || guide.Score.Bedtime == nil ||
		!guide.Score.Bedtime.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("early-morning guidance: %+v %v", guide, err)
	}
	if err := st.DeleteN2RProfile(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := root.QueryRow(ctx, `SELECT count(*) FROM nutrition.daily_biometric_baselines WHERE user_id=$1`, a).Scan(&count); err != nil || count != 0 {
		t.Fatalf("opt-out data: %d %v", count, err)
	}
}

func seedN2RFood(t *testing.T, root querier, owner uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	seed := func(label string, user *uuid.UUID, sugar float64) {
		id := uuid.New()
		var n int
		err := root.QueryRow(ctx, `WITH food AS (
			INSERT INTO nutrition.foods(id,owner_user_id,canonical_name,food_type,quality)
			VALUES ($1,$2,$3,CASE WHEN $2::uuid IS NULL THEN 'generic' ELSE 'custom' END,'source_reported') RETURNING id
		), source AS (
			INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
			SELECT id,id,CASE WHEN $2::uuid IS NULL THEN 'usda' ELSE 'user' END,id::text,'fixture','test' FROM food RETURNING id
		), name AS (
			INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized)
			SELECT id,id,'en',$3,lower($3) FROM source RETURNING food_id
		), nutrients AS (
			INSERT INTO nutrition.nutrient_values(food_id,source_id,code,amount,reference_amount,reference_unit,method,quality)
			SELECT food_id,food_id,n.code,n.amount,100,'g','fixture','source_reported' FROM name
			CROSS JOIN (VALUES('protein',10::numeric),('fat',.2),('added_sugar',$4::numeric)) n(code,amount) RETURNING 1
		) SELECT count(*) FROM nutrients`, id, user, label, sugar).Scan(&n)
		if err != nil || n != 3 {
			t.Fatalf("seed %s: %d %v", label, n, err)
		}
	}
	seed("Plain Skyr", nil, 0)
	seed("Sweet Skyr", nil, 19)
	seed("Private Skyr", &owner, 0)
}

func TestN2RPGVectorGuidance(t *testing.T) {
	if os.Getenv("RYFT_N2R_TEST_VECTOR") != "1" {
		t.Skip("enable on a disposable PostgreSQL server with pgvector")
	}
	st, root, a, _ := nutritionTestStore(t)
	ctx := context.Background()
	if _, err := root.Exec(ctx, `CREATE SCHEMA extensions; CREATE EXTENSION vector WITH SCHEMA extensions`); err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("../../../scripts/etl/embeddings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, string(ddl)); err != nil {
		t.Fatal(err)
	}
	seedN2RFood(t, root, a)
	if _, err := root.Exec(ctx, `INSERT INTO nutrition.food_embeddings(food_id,model,input_hash,embedding)
		SELECT id,'text-embedding-3-small',sha256(convert_to(canonical_name,'UTF8')),array_fill(.1::real,ARRAY[768])::extensions.vector
		FROM nutrition.foods WHERE canonical_name IN ('Plain Skyr','Sweet Skyr')`); err != nil {
		t.Fatal(err)
	}
	if err := st.nutritionTx(ctx, a, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		schema := "extensions"
		foods, err := guidanceFoods(ctx, tx, score.GuideStrength, &schema)
		if err != nil || len(foods) != 1 || foods[0].Name != "Plain Skyr" {
			t.Fatalf("pgvector guidance: %+v %v", foods, err)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
