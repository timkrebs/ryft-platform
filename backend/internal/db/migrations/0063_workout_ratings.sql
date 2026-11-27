-- Recovery without a watch: the session RPE of a workout (docs/DECISIONS.md D291–D295, D293; docs/API.md →
-- PUT /v1/workouts/{id}/rpe). The user's own rating of how hard a workout felt, 1–10 in steps of 0.5 (Foster et
-- al. 2001); the coaching load counts it where heart rate and power cannot. One row per workout.
--
-- The rating belongs to the workout, not to a HealthKit id: a sync of the same HealthKit workout upserts the
-- workouts row and leaves this one, the same-source fold and a session's re-attach move it to the surviving
-- workout BEFORE they delete or detach the old one (store: fold.go, train.go), and a HealthKit deletion of the
-- workout removes it through the foreign key below (ON DELETE CASCADE, run by the owner, so no service needs a
-- grant on this table to delete workouts).
--
-- workouts has no unique (id, user_id), and building one is a lock on a large table, so the key to workouts is its
-- id alone: that the rating's user_id is the workout's user_id is the store's INSERT ... SELECT FROM workouts
-- WHERE id AND user_id, as for every user-scoped write (ingest may only repoint a rating, api alone creates one).
--
-- Written and read by the api on its request path (the coaching load is computed in the api's store). ingest
-- gets the one thing the fold needs: the same-source fold runs in ingest's sync (store.foldTx, and the
-- fold-workouts command), so it must move a dropped duplicate's rating to the survivor, or drop it when the
-- survivor has its own: it may read a rating, change which workout it points at (and when) and delete it, never
-- write a value or create one. scoring and notify get nothing: day strain does not use the rating (D293), and
-- scoring's DELETE on workouts cascades as the owner. The account's deletion and merge and the export read it from
-- the store; ON DELETE CASCADE on user_id is the safety net for the users row.
--
-- Additive: a new table, nothing renamed or dropped. Rollback = re-pin the old images; the table stays and nothing
-- old reads it. The migrator runs the file in one transaction, so the foreign keys' locks on users and workouts
-- (SHARE ROW EXCLUSIVE: writes of both wait, reads don't) are held to the commit; the table is new and empty, so
-- nothing is scanned, and lock_timeout bounds the wait (D116).
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS workout_ratings (
    user_id     uuid          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workout_id  uuid          NOT NULL REFERENCES workouts(id) ON DELETE CASCADE,
    -- 1, 1.5, ..., 10: a multiple of 0.5 within the range
    session_rpe numeric(3,1)  NOT NULL CONSTRAINT workout_ratings_rpe_check
                                  CHECK (session_rpe BETWEEN 1 AND 10 AND mod(session_rpe * 2, 1) = 0),
    created_at  timestamptz   NOT NULL DEFAULT now(),
    updated_at  timestamptz   NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, workout_id)
);
-- The cascade from workouts (a HealthKit deletion, the fold) and the move of a rating look it up by workout.
CREATE INDEX IF NOT EXISTS workout_ratings_workout ON workout_ratings (workout_id);

-- api: the app's read and write side, the re-attach's move, the account's deletion and the merge.
GRANT SELECT, INSERT, UPDATE, DELETE ON workout_ratings TO ryft_api;
-- ingest: the fold's move (UPDATE workout_id) or drop (DELETE) of a rating, and nothing else.
GRANT SELECT, DELETE ON workout_ratings TO ryft_ingest;
GRANT UPDATE (workout_id, updated_at) ON workout_ratings TO ryft_ingest;

DROP POLICY IF EXISTS workout_ratings_services ON workout_ratings;
CREATE POLICY workout_ratings_services ON workout_ratings TO ryft_api, ryft_ingest USING (true) WITH CHECK (true);

-- Supabase only, as in 0007 and 0016: RLS on, nothing for the Data API roles.
DO $$
BEGIN
    IF to_regprocedure('auth.uid()') IS NULL
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        RETURN;
    END IF;
    ALTER TABLE workout_ratings ENABLE ROW LEVEL SECURITY;
    REVOKE ALL ON workout_ratings FROM anon, authenticated, service_role;
END $$;
