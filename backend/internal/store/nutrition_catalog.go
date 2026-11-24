package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/nutrition"
)

// ErrNutritionConflict identifies reuse of a request key or revision with different data.
var ErrNutritionConflict = errors.New("nutrition revision or request conflicts with a saved record")

// setNutritionScope binds RLS to the authenticated user for this transaction only.
func setNutritionScope(ctx context.Context, tx pgx.Tx, user uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT set_config('ryft.nutrition_user', $1, true)`, user.String())
	return err
}

func (s *Store) nutritionTx(ctx context.Context, user uuid.UUID, opts pgx.TxOptions, fn func(pgx.Tx) error) error {
	if user == uuid.Nil {
		return nutrition.ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, opts, func(tx pgx.Tx) error {
		if err := setNutritionScope(ctx, tx, user); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Stable namespace used with private account and client request IDs.
var privateFoodNamespace = uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db10")

func nutritionHash(v any) ([sha256.Size]byte, error) {
	data, err := json.Marshal(v)
	return sha256.Sum256(data), err
}

// HasNutritionData protects an account merge from discarding its private records.
func (s *Store) HasNutritionData(ctx context.Context, user uuid.UUID) (bool, error) {
	var exists bool
	err := s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			EXISTS (SELECT FROM nutrition.foods WHERE owner_user_id=$1)
			OR EXISTS (SELECT FROM nutrition.meals WHERE user_id=$1)
			OR EXISTS (SELECT FROM nutrition.goal_revisions WHERE user_id=$1)
			OR EXISTS (SELECT FROM nutrition.glucose_samples WHERE user_id=$1)
			OR EXISTS (SELECT FROM nutrition.n2r_profiles WHERE user_id=$1)`, user).Scan(&exists)
	})
	return exists, err
}

// SearchFoods ranks visible catalog names for the authenticated account.
func (s *Store) SearchFoods(ctx context.Context, user uuid.UUID, q model.FoodSearch) (model.FoodSearchPage, error) {
	page := model.FoodSearchPage{Foods: []model.FoodSummary{}}
	query := nutrition.Normalize(q.Query)
	if !nutrition.Text(q.Query, 100) || query == "" || !nutrition.Market(q.Market) ||
		(q.Language != "" && !nutrition.Language(q.Language)) || q.Limit < 0 || q.Limit > 50 ||
		q.Offset < 0 || q.Offset > 1000 {
		return page, nutrition.ErrInvalid
	}
	defer observeNutritionDB("search", time.Now())
	if q.Limit == 0 {
		q.Limit = 20
	}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, nutritionSearchSQL, query, q.Language, q.Market, q.Limit+1, q.Offset)
		if err != nil {
			return err
		}
		page.Foods, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.FoodSummary])
		if err == nil && len(page.Foods) > q.Limit {
			page.Foods = page.Foods[:q.Limit]
			if next := q.Offset + q.Limit; next <= 1000 {
				page.NextOffset = &next
			}
		}
		return err
	})
	return page, err
}

// LookupFoodBarcode returns distinct visible foods for a validated identifier.
func (s *Store) LookupFoodBarcode(ctx context.Context, user uuid.UUID, q model.FoodBarcodeQuery) (model.FoodBarcodeResult, error) {
	gtin, err := nutrition.GTIN(q.Barcode)
	out := model.FoodBarcodeResult{GTIN: gtin, Matches: []model.FoodSummary{}}
	if err != nil || !nutrition.Market(q.Market) {
		return out, nutrition.ErrInvalid
	}
	defer observeNutritionDB("barcode", time.Now())
	err = s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, nutritionBarcodeSQL, gtin, q.Market, user)
		if err != nil {
			return err
		}
		out.Matches, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.FoodSummary])
		out.Conflicted = len(out.Matches) > 1
		if len(out.Matches) > 20 {
			out.Matches = out.Matches[:20]
			out.Truncated = true
		}
		return err
	})
	return out, err
}

// Food loads an active food and its full reference profile.
func (s *Store) Food(ctx context.Context, user, id uuid.UUID) (model.FoodDetail, error) {
	var out model.FoodDetail
	err := s.nutritionTx(ctx, user, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = nutritionFood(ctx, tx, user, id)
		return err
	})
	return out, err
}

func nutritionFood(ctx context.Context, tx pgx.Tx, user, id uuid.UUID) (model.FoodDetail, error) {
	var f model.FoodDetail
	err := tx.QueryRow(ctx, `SELECT id, canonical_name, brand, food_type, quality, density_g_ml
		FROM nutrition.foods WHERE id=$1 AND status='active' AND (owner_user_id IS NULL OR owner_user_id=$2)`, id, user).
		Scan(&f.ID, &f.Name, &f.Brand, &f.Type, &f.Quality, &f.Density)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	batch := &pgx.Batch{}
	batch.Queue(`SELECT DISTINCT language, label, alias FROM nutrition.food_names WHERE food_id=$1 ORDER BY language, alias, label`, id).
		Query(func(rows pgx.Rows) error {
			var err error
			f.Names, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.FoodName])
			return err
		})
	batch.Queue(`SELECT id, provider, external_id, dataset_version, source_url, license_identifier, source_updated_at
		FROM nutrition.food_sources WHERE food_id=$1 ORDER BY id`, id).Query(func(rows pgx.Rows) error {
		var err error
		f.Sources, err = pgx.CollectRows(rows, pgx.RowToStructByPos[model.FoodSource])
		return err
	})
	batch.Queue(`SELECT id, source_id, label, labels, quantity, unit, weight_g, volume_ml, estimated
		FROM nutrition.portions WHERE food_id=$1 ORDER BY label, id`, id).Query(func(rows pgx.Rows) error {
		var err error
		f.Portions, err = pgx.CollectRows(rows, scanFoodPortion)
		return err
	})
	batch.Queue(`SELECT v.code, v.amount, n.unit, v.reference_amount, v.reference_unit, v.source_id, v.method, v.quality
		FROM nutrition.nutrient_values v JOIN nutrition.nutrients n USING(code)
		WHERE v.food_id=$1 ORDER BY v.code, v.source_id, v.reference_unit, v.reference_amount`, id).Query(func(rows pgx.Rows) error {
		var err error
		f.Nutrients, err = pgx.CollectRows(rows, scanFoodNutrient)
		return err
	})
	batch.Queue(`WITH candidates AS (
		SELECT source_id, code, contribution_fraction
		FROM nutrition.classifications
		WHERE food_id=$1 AND system='ryft_nutrition' AND version='1' AND contribution_fraction IS NOT NULL
			AND (code <> 'sugary_drink' OR contribution_fraction IN (0,1))
			AND code IN ('non_starchy_vegetable_fraction','whole_fruit_fraction','nuts_legumes_fraction',
				'red_processed_meat_fraction','sugary_drink')
	), profile AS (
		SELECT source_id FROM candidates GROUP BY source_id ORDER BY count(*) DESC, source_id LIMIT 1
	)
	SELECT jsonb_object_agg(code, CASE WHEN code='sugary_drink' THEN to_jsonb(contribution_fraction=1)
		ELSE to_jsonb(contribution_fraction) END) FROM candidates JOIN profile USING (source_id)`, id).
		QueryRow(func(row pgx.Row) error { return row.Scan(&f.QualityProfile) })
	err = tx.SendBatch(ctx, batch).Close()
	return f, err
}

func scanFoodPortion(row pgx.CollectableRow) (model.FoodPortion, error) {
	var p model.FoodPortion
	err := row.Scan(&p.ID, &p.SourceID, &p.Label, &p.Labels, &p.Quantity, &p.Unit, &p.WeightG, &p.VolumeML, &p.Estimated)
	return p, err
}

func scanFoodNutrient(row pgx.CollectableRow) (model.FoodNutrient, error) {
	var n model.FoodNutrient
	err := row.Scan(&n.Code, &n.Amount, &n.Unit, &n.ReferenceAmount, &n.ReferenceUnit, &n.SourceID, &n.Method, &n.Quality)
	return n, err
}

// FoodPortions lists available measures for an active, visible food.
func (s *Store) FoodPortions(ctx context.Context, user, id uuid.UUID) ([]model.FoodPortion, error) {
	var out []model.FoodPortion
	err := s.nutritionTx(ctx, user, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT p.id, p.source_id, p.label, p.labels, p.quantity, p.unit, p.weight_g, p.volume_ml, p.estimated
			FROM nutrition.foods f LEFT JOIN nutrition.portions p ON p.food_id=f.id
			WHERE f.id=$1 AND f.status='active' AND (f.owner_user_id IS NULL OR f.owner_user_id=$2)
			ORDER BY p.label, p.id`, id, user)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return ErrNotFound
		}
		out = []model.FoodPortion{}
		// A left-join sentinel distinguishes an empty portion list from a missing food.
		for {
			if rows.RawValues()[0] != nil {
				p, err := scanFoodPortion(rows)
				if err != nil {
					return err
				}
				out = append(out, p)
			}
			if !rows.Next() {
				return rows.Err()
			}
		}
	})
	return out, err
}

func registryUnits(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT code, unit FROM nutrition.nutrients`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var code, unit string
		if err := rows.Scan(&code, &unit); err != nil {
			return nil, err
		}
		out[code] = unit
	}
	return out, rows.Err()
}

// CreateCustomFood creates or replays an owner-private catalog record.
func (s *Store) CreateCustomFood(ctx context.Context, user uuid.UUID, in model.FoodCustomInput) (model.FoodDetail, error) {
	var out model.FoodDetail
	if in.ClientRequestID == uuid.Nil || !nutrition.Text(in.Name, 200) || nutrition.Normalize(in.Name) == "" ||
		!nutrition.ValidQualityProfile(in.QualityProfile) || !nutrition.Text(in.Brand, 160) || len(in.Names) > 20 || len(in.Portions) > 25 || len(in.Nutrients) > 120 {
		return out, nutrition.ErrInvalid
	}
	in.Name, in.Brand = strings.TrimSpace(in.Name), strings.TrimSpace(in.Brand)
	hash, err := nutritionHash(in)
	if err != nil {
		return out, nutrition.ErrInvalid
	}
	id := uuid.NewSHA1(privateFoodNamespace, []byte(user.String()+":"+in.ClientRequestID.String()))
	err = s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		record, err := customRecord(ctx, tx, id, in)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO nutrition.foods
			(id, owner_user_id, canonical_name, brand, food_type, quality, completeness, request_hash)
			VALUES ($1,$2,$3,$4,'custom','user_reported',$5,$6) ON CONFLICT (id) DO NOTHING`,
			id, user, record.Name, record.Brand, record.Completeness(), hash[:])
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var old []byte
			err = tx.QueryRow(ctx, `SELECT request_hash FROM nutrition.foods WHERE id=$1 AND owner_user_id=$2`, id, user).Scan(&old)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && !bytes.Equal(old, hash[:])) {
				return ErrNutritionConflict
			}
			if err != nil {
				return err
			}
		} else {
			if err := writeCatalogSource(ctx, tx, id, id, record, hash[:]); err != nil {
				return err
			}
			if err := writeQualityProfile(ctx, tx, id, in.QualityProfile); err != nil {
				return err
			}
		}
		out, err = nutritionFood(ctx, tx, user, id)
		return err
	})
	return out, err
}

func customRecord(ctx context.Context, tx pgx.Tx, id uuid.UUID, in model.FoodCustomInput) (nutrition.CatalogRecord, error) {
	record := nutrition.CatalogRecord{
		Name: in.Name, Brand: in.Brand, Type: "custom", Quality: "user_reported",
		Names: append([]model.FoodName{{Language: "und", Label: in.Name}}, in.Names...), Portions: in.Portions,
		Provider: "user", ExternalID: id.String(), Version: "1", License: "private",
	}
	names := make(map[[2]string]bool)
	for _, n := range record.Names {
		key := [2]string{n.Language, n.Label}
		if !nutrition.ValidName(n) || names[key] {
			return record, nutrition.ErrInvalid
		}
		names[key] = true
	}
	servings := 0
	for _, p := range record.Portions {
		if !nutrition.ValidPortion(p) {
			return record, nutrition.ErrInvalid
		}
		if p.Unit == "serving" {
			servings++
		}
	}
	if servings > 1 {
		return record, nutrition.ErrInvalid
	}
	units, err := registryUnits(ctx, tx)
	if err != nil {
		return record, err
	}
	type reference struct {
		code, unit string
		amount     float64
	}
	seen := make(map[reference]bool)
	for _, w := range in.Nutrients {
		if w.Amount == nil || (w.ReferenceUnit == "serving" && servings != 1) {
			return record, nutrition.ErrInvalid
		}
		n, err := nutrition.NormalizeNutrient(model.FoodNutrientInput{
			Code: w.Code, Amount: *w.Amount, Unit: w.Unit, ReferenceUnit: w.ReferenceUnit, ReferenceAmount: w.ReferenceAmount,
		}, units[w.Code])
		key := reference{n.Code, n.ReferenceUnit, n.ReferenceAmount}
		if err != nil || seen[key] {
			return record, nutrition.ErrInvalid
		}
		seen[key] = true
		record.Nutrients = append(record.Nutrients, model.FoodNutrient{FoodNutrientInput: n, Method: "user_entry", Quality: record.Quality})
	}
	return record, nil
}

func writeCatalogSource(ctx context.Context, tx pgx.Tx, foodID, sourceID uuid.UUID, r nutrition.CatalogRecord, checksum []byte) error {
	_, err := tx.Exec(ctx, `INSERT INTO nutrition.food_sources
		(id,food_id,provider,external_id,dataset_version,source_url,license_identifier,source_updated_at,checksum)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, sourceID, foodID, r.Provider, r.ExternalID, r.Version, r.URL, r.License, r.SourceUpdatedAt, checksum)
	if err != nil {
		return err
	}
	return writeCatalogChildren(ctx, tx, foodID, sourceID, r)
}

// Use array parameters for set-based writes, including through Supavisor exec mode.
func writeCatalogChildren(ctx context.Context, tx pgx.Tx, foodID, sourceID uuid.UUID, r nutrition.CatalogRecord) error {
	batch := &pgx.Batch{}
	if err := queueCatalogChildren(batch, foodID, sourceID, r); err != nil {
		return err
	}
	return tx.SendBatch(ctx, batch).Close()
}

func queueCatalogChildren(batch *pgx.Batch, foodID, sourceID uuid.UUID, r nutrition.CatalogRecord) error {
	if len(r.Names) > 0 {
		var langs, labels, normalized []string
		var aliases []bool
		for _, n := range r.Names {
			langs, labels = append(langs, n.Language), append(labels, n.Label)
			normalized, aliases = append(normalized, nutrition.Normalize(n.Label)), append(aliases, n.Alias)
		}
		batch.Queue(`INSERT INTO nutrition.food_names(food_id,source_id,language,label,normalized,normalized_brand,alias)
			SELECT $1,$2,n.language,n.label,n.normalized,$3,n.alias
			FROM unnest($4::text[],$5::text[],$6::text[],$7::boolean[]) n(language,label,normalized,alias)`,
			foodID, sourceID, nutrition.Normalize(r.Brand), langs, labels, normalized, aliases)
	}
	if len(r.Nutrients) > 0 {
		var codes, bases, methods, quality []string
		var amounts, refs []float64
		for _, n := range r.Nutrients {
			codes, bases = append(codes, n.Code), append(bases, n.ReferenceUnit)
			methods, quality = append(methods, n.Method), append(quality, n.Quality)
			amounts, refs = append(amounts, n.Amount), append(refs, n.ReferenceAmount)
		}
		batch.Queue(`INSERT INTO nutrition.nutrient_values(food_id,source_id,code,amount,reference_amount,reference_unit,method,quality)
			SELECT $1,$2,n.code,n.amount::numeric,n.ref::numeric,n.basis,n.method,n.quality
			FROM unnest($3::text[],$4::text[],$5::text[],$6::text[],$7::text[],$8::text[]) n(code,amount,ref,basis,method,quality)`,
			foodID, sourceID, codes, decimalStrings(amounts), decimalStrings(refs), bases, methods, quality)
	}
	for _, p := range r.Portions {
		labels := p.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		localized, err := json.Marshal(labels)
		if err != nil {
			return err
		}
		// Stable across dataset updates when the provider portion's definition is unchanged.
		definition, err := json.Marshal(p)
		if err != nil {
			return err
		}
		id := uuid.NewSHA1(sourceID, definition)
		batch.Queue(`INSERT INTO nutrition.portions(id,food_id,source_id,label,labels,quantity,unit,weight_g,volume_ml,estimated)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9,$10) ON CONFLICT (id) DO NOTHING`,
			id, foodID, sourceID, p.Label, string(localized), p.Quantity, p.Unit, p.WeightG, p.VolumeML, p.Estimated)
	}
	for _, b := range r.Barcodes {
		batch.Queue(`INSERT INTO nutrition.barcodes(food_id,source_id,original_barcode,gtin,market)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, foodID, sourceID, b.Original, b.GTIN, b.Market)
	}
	return nil
}

// SubmitFood records a private draft or advances it to submitted.
func (s *Store) SubmitFood(ctx context.Context, user uuid.UUID, in model.FoodSubmissionInput) (model.FoodSubmission, error) {
	var out model.FoodSubmission
	if in.FoodID == uuid.Nil || !slices.Contains([]string{"draft", "submitted"}, in.Status) {
		return out, nutrition.ErrInvalid
	}
	err := s.nutritionTx(ctx, user, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM nutrition.foods WHERE id=$1 AND owner_user_id=$2 AND status='active')`,
			in.FoodID, user).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO nutrition.submissions(id,user_id,food_id,status)
			VALUES ($1,$2,$3,$4) ON CONFLICT (user_id,food_id) DO NOTHING`, uuid.New(), user, in.FoodID, in.Status); err != nil {
			return err
		}
		if in.Status == "submitted" {
			if _, err := tx.Exec(ctx, `UPDATE nutrition.submissions SET status='submitted',updated_at=now()
				WHERE user_id=$1 AND food_id=$2 AND status='draft'`, user, in.FoodID); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT id,food_id,status,updated_at FROM nutrition.submissions WHERE user_id=$1 AND food_id=$2`, user, in.FoodID).
			Scan(&out.ID, &out.FoodID, &out.Status, &out.UpdatedAt)
	})
	return out, err
}

func decimalStrings(values []float64) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strconv.FormatFloat(value, 'f', -1, 64)
	}
	return out
}

const nutritionSearchSQL = `SELECT * FROM nutrition.search_foods($1,$2,$3,$4,$5)`

const nutritionBarcodeSQL = `SELECT f.id, f.canonical_name, f.brand, f.food_type, f.quality
FROM (SELECT food_id, bool_or(market=$2 AND $2<>'') AS in_market FROM nutrition.barcodes
      WHERE gtin=$1 GROUP BY food_id) b
JOIN nutrition.foods f ON f.id=b.food_id
WHERE f.status='active' AND (f.owner_user_id IS NULL OR f.owner_user_id=$3)
ORDER BY b.in_market DESC, f.completeness DESC, f.id LIMIT 21`

func observeNutritionDB(operation string, start time.Time) {
	metrics.NutritionDBDuration.WithLabelValues(operation).Observe(time.Since(start).Seconds())
}

func writeQualityProfile(ctx context.Context, tx pgx.Tx, foodID uuid.UUID, profile *model.NutritionQualityProfile) error {
	if profile == nil {
		return nil
	}
	body, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO nutrition.classifications
		(food_id,source_id,system,version,code,contribution_fraction)
		SELECT $1,$1,'ryft_nutrition','1',key,
			CASE WHEN key='sugary_drink' THEN (value::boolean)::int ELSE value::numeric END
		FROM jsonb_each_text($2::jsonb)`, foodID, string(body))
	return err
}
