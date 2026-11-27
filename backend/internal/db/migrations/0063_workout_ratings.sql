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
-- The key to workouts is its primary key (user_id, id): since 0007 a workout id is unique per user only (one iPhone
-- can be signed into by two accounts), so a foreign key on id alone has no unique constraint to point at. The
-- composite key also makes the database keep the rating's user_id equal to the workout's. ON UPDATE CASCADE: the
-- account merge moves a workout with UPDATE workouts SET user_id, and its rating follows it (the cascade runs as
-- the owner).
--
-- Written and read by the api on its request path (the coaching load is computed in the api's store). ingest
-- gets the one thing the fold needs: the same-source fold runs in ingest's sync (store.foldTx, and the
-- fold-workouts command), so it must move a dropped duplicate's rating to the survivor before it deletes the
-- duplicate (the delete cascades, as the owner, to a rating the survivor already has): it may read the key columns
-- and updated_at (the latest rating wins) and change which workout a rating points at (and when), never read or
-- write a value, create one or delete one. scoring gets the same without updated_at: it re-attaches a Train
-- session to another workout, or back to its own (store.ReleaseSessionWorkouts), and the rating moves with the
-- session (SELECT of the two key columns, UPDATE of workout_id and updated_at; it never reads a rating's value).
-- A completed session's own workout (source 'ryft-session') whose times were edited is updated in place
-- (store.EnsureSessionWorkout, the column grant UPDATE (start_at, end_at) on workouts below), not deleted and
-- inserted again, so its rating stays. Day strain does not use the rating (D293). notify gets nothing. The account's deletion and merge and the export read it from the store; ON DELETE CASCADE
-- on user_id is the safety net for the users row.
--
-- Additive: a new table, nothing renamed or dropped. Rollback = re-pin the old images; the table stays and nothing
-- old reads it. The migrator runs the file in one transaction, so the foreign keys' locks on users and workouts
-- (SHARE ROW EXCLUSIVE: writes of both wait, reads don't) are held to the commit; the table is new and empty, so
-- nothing is scanned, and lock_timeout bounds the wait (D116).
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS workout_ratings (
    user_id     uuid          NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workout_id  uuid          NOT NULL,
    -- 1, 1.5, ..., 10: a multiple of 0.5 within the range
    session_rpe numeric(3,1)  NOT NULL CONSTRAINT workout_ratings_rpe_check
                                  CHECK (session_rpe BETWEEN 1 AND 10 AND mod(session_rpe * 2, 1) = 0),
    created_at  timestamptz   NOT NULL DEFAULT now(),
    updated_at  timestamptz   NOT NULL DEFAULT now(),
    -- the cascade from workouts (a HealthKit deletion, the fold) and the move of a rating find it by this key
    PRIMARY KEY (user_id, workout_id),
    CONSTRAINT workout_ratings_workout_fkey FOREIGN KEY (user_id, workout_id) REFERENCES workouts (user_id, id)
        ON DELETE CASCADE ON UPDATE CASCADE
);

-- api: the app's read and write side, the account's deletion and the merge.
GRANT SELECT, INSERT, UPDATE, DELETE ON workout_ratings TO ryft_api;
-- ingest: the fold's move of a rating to the surviving copy (which of two ratings is the latest: updated_at), never
-- its value and never a delete (a dropped copy's rating goes with its workout through the foreign key).
GRANT SELECT (user_id, workout_id, updated_at) ON workout_ratings TO ryft_ingest;
GRANT UPDATE (workout_id, updated_at) ON workout_ratings TO ryft_ingest;
-- scoring: the re-attach's move of a rating with its session, by key columns only.
GRANT SELECT (user_id, workout_id) ON workout_ratings TO ryft_scoring;
GRANT UPDATE (workout_id, updated_at) ON workout_ratings TO ryft_scoring;
-- scoring: a completed session's own workout follows an edit of the session's times in place, so its rating stays.
-- The statement is restricted to source 'ryft-session' (store.EnsureSessionWorkout); the grant is per column, not per
-- row, as for the load columns above. scoring still cannot change a workout's source, activity or owner.
GRANT UPDATE (start_at, end_at) ON workouts TO ryft_scoring;

DROP POLICY IF EXISTS workout_ratings_services ON workout_ratings;
CREATE POLICY workout_ratings_services ON workout_ratings TO ryft_api, ryft_ingest, ryft_scoring USING (true) WITH CHECK (true);

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
