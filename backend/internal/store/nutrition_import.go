package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/nutrition"
)

// StartNutritionImport creates or resumes a schema-owner import. Checkpoints
// refer to a particular staged artifact and a particular set of filter rules.
func (s *Store) StartNutritionImport(ctx context.Context, job nutrition.ImportJob) (nutrition.ImportJob, error) {
	var out nutrition.ImportJob
	newID := job.ID == uuid.Nil
	if newID {
		job.ID = uuid.New()
	}
	q := `UPDATE nutrition.import_jobs SET status='running',reason=NULL,updated_at=now(),completed_at=NULL
		WHERE id=$1 AND provider=$2 AND dataset_version=$3 AND dataset_sha256=$4 AND options_hash=$5 AND status<>'complete'`
	if newID {
		q = `INSERT INTO nutrition.import_jobs (id,provider,dataset_version,dataset_sha256,options_hash,status)
			VALUES ($1,$2,$3,$4,$5,'running')`
	}
	err := s.pool.QueryRow(ctx, q+` RETURNING id,provider,dataset_version,checkpoint,counts,status`,
		job.ID, job.Provider, job.Version, job.DatasetHash[:], job.OptionsHash[:]).
		Scan(&out.ID, &out.Provider, &out.Version, &out.Checkpoint, &out.Counts, &out.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNutritionConflict
	}
	return out, err
}

const nutritionImportLock = 7419020

var importedFoodNamespace = uuid.MustParse("a7e40c6e-bfb4-47eb-ad64-aea61136db20")

type sourceState struct {
	id, food  uuid.UUID
	version   string
	updatedAt *time.Time
	checksum  []byte
}

// ApplyNutritionImport commits catalog data and the checkpoint together. On a
// transaction failure the caller can retry the same batch or resume from the job.
func (s *Store) ApplyNutritionImport(ctx context.Context, job nutrition.ImportJob, next int64,
	records []nutrition.CatalogRecord, stats nutrition.ImportStats) (nutrition.ImportJob, error) {
	if next <= job.Checkpoint || int64(stats.Processed) != next-job.Checkpoint {
		return job, nutrition.ErrInvalid
	}
	out := job
	stats.Reasons = maps.Clone(stats.Reasons)
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::bigint)`, nutritionImportLock); err != nil {
			return err
		}
		var checkpoint int64
		var counts nutrition.ImportStats
		var state string
		if err := tx.QueryRow(ctx, `SELECT checkpoint,counts,status FROM nutrition.import_jobs
			WHERE id=$1 AND provider=$2 AND dataset_version=$3 FOR UPDATE`, job.ID, job.Provider, job.Version).
			Scan(&checkpoint, &counts, &state); err != nil {
			return err
		}
		if checkpoint != job.Checkpoint || state != "running" {
			return ErrNutritionConflict
		}
		current, err := getSourceStates(ctx, tx, job.Provider, records)
		if err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, r := range records {
			if r.Provider != job.Provider || r.Version != job.Version {
				return nutrition.ErrInvalid
			}
			v, err := queueImportRecord(batch, current[r.ExternalID], r, &stats)
			if err != nil {
				return err
			}
			current[r.ExternalID] = v
		}
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
		counts.Add(stats)
		data, err := json.Marshal(counts)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE nutrition.import_jobs SET checkpoint=$2,counts=$3::jsonb,updated_at=now() WHERE id=$1`,
			job.ID, next, string(data))
		out.Checkpoint, out.Counts = next, counts
		return err
	})
	if err != nil {
		return job, err
	}
	return out, nil
}

func getSourceStates(ctx context.Context, tx pgx.Tx, provider string, records []nutrition.CatalogRecord) (map[string]sourceState, error) {
	ids := make([]string, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.ExternalID)
	}
	rows, err := tx.Query(ctx, `SELECT external_id, id, food_id, dataset_version, source_updated_at, checksum
		FROM nutrition.food_sources WHERE provider=$1 AND external_id=ANY($2::text[])`, provider, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]sourceState)
	for rows.Next() {
		var key string
		var v sourceState
		if err := rows.Scan(&key, &v.id, &v.food, &v.version, &v.updatedAt, &v.checksum); err != nil {
			return nil, err
		}
		out[key] = v
	}
	return out, rows.Err()
}

func queueImportRecord(batch *pgx.Batch, old sourceState, r nutrition.CatalogRecord, stats *nutrition.ImportStats) (sourceState, error) {
	hash, err := nutritionHash(r)
	if err != nil {
		return old, err
	}
	canonical := uuid.NewSHA1(importedFoodNamespace, []byte(r.Provider+":"+r.ExternalID))
	switch {
	case bytes.Equal(old.checksum, hash[:]):
		stats.Unchanged++
		return old, nil
	case old.id == uuid.Nil:
		if r.Markets == nil {
			r.Markets = []string{}
		}
		batch.Queue(`INSERT INTO nutrition.foods(id,canonical_name,brand,food_type,quality,markets,completeness)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, canonical, r.Name, r.Brand, r.Type, r.Quality, r.Markets, r.Completeness())
		old = sourceState{id: canonical, food: canonical}
		batch.Queue(`INSERT INTO nutrition.food_sources
			(id,food_id,provider,external_id,dataset_version,source_url,license_identifier,source_updated_at,checksum)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, old.id, old.food, r.Provider, r.ExternalID,
			r.Version, r.URL, r.License, r.SourceUpdatedAt, hash[:])
		stats.Inserted++
	case old.version == r.Version || (old.updatedAt != nil && r.SourceUpdatedAt != nil && r.SourceUpdatedAt.Before(*old.updatedAt)):
		// Pin a version to a reproducible source record and keep newer upstream data.
		stats.Quarantined++
		if stats.Reasons == nil {
			stats.Reasons = make(map[string]int)
		}
		stats.Reasons["source_revision_conflict"]++
		return old, nil
	default:
		if old.food == canonical {
			if r.Markets == nil {
				r.Markets = []string{}
			}
			batch.Queue(`UPDATE nutrition.foods SET canonical_name=$2,brand=$3,quality=$4,markets=$5,
				completeness=$6,updated_at=now() WHERE id=$1`, old.food, r.Name, r.Brand, r.Quality, r.Markets, r.Completeness())
		}
		batch.Queue(`UPDATE nutrition.food_sources SET dataset_version=$2,source_url=$3,license_identifier=$4,
			source_updated_at=$5,checksum=$6,imported_at=now() WHERE id=$1`, old.id,
			r.Version, r.URL, r.License, r.SourceUpdatedAt, hash[:])
		batch.Queue(`DELETE FROM nutrition.food_names WHERE food_id=$1 AND source_id=$2`, old.food, old.id)
		batch.Queue(`DELETE FROM nutrition.nutrient_values WHERE food_id=$1 AND source_id=$2`, old.food, old.id)
		batch.Queue(`DELETE FROM nutrition.portions WHERE food_id=$1 AND source_id=$2`, old.food, old.id)
		batch.Queue(`DELETE FROM nutrition.barcodes WHERE food_id=$1 AND source_id=$2`, old.food, old.id)
		stats.Updated++
	}
	err = queueCatalogChildren(batch, old.food, old.id, r)
	old.version, old.updatedAt, old.checksum = r.Version, r.SourceUpdatedAt, hash[:]
	return old, err
}

// FinishNutritionImport records the final state if the checkpoint is still the caller's.
func (s *Store) FinishNutritionImport(ctx context.Context, job nutrition.ImportJob, reason string) error {
	status := "complete"
	if reason != "" {
		status = "failed"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE nutrition.import_jobs SET status=$2,reason=NULLIF($3,''),
		completed_at=now(),updated_at=now() WHERE id=$1 AND checkpoint=$4 AND status='running'`, job.ID, status, reason, job.Checkpoint)
	if err == nil && tag.RowsAffected() != 1 {
		err = ErrNutritionConflict
	}
	return err
}

// NutritionCatalogStats reports counts of source rows by provider.
func (s *Store) NutritionCatalogStats(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT provider,count(*) FROM nutrition.food_sources
		WHERE provider IN ('usda','off','ryft') GROUP BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var provider string
		var count int64
		if err := rows.Scan(&provider, &count); err != nil {
			return nil, err
		}
		out[provider] = count
	}
	return out, rows.Err()
}

// RetryNutritionImport identifies a batch failure whose transaction was rolled back.
func RetryNutritionImport(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01" || pgErr.Code == "55P03")
}
